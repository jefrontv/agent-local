package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Daemon hosts the router + agent API in one long-lived process.

// APIToken reads (or creates) the shared-secret token file.
func APIToken() (string, error) {
	p := P().Token()
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) >= 16 {
		return strings.TrimSpace(string(b)), nil
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(raw)
	if err := os.WriteFile(p, []byte(tok), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// RunDaemon is the `daemon` entrypoint.
func RunDaemon(background bool) error {
	store, err := OpenStore()
	if err != nil {
		return err
	}
	DiscoverInventory(store)
	store.Save()

	e := NewEngine(store)

	// Start previously-running sites' pools.
	for _, site := range store.Sites() {
		if site.State == StateRunning {
			if err := e.StartFPM(site.Slug, site.WPDir, site.PHPVersion); err != nil {
				log.Printf("fpm %s: %v", site.Slug, err)
			}
		}
	}
	for _, w := range store.Data.Worktrees {
		if e.FPMRunning(w.ID) {
			continue
		}
		// worktrees don't persist state; leave stopped
	}

	// HTTP front: router serves in-process; apache is a child process. The API
	// below runs under both fronts so agents never lose control.
	router := NewRouter(e)
	if err := applyFront(store); err != nil {
		log.Printf("front: %v", err)
	}
	if FrontKind(store) == "router" {
		if err := router.ListenAndServe(DefaultHTTPPort, DefaultHTTPSPort); err != nil {
			return fmt.Errorf("router: %w", err)
		}
	}
	// record daemon pid so `front` switching can restart us
	if pidf := filepath.Join(P().Run(), "daemon.pid"); pidf != "" {
		os.WriteFile(pidf, []byte(fmt.Sprint(os.Getpid())), 0o644)
	}

	// Agent API
	api := &APIServer{store: store, engine: e}
	mux := api.routes()
	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", DefaultAPIPort), Handler: api.auth(mux)}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("api: %v", err)
		}
	}()

	log.Printf("agent-local daemon: http :%d https :%d api :%d", DefaultHTTPPort, DefaultHTTPSPort, DefaultAPIPort)

	if background {
		// detach: daemon was spawned already detached by EnsureRouterDaemon;
		// just block until signaled.
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Printf("shutting down")
	return nil
}

// APIServer is the agent-facing HTTP control surface.
type APIServer struct {
	store  *Store
	engine *Engine
}

type apiResp struct {
	OK    bool        `json:"ok"`
	Error string      `json:"error,omitempty"`
	Data  interface{} `json:"data,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, apiResp{Error: msg})
}

func ok(w http.ResponseWriter, data interface{}) {
	writeJSON(w, 200, apiResp{OK: true, Data: data})
}

// auth wraps the mux with bearer-token auth.
func (a *APIServer) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.store.ReloadIfChanged()
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		want, err := APIToken()
		if err != nil {
			fail(w, 500, "token unavailable")
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			fail(w, 401, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *APIServer) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { ok(w, "alive") })
	mux.HandleFunc("GET /status", a.handleStatus)
	mux.HandleFunc("POST /sites", a.handleCreate)
	mux.HandleFunc("GET /sites", a.handleList)
	mux.HandleFunc("GET /sites/{slug}", a.handleGet)
	mux.HandleFunc("POST /sites/{slug}/start", a.handleStart)
	mux.HandleFunc("POST /sites/{slug}/stop", a.handleStop)
	mux.HandleFunc("POST /sites/{slug}/restart", a.handleRestart)
	mux.HandleFunc("DELETE /sites/{slug}", a.handleDelete)
	mux.HandleFunc("POST /sites/{slug}/php", a.handleSwitchPHP)
	mux.HandleFunc("POST /sites/{slug}/domain", a.handleDomain)
	mux.HandleFunc("POST /sites/{slug}/db", a.handleDB)
	mux.HandleFunc("POST /sites/{slug}/db/query", a.handleQuery)
	mux.HandleFunc("POST /sites/{slug}/wp-cli", a.handleWPCLI)
	mux.HandleFunc("POST /sites/{slug}/worktrees", a.handleAddWorktree)
	mux.HandleFunc("GET /sites/{slug}/worktrees", a.handleListWorktrees)
	mux.HandleFunc("POST /sites/{slug}/worktrees/{id}/stop", a.handleStopWorktree)
	mux.HandleFunc("POST /sites/{slug}/worktrees/{id}/start", a.handleStartWorktree)
	mux.HandleFunc("DELETE /sites/{slug}/worktrees/{id}", a.handleRemoveWorktree)
	mux.HandleFunc("GET /runtimes", a.handleRuntimes)
	mux.HandleFunc("POST /install", a.handleInstall)
	mux.HandleFunc("POST /db/query", a.handleQuery)
	mux.HandleFunc("GET /doctor", a.handleDoctor)
	mux.HandleFunc("POST /doctor/fix", a.handleDoctorFix)
	mux.HandleFunc("GET /logs/{name}", a.handleLogs)
	mux.HandleFunc("POST /hosts", a.handleHosts)
	mux.HandleFunc("POST /import", a.handleImport)
	mux.HandleFunc("POST /suffix", a.handleSuffix)
	mux.HandleFunc("GET /suffix", a.handleGetSuffix)
	mux.HandleFunc("GET /front", a.handleGetFront)
	mux.HandleFunc("DELETE /hosts", a.handleUnHosts)
	mux.HandleFunc("POST /front", a.handleSetFront)
	mux.HandleFunc("GET /sites/{slug}/branches", a.handleBranches)
	mux.HandleFunc("POST /sites/{slug}/worktrees/{id}/wp-cli", a.handleWorktreeWPCLI)
	mux.HandleFunc("POST /sites/{slug}/db/import", a.handleDBImport)
	mux.HandleFunc("POST /sites/{slug}/db/export", a.handleDBExport)
	mux.HandleFunc("POST /sites/{slug}/db/reset", a.handleDBReset)
	mux.HandleFunc("GET /sites/{slug}/db/tables", a.handleDBTables)
	mux.HandleFunc("GET /resolve", a.handleResolvePath)
	mux.HandleFunc("GET /certs/{domain}", a.handleCertStatus)
	mux.HandleFunc("POST /certs/{domain}/trust", a.handleCertTrust)
	mux.HandleFunc("POST /yield", a.handleYield)
	return mux
}

type createReq struct {
	Name       string `json:"name"`
	Domain     string `json:"domain"`
	PHPVersion string `json:"php_version"`
	WPVersion  string `json:"wp_version"`
	Repo       string `json:"repo"`
	AdminUser  string `json:"admin_user"`
	AdminPass  string `json:"admin_pass"`
	AdminEmail string `json:"admin_email"`
	Title      string `json:"title"`
}

func (a *APIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	e := a.engine
	st := map[string]interface{}{
		"version":   Version,
		"db":        map[string]interface{}{"running": e.DBRunning(), "port": DefaultDBPort},
		"http":      map[string]interface{}{"port": DefaultHTTPPort, "listening": portOpen(DefaultHTTPPort), "front": FrontKind(a.store)},
		"runtimes":  a.store.Inventory().Runtimes(),
		"sites":     len(a.store.Sites()),
		"worktrees": len(a.store.Data.Worktrees),
	}
	ok(w, st)
}

func (a *APIServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "bad json: "+err.Error())
		return
	}
	if req.Name == "" {
		fail(w, 400, "name required")
		return
	}
	site, err := a.engine.CreateSite(CreateOpts{
		Name: req.Name, Domain: req.Domain, PHPVersion: req.PHPVersion,
		WPVersion: req.WPVersion, Repo: req.Repo,
		AdminUser: req.AdminUser, AdminPass: req.AdminPass, AdminEmail: req.AdminEmail,
		Title: req.Title,
	})
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, site)
}

// handleList never returns secrets by default. `?include=secrets` opts in for
// a caller that genuinely needs every credential at once.
func (a *APIServer) handleList(w http.ResponseWriter, r *http.Request) {
	if wantSecrets(r) {
		ok(w, a.store.Sites())
		return
	}
	ok(w, publicSites(a.store.Sites()))
}

func (a *APIServer) handleGet(w http.ResponseWriter, r *http.Request) {
	site := a.store.Site(r.PathValue("slug"))
	if site == nil {
		fail(w, 404, "no such site")
		return
	}
	record := publicSite(site)
	if wantSecrets(r) {
		record = site
	}
	detail := map[string]interface{}{
		"site":      record,
		"running":   a.engine.FPMRunning(site.Slug),
		"url":       BareURL(site),
		"https_url": site.SURL(),
		"worktrees": a.store.WorktreesFor(site.Slug),
		// Credentials live here, not in the site record above.
		"db": dbBlock(site),
	}
	ok(w, detail)
}

// handleResolvePath answers "which site owns this directory?" — the lookup an
// integrator needs, because a UI keys sites by the checkout the user picked
// while we key them by slug.
//
// Matches a path inside a site, the site root itself, or a directory that
// *contains* exactly one site: a repo root one level above the docroot is the
// normal shape (`…/sulo` over `…/sulo/app/public`), and 404ing on it forced
// callers to pull the whole site list and prefix-match by hand. Several sites
// under one directory is ambiguous, so that answers 409 with the candidates
// rather than guessing.
func (a *APIServer) handleResolvePath(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		fail(w, 400, "path required")
		return
	}
	site, matched, wt := a.engine.SiteForPath(path)
	if site == nil {
		below := a.engine.SitesUnderPath(path)
		switch len(below) {
		case 0:
			fail(w, 404, "no site manages "+path)
			return
		case 1:
			site, matched, wt = below[0], "contains", nil
		default:
			slugs := make([]string, 0, len(below))
			for _, s := range below {
				slugs = append(slugs, s.Slug)
			}
			writeJSON(w, 409, apiResp{Error: fmt.Sprintf("%s contains %d sites: %s — pass one of their paths",
				path, len(below), strings.Join(slugs, ", "))})
			return
		}
	}
	out := a.runtimeInfo(site)
	out["matched"] = matched
	out["site"] = publicSite(site) // credentials are in out["db"]
	out["cert"] = InspectCert(site.Domain)
	if wt != nil {
		out["worktree"] = wt
		out["url"] = BareDomainURL(wt.Domain)
		out["domain"] = wt.Domain
		out["wp_dir"] = a.engine.wtServeDir(wt)
		out["running"] = a.engine.FPMRunning(wt.ID)
		out["cert"] = InspectCert(wt.Domain)
	}
	ok(w, out)
}

type yieldReq struct {
	Seconds int `json:"seconds"`
}

// handleYield frees the bare-URL ports (:80/:443) for a window so another
// local-dev app can pass its own port pre-check and bind first — LocalWP
// refuses to start when anything is listening, even on a different address.
// The front daemon reclaims its specific-address bind automatically, which the
// kernel allows alongside a wildcard listener, so both end up serving.
func (a *APIServer) handleYield(w http.ResponseWriter, r *http.Request) {
	var req yieldReq
	json.NewDecoder(r.Body).Decode(&req)
	secs := req.Seconds
	if secs <= 0 {
		secs = 45
	}
	if secs > 600 {
		secs = 600 // a longer hand-off is a stuck caller, not a use case
	}
	if !AliasActive() {
		ok(w, map[string]interface{}{
			"yielded": false,
			"detail":  "bare URLs are not enabled — nothing holds :80/:443",
		})
		return
	}
	until := time.Now().Add(time.Duration(secs) * time.Second)
	if err := os.MkdirAll(P().Run(), 0o755); err != nil {
		fail(w, 500, err.Error())
		return
	}
	if err := os.WriteFile(frontYieldPath(), []byte(fmt.Sprint(until.Unix())), 0o644); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, map[string]interface{}{
		"yielded":  true,
		"seconds":  secs,
		"until":    until.Format(time.RFC3339),
		"detail":   fmt.Sprintf("released :80/:443 for %ds; sites stay reachable on :%d", secs, DefaultHTTPPort),
		"reclaims": "automatic",
	})
}

func (a *APIServer) handleCertStatus(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	if !ValidDomain(domain) {
		fail(w, 400, "invalid domain")
		return
	}
	ok(w, InspectCert(domain))
}

// handleCertTrust issues the cert if absent, then trusts it. Non-interactive:
// the sudoers allowlist installed by `agent-local sudo` covers the keychain
// write, so a daemon-side call cannot land on an invisible password prompt.
func (a *APIServer) handleCertTrust(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	if !ValidDomain(domain) {
		fail(w, 400, "invalid domain")
		return
	}
	cert, _, _, err := EnsureCert(domain)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	if err := TrustCert(cert, false); err != nil {
		fail(w, 500, "trust failed (run `agent-local sudo` once to allow this without a prompt): "+err.Error())
		return
	}
	ok(w, InspectCert(domain))
}

// handleStart boots the site and returns everything a caller needs to talk to
// it: URL, docroot, PHP version and live DB connection details. One call, so a
// provider integration never has to guess or follow up.
func (a *APIServer) handleStart(w http.ResponseWriter, r *http.Request) {
	if err := a.engine.StartSite(r.PathValue("slug")); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, a.runtimeInfo(a.store.Site(r.PathValue("slug"))))
}

// runtimeInfo is the serving+connection snapshot shared by start and resolve.
func (a *APIServer) runtimeInfo(site *Site) map[string]interface{} {
	if site == nil {
		return map[string]interface{}{}
	}
	return map[string]interface{}{
		"slug":        site.Slug,
		"url":         BareURL(site),
		"https_url":   site.SURL(),
		"domain":      site.Domain,
		"wp_dir":      site.WPDir,
		"php_version": site.PHPVersion,
		"running":     a.engine.FPMRunning(site.Slug),
		"db":          dbBlock(site),
	}
}

// publicSite is a site record safe to hand out in bulk: the database and admin
// passwords are stripped. Listing every site's cleartext credentials meant one
// logged response leaked all of them, so secrets now travel only in the `db`
// block of a single-site call (`/resolve`, `/start`, `/sites/{slug}/db`), or
// when a caller opts in with `?include=secrets`.
func publicSite(site *Site) *Site {
	if site == nil {
		return nil
	}
	copy := *site
	copy.DBPass = ""
	copy.AdminPass = ""
	return &copy
}

// publicSites redacts a whole list.
func publicSites(sites []*Site) []*Site {
	out := make([]*Site, 0, len(sites))
	for _, s := range sites {
		out = append(out, publicSite(s))
	}
	return out
}

// wantSecrets reports an explicit `?include=secrets` opt-in.
func wantSecrets(r *http.Request) bool {
	return strings.Contains(r.URL.Query().Get("include"), "secrets")
}

func (a *APIServer) handleStop(w http.ResponseWriter, r *http.Request) {
	if err := a.engine.StopSite(r.PathValue("slug")); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "stopped")
}

func (a *APIServer) handleRestart(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	_ = a.engine.StopSite(slug)
	if err := a.engine.StartSite(slug); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "restarted")
}

// handleDelete removes a site. `?files=keep` leaves the checkout, `?db=keep`
// leaves the schema and user so the folder can be re-adopted later.
func (a *APIServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := a.engine.DeleteSite(r.PathValue("slug"), DeleteOpts{
		KeepFiles: q.Get("files") == "keep",
		KeepDB:    q.Get("db") == "keep",
	}); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "deleted")
}

type phpReq struct {
	Version string `json:"version"`
}

func (a *APIServer) handleSwitchPHP(w http.ResponseWriter, r *http.Request) {
	var req phpReq
	json.NewDecoder(r.Body).Decode(&req)
	if req.Version == "" {
		fail(w, 400, "version required")
		return
	}
	if err := a.engine.SwitchPHP(r.PathValue("slug"), req.Version); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "php "+req.Version)
}

type domainReq struct {
	Domain string `json:"domain"`
}

func (a *APIServer) handleDomain(w http.ResponseWriter, r *http.Request) {
	var req domainReq
	json.NewDecoder(r.Body).Decode(&req)
	if err := a.engine.SetDomain(r.PathValue("slug"), req.Domain); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, req.Domain)
}

// handleDB returns the site's connection details, starting MariaDB if needed.
// Same nested `db` block as /resolve and /start: one spelling everywhere beat
// keeping a second flat one, which cost an integrator a bug where `pass` read
// as empty and looked like a credentials failure.
func (a *APIServer) handleDB(w http.ResponseWriter, r *http.Request) {
	site := a.store.Site(r.PathValue("slug"))
	if site == nil {
		fail(w, 404, "no such site")
		return
	}
	if err := a.engine.EnsureDB(); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, map[string]interface{}{"slug": site.Slug, "db": dbBlock(site)})
}

// dbBlock is the one description of how to reach a site's database.
// The socket is always empty: one shared MariaDB on TCP, unlike LocalWP's
// per-site unix socket, so callers must not carry a stale socket path.
func dbBlock(site *Site) map[string]interface{} {
	return map[string]interface{}{
		"host":   "127.0.0.1",
		"port":   DefaultDBPort,
		"socket": "",
		"name":   site.DBName,
		"user":   site.DBUser,
		"pass":   site.DBPass,
	}
}

type queryReq struct {
	SQL string `json:"sql"`
}

// handleQuery runs SQL as root. Mounted twice: globally, and under a site
// where the site's database becomes the default schema so agents can write
// unqualified SQL.
func (a *APIServer) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req queryReq
	json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.SQL) == "" {
		fail(w, 400, "sql required")
		return
	}
	db := ""
	if slug := r.PathValue("slug"); slug != "" {
		site := a.store.Site(slug)
		if site == nil {
			fail(w, 404, "no such site")
			return
		}
		db = site.DBName
	}
	out, err := a.engine.DBIn(db, req.SQL)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, out)
}

type sqlFileReq struct {
	Path     string `json:"path"`
	KeepURLs bool   `json:"keep_urls"` // skip domain search-replace
}

// handleDBImport replaces a site's database contents with a .sql/.sql.gz dump.
func (a *APIServer) handleDBImport(w http.ResponseWriter, r *http.Request) {
	var req sqlFileReq
	json.NewDecoder(r.Body).Decode(&req)
	if req.Path == "" {
		fail(w, 400, "path required")
		return
	}
	msg, err := a.engine.ImportSQL(r.PathValue("slug"), req.Path, !req.KeepURLs)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, msg)
}
func (a *APIServer) handleDBExport(w http.ResponseWriter, r *http.Request) {
	var req sqlFileReq
	json.NewDecoder(r.Body).Decode(&req)
	msg, err := a.engine.ExportSQL(r.PathValue("slug"), req.Path)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, msg)
}

func (a *APIServer) handleDBReset(w http.ResponseWriter, r *http.Request) {
	if err := a.engine.ResetDB(r.PathValue("slug")); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "database emptied")
}

// handleDBTables lists tables with row counts and size, the orientation query
// agents otherwise hand-write every time.
func (a *APIServer) handleDBTables(w http.ResponseWriter, r *http.Request) {
	site := a.store.Site(r.PathValue("slug"))
	if site == nil {
		fail(w, 404, "no such site")
		return
	}
	out, err := a.engine.DB(fmt.Sprintf(
		"SELECT table_name, table_rows, ROUND(data_length/1024) AS kb FROM information_schema.tables "+
			"WHERE table_schema='%s' ORDER BY table_name", site.DBName))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, out)
}

type wpcliReq struct {
	Args []string `json:"args"`
}

func (a *APIServer) handleWPCLI(w http.ResponseWriter, r *http.Request) {
	site := a.store.Site(r.PathValue("slug"))
	if site == nil {
		fail(w, 404, "no such site")
		return
	}
	var req wpcliReq
	json.NewDecoder(r.Body).Decode(&req)
	out, err := wpCLI(site, req.Args...)
	if err != nil {
		fail(w, 500, err.Error()+" "+out)
		return
	}
	ok(w, out)
}

// handleWorktreeWPCLI runs wp-cli inside a preview's docroot: same DB and PHP
// version as the base site, but the branch's code.
func (a *APIServer) handleWorktreeWPCLI(w http.ResponseWriter, r *http.Request) {
	site := a.store.Site(r.PathValue("slug"))
	if site == nil {
		fail(w, 404, "no such site")
		return
	}
	wt, okw := a.store.Data.Worktrees[r.PathValue("id")]
	if !okw {
		fail(w, 404, "no such worktree")
		return
	}
	var req wpcliReq
	json.NewDecoder(r.Body).Decode(&req)
	out, err := wpCLIAt(site, a.engine.wtServeDir(wt), req.Args...)
	if err != nil {
		fail(w, 500, err.Error()+" "+out)
		return
	}
	ok(w, out)
}

func (a *APIServer) handleBranches(w http.ResponseWriter, r *http.Request) {
	res, err := a.engine.Branches(r.PathValue("slug"))
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	ok(w, res)
}

func (a *APIServer) handleGetSuffix(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]string{"suffix": a.store.Suffix()})
}

func (a *APIServer) handleGetFront(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]interface{}{
		"front":            FrontKind(a.store),
		"apache_installed": a.store.Inventory().HTTP.Bin != "",
		"choices":          []string{"router", "apache"},
	})
}

type frontReq struct {
	Front string `json:"front"`
}

// handleSetFront switches the HTTP front. The swap must outlive this process
// (the daemon owns the ports in router mode), so it is handed to a detached
// `agent-local front <kind>` after the response is flushed.
func (a *APIServer) handleSetFront(w http.ResponseWriter, r *http.Request) {
	var req frontReq
	json.NewDecoder(r.Body).Decode(&req)
	if req.Front != "router" && req.Front != "apache" {
		fail(w, 400, "front must be router or apache")
		return
	}
	if req.Front == "apache" && a.store.Inventory().HTTP.Bin == "" {
		fail(w, 400, "apache not installed; install_runtime apache first")
		return
	}
	msg := "switching front to " + req.Front + "; ports rebind within a few seconds"
	if FrontKind(a.store) == req.Front {
		msg = "re-applying " + req.Front + " front (config re-rendered, front restarted)"
	}
	self, err := os.Executable()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, msg)
	go func() {
		time.Sleep(300 * time.Millisecond)
		cmd := exec.Command(self, "front", req.Front)
		// Detached: send its output to daemon.log so a failed switch is
		// visible to agents through get_logs instead of vanishing.
		if logf, err := os.OpenFile(P().Log("daemon"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			cmd.Stdout, cmd.Stderr = logf, logf
		}
		_ = cmd.Start()
	}()
}

type worktreeReq struct {
	Branch string `json:"branch"`
}

func (a *APIServer) handleAddWorktree(w http.ResponseWriter, r *http.Request) {
	var req worktreeReq
	json.NewDecoder(r.Body).Decode(&req)
	if req.Branch == "" {
		fail(w, 400, "branch required")
		return
	}
	wt, err := a.engine.AddWorktree(r.PathValue("slug"), req.Branch)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, map[string]interface{}{
		"id":     wt.ID,
		"branch": wt.Branch,
		"domain": wt.Domain,
		"url":    BareDomainURL(wt.Domain),
		"path":   wt.Path,
	})
}

func (a *APIServer) handleStartWorktree(w http.ResponseWriter, r *http.Request) {
	if err := a.engine.StartWorktree(r.PathValue("id")); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "started")
}

func (a *APIServer) handleStopWorktree(w http.ResponseWriter, r *http.Request) {
	if err := a.engine.StopWorktree(r.PathValue("id")); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "stopped")
}

func (a *APIServer) handleRemoveWorktree(w http.ResponseWriter, r *http.Request) {
	if err := a.engine.RemoveWorktree(r.PathValue("id")); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "removed")
}

func (a *APIServer) handleListWorktrees(w http.ResponseWriter, r *http.Request) {
	out := []map[string]interface{}{}
	for _, wt := range a.store.WorktreesFor(r.PathValue("slug")) {
		state := "stopped"
		if a.engine.FPMRunning(wt.ID) {
			state = "running"
		}
		out = append(out, map[string]interface{}{
			"id":     wt.ID,
			"branch": wt.Branch,
			"domain": wt.Domain,
			"url":    BareDomainURL(wt.Domain),
			"path":   wt.Path,
			"state":  state,
		})
	}
	ok(w, out)
}

func (a *APIServer) handleRuntimes(w http.ResponseWriter, r *http.Request) {
	ok(w, a.store.Inventory())
}

type installReq struct {
	What    string `json:"what"`    // php | mariadb | apache | brew
	Version string `json:"version"` // php version
}

func (a *APIServer) handleInstall(w http.ResponseWriter, r *http.Request) {
	var req installReq
	json.NewDecoder(r.Body).Decode(&req)
	cb := func(line string) { log.Printf("install: %s", line) }
	var err error
	switch req.What {
	case "php":
		if req.Version == "" {
			req.Version = "8.3"
		}
		err = InstallPHP(a.store, req.Version, cb)
	case "mariadb", "mysql":
		err = InstallMySQL(a.store, cb)
	case "apache", "httpd":
		err = InstallApache(a.store, cb)
	case "brew", "homebrew":
		err = InstallBrew(cb)
	default:
		fail(w, 400, "what: php|mariadb|apache|brew")
		return
	}
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	a.store.Save()
	ok(w, "installed "+req.What)
}

func (a *APIServer) handleDoctor(w http.ResponseWriter, r *http.Request) {
	ok(w, Doctor(a.store))
}

func (a *APIServer) handleDoctorFix(w http.ResponseWriter, r *http.Request) {
	ok(w, DoctorFix(a.store, false))
}

func (a *APIServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path := P().Log(name)
	b, err := os.ReadFile(path)
	if err != nil {
		fail(w, 404, "no log: "+name)
		return
	}
	if len(b) > 1024*1024 {
		b = b[len(b)-1024*1024:]
	}
	lines := 100
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lines = n
		}
	}
	all := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	ok(w, strings.Join(all, "\n"))
}

type hostsReq struct {
	Domains []string `json:"domains"`
}

// handleUnHosts drops agent-local hosts lines for the given domains, so an
// agent can clean up domains it registered.
func (a *APIServer) handleUnHosts(w http.ResponseWriter, r *http.Request) {
	var req hostsReq
	json.NewDecoder(r.Body).Decode(&req)
	if len(req.Domains) == 0 {
		fail(w, 400, "domains required")
		return
	}
	if err := RemoveHosts(false, req.Domains); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, fmt.Sprintf("%d domains removed", len(req.Domains)))
}

func (a *APIServer) handleHosts(w http.ResponseWriter, r *http.Request) {
	var req hostsReq
	json.NewDecoder(r.Body).Decode(&req)
	n, err := EnsureHosts(false, req.Domains)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, fmt.Sprintf("%d entries added", n))
}

type importReq struct {
	Source     string `json:"source"` // LocalWP site name or docroot path
	Name       string `json:"name"`
	Domain     string `json:"domain"`
	PHPVersion string `json:"php_version"`
	Copy       bool   `json:"copy"`
	SQLDump    string `json:"sql_dump"`
	ServeOnly  bool   `json:"serve_only"`
	DBHost     string `json:"db_host"`
	DBPort     int    `json:"db_port"`
	DBUser     string `json:"db_user"`
	DBPass     string `json:"db_pass"`
	DBName     string `json:"db_name"`
}

func (a *APIServer) handleImport(w http.ResponseWriter, r *http.Request) {
	var req importReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "bad json: "+err.Error())
		return
	}
	if req.Source == "" {
		fail(w, 400, "source required")
		return
	}
	site, err := a.engine.ImportSite(ImportOpts{
		Source: req.Source, Name: req.Name, Domain: req.Domain,
		PHPVer: req.PHPVersion, Copy: req.Copy, SQLDump: req.SQLDump,
		ServeOnly: req.ServeOnly, DBHost: req.DBHost, DBPort: req.DBPort,
		DBUser: req.DBUser, DBPass: req.DBPass, DBName: req.DBName,
	})
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, site)
}

type suffixReq struct {
	Suffix string `json:"suffix"`
}

func (a *APIServer) handleSuffix(w http.ResponseWriter, r *http.Request) {
	var req suffixReq
	json.NewDecoder(r.Body).Decode(&req)
	if err := a.store.SetSuffix(req.Suffix); err != nil {
		fail(w, 400, err.Error())
		return
	}
	ok(w, map[string]string{"suffix": a.store.Suffix()})
}
