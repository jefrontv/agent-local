package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Daemon hosts the router + agent API in one long-lived process.

// APIToken reads (or creates) the shared-secret token file.
//
// Cached against the file's mtime: the MCP client reads it per call and the
// daemon's auth middleware reads it per request, so an uncached read was two
// file reads on every single tool invocation. A rotated token (new mtime) is
// picked up on the next call; a missing file regenerates as before.
func APIToken() (string, error) {
	p := P().Token()
	st, err := os.Stat(p)
	if err == nil {
		tokenMu.Lock()
		if tokenVal != "" && tokenMtime.Equal(st.ModTime()) {
			tok := tokenVal
			tokenMu.Unlock()
			return tok, nil
		}
		tokenMu.Unlock()
	}
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) >= 16 {
		tok := strings.TrimSpace(string(b))
		tokenMu.Lock()
		tokenVal, tokenMtime = tok, st.ModTime()
		tokenMu.Unlock()
		return tok, nil
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(raw)
	if err := os.WriteFile(p, []byte(tok), 0o600); err != nil {
		return "", err
	}
	tokenMu.Lock()
	tokenVal, tokenMtime = tok, fileModTime(p)
	tokenMu.Unlock()
	return tok, nil
}

// tokenVal/tokenMtime back APIToken's cache; tokenMu guards them because the
// daemon reads the token from every request goroutine.
var (
	tokenMu    sync.Mutex
	tokenVal   string
	tokenMtime time.Time
)

// restoreRunning starts the pools for every site and worktree whose persisted
// state says it was running. Returns how many came back.
func restoreRunning(e *Engine, store *Store) int {
	type target struct {
		id, dir, php string
	}
	var want []target
	alive := e.AliveAll()
	for _, site := range store.Sites() {
		if site.State == StateRunning && !alive[site.Slug] {
			want = append(want, target{site.Slug, site.WPDir, site.PHPVersion})
		}
	}
	for _, w := range store.Data.Worktrees {
		site := store.Site(w.Site)
		if site == nil || w.State != StateRunning || alive[w.ID] {
			continue
		}
		want = append(want, target{w.ID, e.wtServeDir(w), site.PHPVersion})
	}
	const restoreConcurrency = 4
	sem := make(chan struct{}, restoreConcurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	started := 0
	for _, t := range want {
		wg.Add(1)
		sem <- struct{}{}
		go func(t target) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := e.StartFPM(t.id, t.dir, t.php); err != nil {
				log.Printf("fpm %s: %v", t.id, err)
				return
			}
			mu.Lock()
			started++
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	return started
}

// unreadableDocroots lists sites whose docroot exists on disk but this process
// is refused permission to list — the signature of a macOS folder-access
// denial, as opposed to a docroot that is simply gone. A missing docroot is
// the user's business (they moved it); a denied one is ours.
//
// The probes run together and are given a deadline. A read that neither
// succeeds nor fails is macOS showing the user a consent dialog for that
// folder — which it does afresh for every new build, since an ad-hoc signature
// is a new identity each time. Waiting on it kept a daemon silent, portless and
// "running" for as long as the dialog went unanswered (15 minutes once), with
// every site dark. So: report what is still pending and let the boot go on.
// PHP has its own grant and serves; static files under that folder 403 until
// the user allows it, at which point they simply start working.
func unreadableDocroots(store *Store) (denied, pending []string) {
	type result struct {
		slug string
		err  error
	}
	sites := store.Sites()
	results := make(chan result, len(sites))
	for _, s := range sites {
		go func(s *Site) {
			_, err := os.ReadDir(s.WPDir)
			results <- result{s.Slug, err}
		}(s)
	}
	deadline := time.After(docrootProbeDeadline)
	answered := map[string]bool{}
	for range sites {
		select {
		case r := <-results:
			answered[r.slug] = true
			if r.err != nil && errors.Is(r.err, fs.ErrPermission) {
				denied = append(denied, r.slug)
			}
		case <-deadline:
			for _, s := range sites {
				if !answered[s.Slug] {
					pending = append(pending, s.Slug)
				}
			}
			return denied, pending
		}
	}
	return denied, nil
}

// docrootProbeDeadline is how long a docroot listing may take before it is
// taken to be waiting on a consent dialog rather than a disk. Local
// directories answer in microseconds; nothing legitimate needs seconds.
const docrootProbeDeadline = 3 * time.Second

// RunDaemon is the `daemon` entrypoint.
func RunDaemon(background bool) error {
	// Armed before anything else so a signal arriving during the standby wait,
	// store load, or pool restoration below is still caught for a clean exit
	// instead of falling through to the OS default (an untidy, log-less kill).
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	// hand is how the binary watcher asks this goroutine to step down: the
	// file on disk is a newer build, and launchd should be running that one.
	hand := make(chan string, 1)
	// A daemon already serving owns the ports. Wait rather than exit: exiting
	// meant launchd (KeepAlive on failure only) never brought the login job back
	// when that other instance later died, so a machine could end up with nothing
	// serving and no way to notice. Standing by costs nothing and guarantees one
	// instance is always ready to take the role.
	if !background && portOpen(DefaultAPIPort) {
		log.Printf("another agent-local daemon holds :%d — standing by to take over", DefaultAPIPort)
		// A standby has nothing to drain: a new binary means step aside now so
		// the instance that eventually takes over is the current build. Its own
		// channel, so a change seen while standing by can never skip the
		// job-aware handover a serving daemon gets below.
		standbyHand := make(chan string, 1)
		go watchBinary(watchedBinaryPath(), binaryWatchEvery, func(v string) { standbyHand <- v })
		for portOpen(DefaultAPIPort) {
			select {
			case <-sig:
				log.Printf("shutting down while standing by")
				return nil
			case v := <-standbyHand:
				log.Printf("binary on disk is now %s (running %s) — standing by ends here", v, Version)
				if os.Getenv(launchdMarker) != "" {
					relaunchViaLaunchd()
				}
				return nil
			case <-time.After(2 * time.Second):
			}
		}
		log.Printf("the other daemon is gone — taking over")
	}
	// Whoever starts the daemon also gets it started at login: after a reboot the
	// stack used to stay down until someone happened to run a command.
	if err := EnsureDaemonAutostart(); err != nil {
		log.Printf("autostart: %v", err)
	}
	store, err := OpenStore()
	if err != nil {
		return err
	}
	EnsureInventory(store)
	store.Save()

	e := NewEngine(store)

	// A daemon that cannot read its own sites must not take the ports. macOS
	// grants folder access (Documents, Desktop, …) per launching app, and a
	// daemon forked from an app without it serves PHP (php-fpm's own grant)
	// while every static file 403s and pools it starts cannot load plugins —
	// a half-working stack that looks like a bug in each site. Bail here so
	// the launchd job, which runs with the user's grants, takes over instead.
	// A grant still being asked for is different: boot, and say what is waiting.
	denied, pending := unreadableDocroots(store)
	if len(denied) > 0 {
		return fmt.Errorf("this process cannot read %d site docroot(s) (%s): macOS denies it folder access; run `agent-local restart-daemon` from Terminal, or start via launchd",
			len(denied), strings.Join(denied, ", "))
	}
	if len(pending) > 0 {
		log.Printf("macOS is asking for permission to read the folder holding %d site(s) (%s) — allow it in the dialog; until then their static files 403 while PHP serves",
			len(pending), strings.Join(pending, ", "))
	}

	// Residue from deletes that predate pool cleanup: sweep before starting pools,
	// so php-fpm never parses a config naming a directory that is gone. This used
	// to run after the starts, contradicting its own comment.
	if n := e.SweepOrphanPools(); n > 0 {
		log.Printf("swept %d orphaned php-fpm pool config(s)", n)
	}

	// Tunnels a previous daemon owned cannot be re-adopted — reap them and
	// the mu-plugins they left, so no site keeps mapping a dead share host.
	SweepShares(store)

	// Bring back whatever was running before the machine went down — sites and
	// their branch previews. Concurrently: each pool boot is about a second, and
	// eight of them in series is a boot nobody waits for.
	restored := restoreRunning(e, store)
	if restored > 0 {
		log.Printf("restored %d pool(s) that were running before shutdown", restored)
	}

	// HTTP front: router serves in-process; apache is a child process. The API
	// below runs under both fronts so agents never lose control.
	router := NewRouter(e)
	if err := applyFront(store); err != nil {
		log.Printf("front: %v", err)
	}
	// The router binds its listeners and serves them on background goroutines,
	// reporting only bind failures: a nil return means "up", not "done". So it
	// runs inline — waiting on its return as an exit signal would shut the
	// daemon down the moment boot succeeded.
	if FrontKind(store) == "router" {
		if err := router.ListenAndServe(DefaultHTTPPort, DefaultHTTPSPort); err != nil {
			return fmt.Errorf("router: %w", err)
		}
	}
	// The bare-URL front is a separate root process; keep an eye on it.
	go watchFront()
	// record daemon pid so `front` switching can restart us
	if pidf := filepath.Join(P().Run(), "daemon.pid"); pidf != "" {
		os.WriteFile(pidf, []byte(fmt.Sprint(os.Getpid())), 0o644)
	}

	// Agent API
	jobs := NewJobHub()
	api := &APIServer{store: store, engine: e, jobs: jobs}
	mux := api.routes()
	srv := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", DefaultAPIPort),
		Handler:           api.auth(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	// Bind synchronously and fail the boot if it does not take. Binding
	// inside the serve goroutine let a daemon whose API port was still held
	// by its predecessor log one line and then sit there for its whole life
	// with no API: launchd saw a healthy process, agents saw connection
	// refused, and nothing ever restarted it. A failed boot is what launchd
	// knows how to handle.
	apiLn, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("api: %w", err)
	}
	go func() {
		if err := srv.Serve(apiLn); err != nil && err != http.ErrServerClosed {
			log.Printf("api: %v", err)
		}
	}()
	// A bound socket is not a serving one. Dial our own API before claiming
	// to be up; a listener the kernel accepted but nobody can reach means
	// this process is useless and should exit so launchd tries again.
	if err := waitPort(DefaultAPIPort, 5*time.Second); err != nil {
		return fmt.Errorf("api bound but unreachable on :%d — exiting for launchd to retry", DefaultAPIPort)
	}

	log.Printf("agent-local daemon: http :%d https :%d api :%d", DefaultHTTPPort, DefaultHTTPSPort, DefaultAPIPort)

	// From here the binary on disk is watched: a new build installed by any
	// route - update, brew upgrade, install.sh - takes over without anyone
	// running restart-daemon. And once a day, GitHub is asked whether there
	// is one to install.
	go watchBinary(watchedBinaryPath(), binaryWatchEvery, daemonHandover(jobs, hand))
	go updateLoop(store)

	handingOver := ""
	select {
	case <-sig:
		log.Printf("shutting down")
	case handingOver = <-hand:
		log.Printf("handing over to %s", handingOver)
	}
	// Give any in-flight import/deploy job a moment to finish rather than
	// being cut off mid-write.
	jobs.DrainRunning(5 * time.Second)
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("api shutdown: %v", err)
	}
	for _, sh := range shares.All() {
		sh.shutdown()
	}
	if handingOver != "" {
		relaunchViaLaunchd()
	}
	return nil
}

// APIServer is the agent-facing HTTP control surface.
type APIServer struct {
	store  *Store
	engine *Engine
	jobs   *JobHub
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
		// /mail-ui and /hub-ui are the apache front's ProxyPass targets for the
		// inbox UI and the tooling index — the same pages the router serves
		// unauthenticated on a site's own domain, so gating them here would
		// only break one front of the two. Both listeners are loopback-only.
		// Neither page carries secrets: the inbox needs its id, the index is
		// only links.
		if r.URL.Path == "/healthz" || strings.HasPrefix(r.URL.Path, "/mail-ui/") || strings.HasPrefix(r.URL.Path, "/hub-ui/") {
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
	mux.HandleFunc("POST /attach", a.handleAttach)
	mux.HandleFunc("POST /sites/{slug}/media", a.handleMedia)
	mux.HandleFunc("GET /sites/{slug}/media", a.handleMedia)
	mux.HandleFunc("GET /sites-dir", a.handleSitesDir)
	mux.HandleFunc("POST /sites-dir", a.handleSitesDir)
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
	mux.HandleFunc("POST /sites/{slug}/db/snapshot", a.handleDBSnapshot)
	mux.HandleFunc("GET /sites/{slug}/db/snapshots", a.handleDBSnapshots)
	mux.HandleFunc("POST /sites/{slug}/db/restore", a.handleDBRestore)
	mux.HandleFunc("GET /sites/{slug}/wp-debug", a.handleWPDebug)
	mux.HandleFunc("POST /sites/{slug}/wp-debug", a.handleWPDebug)
	mux.HandleFunc("GET /sites/{slug}/mail", a.handleMailList)
	mux.HandleFunc("GET /sites/{slug}/mail/{msg}", a.handleMailGet)
	mux.HandleFunc("DELETE /sites/{slug}/mail", a.handleMailClear)
	mux.HandleFunc("/mail-ui/{id}", a.handleMailUI)
	mux.HandleFunc("/mail-ui/{id}/{rest...}", a.handleMailUI)
	mux.HandleFunc("/hub-ui/{id}", a.handleHubUI)
	mux.HandleFunc("POST /sites/{slug}/share", a.handleShareStart)
	mux.HandleFunc("GET /sites/{slug}/share", a.handleShareGet)
	mux.HandleFunc("DELETE /sites/{slug}/share", a.handleShareStop)
	mux.HandleFunc("GET /resolve", a.handleResolvePath)
	mux.HandleFunc("GET /certs/{domain}", a.handleCertStatus)
	mux.HandleFunc("POST /certs/{domain}/trust", a.handleCertTrust)
	mux.HandleFunc("POST /yield", a.handleYield)
	mux.HandleFunc("GET /jobs", a.handleListJobs)
	mux.HandleFunc("GET /jobs/{id}", a.handleGetJob)
	mux.HandleFunc("GET /sites/{slug}/adminer", a.handleAdminer)
	// Working on a site: diagnose, fix, undo.
	mux.HandleFunc("POST /sites/{slug}/probe", a.handleProbe)
	mux.HandleFunc("POST /sites/{slug}/request", a.handleRequest)
	mux.HandleFunc("GET /sites/{slug}/wp-info", a.handleWPInfo)
	mux.HandleFunc("GET /sites/{slug}/errors", a.handleErrors)
	mux.HandleFunc("POST /sites/{slug}/checkpoints", a.handleCheckpoint)
	mux.HandleFunc("GET /sites/{slug}/checkpoints", a.handleListCheckpoints)
	mux.HandleFunc("POST /sites/{slug}/checkpoints/{name}/rollback", a.handleRollback)
	mux.HandleFunc("DELETE /sites/{slug}/checkpoints/{name}", a.handleDeleteCheckpoint)
	mux.HandleFunc("POST /sites/{slug}/db/search", a.handleDBSearch)
	mux.HandleFunc("POST /sites/{slug}/db/search-replace", a.handleSearchReplace)
	mux.HandleFunc("POST /sites/{slug}/login", a.handleMagicLogin)
	mux.HandleFunc("GET /sites/{slug}/wp-config/constants", a.handleWPConstants)
	mux.HandleFunc("POST /sites/{slug}/wp-config/constant", a.handleSetWPConstant)
	return mux
}

// handleWPDebug reads or flips a site's WP_DEBUG. On points WP_DEBUG_LOG at
// ~/.agent-local/logs/wp-<slug>.log, which get_logs can then tail by the
// returned log_name.
func (a *APIServer) handleWPDebug(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	if r.Method == "GET" {
		ok(w, WPDebugStatus(site))
		return
	}
	var req struct {
		On bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "body must be {\"on\": true|false}")
		return
	}
	st, err := a.engine.SetWPDebug(site.Slug, req.On)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, st)
}

// handleMailList, handleMailGet and handleMailClear are the agent-facing
// inbox: submit a form with a browser, then read the email it produced —
// a complete end-to-end check with no human mailbox involved.
func (a *APIServer) handleMailList(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	sums, err := ListMail(site.Slug)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	for i := range sums {
		sums[i].URL = MailURL(site.Domain) + "/msg/" + sums[i].ID
	}
	ok(w, sums)
}

func (a *APIServer) handleMailGet(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	msg, err := GetMail(site.Slug, r.PathValue("msg"))
	if err != nil {
		fail(w, 404, err.Error())
		return
	}
	mailLinks(site.Domain, msg)
	ok(w, msg)
}

func (a *APIServer) handleMailClear(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	n, err := ClearMail(site.Slug)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, map[string]int{"cleared": n})
}

// handleMailUI renders the browser inbox for the apache front, whose vhosts
// ProxyPass /.agent-local/mail here. Pages link relative to the
// browser-facing path, which is why base is MailPath and not this route —
// hitting /mail-ui directly is not a supported way in.
func (a *APIServer) handleMailUI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// The id is a pool — a site slug or a worktree id — and nothing else: it
	// becomes a directory under ~/.agent-local/mail, and this route is the one
	// the token does not guard (Apache proxies to it), so a stray "../" here
	// would read or clear files outside the mail tree.
	if a.store.Site(id) == nil && a.store.Data.Worktrees[id] == nil {
		http.NotFound(w, r)
		return
	}
	rest := r.PathValue("rest")
	if rest != "" {
		rest = "/" + rest
	}
	serveMailUI(w, r, id, MailPath, rest, id)
}

// handleHubUI renders the tooling index for the apache front, whose vhosts
// ProxyPass the exact /.agent-local path here. Base stays HubPath for the
// same reason as the inbox: links must work on the browser-facing path.
// The id is validated like the inbox's — this route skips the token — but
// unlike mail it never touches disk, it only picks the title.
func (a *APIServer) handleHubUI(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	title := id
	if site := a.store.Site(id); site != nil {
		title = site.Domain
	} else if wt := a.store.Data.Worktrees[id]; wt != nil {
		title = wt.Domain
	} else {
		http.NotFound(w, r)
		return
	}
	serveHubUI(w, HubPath, title)
}

type shareReq struct {
	Minutes int `json:"minutes"`
}

// handleShareStart opens (or reports — it is idempotent) a site's public
// tunnel. Wrapped in a job because the first run may brew-install cloudflared.
func (a *APIServer) handleShareStart(w http.ResponseWriter, r *http.Request) {
	if a.requireSite(w, r) == nil {
		return
	}
	var req shareReq
	json.NewDecoder(r.Body).Decode(&req)
	slug := r.PathValue("slug")
	a.runJob(w, r, "share", func(cb func(string, string)) (any, error) {
		return a.engine.StartShare(slug, req.Minutes, cb)
	})
}

func (a *APIServer) handleShareGet(w http.ResponseWriter, r *http.Request) {
	if a.requireSite(w, r) == nil {
		return
	}
	if sh := shares.ForSlug(r.PathValue("slug")); sh != nil {
		ok(w, sh)
		return
	}
	ok(w, map[string]bool{"active": false})
}

func (a *APIServer) handleShareStop(w http.ResponseWriter, r *http.Request) {
	if a.requireSite(w, r) == nil {
		return
	}
	if a.engine.StopShare(r.PathValue("slug")) {
		ok(w, "share stopped")
		return
	}
	ok(w, "not shared")
}

func wantAsync(r *http.Request) bool {
	if r.URL.Query().Get("async") == "1" || strings.EqualFold(r.URL.Query().Get("async"), "true") {
		return true
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Prefer")), "respond-async")
}

func (a *APIServer) runJob(w http.ResponseWriter, r *http.Request, op string, fn func(cb func(string, string)) (any, error)) {
	if a.jobs == nil {
		a.jobs = NewJobHub()
	}
	job := a.jobs.Start(op, fn)
	w.Header().Set("X-Job-Id", job.ID)
	if wantAsync(r) {
		writeJSON(w, 202, apiResp{OK: true, Data: job.Snapshot()})
		return
	}
	job.Wait()
	snap := job.Snapshot()
	if snap.Status == JobErr {
		fail(w, 500, snap.Error)
		return
	}
	ok(w, snap.Result)
}

func (a *APIServer) handleListJobs(w http.ResponseWriter, r *http.Request) {
	if a.jobs == nil {
		ok(w, []JobView{})
		return
	}
	ok(w, a.jobs.List())
}

func (a *APIServer) handleGetJob(w http.ResponseWriter, r *http.Request) {
	if a.jobs == nil {
		fail(w, 404, "no such job")
		return
	}
	j := a.jobs.Get(r.PathValue("id"))
	if j == nil {
		fail(w, 404, "no such job")
		return
	}
	ok(w, j.Snapshot())
}

func (a *APIServer) handleAdminer(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	if _, err := writeAdminerBoot(site); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, map[string]string{"url": AdminerURL(site.Domain), "path": AdminerPath})
}

type createReq struct {
	Dir        string `json:"dir"`
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
	// version is the build in memory; installed is the binary on disk, which
	// differs for the seconds between an update and the handover - or for as
	// long as a job the handover is waiting on keeps running.
	installed := Version
	if v := installedVersion.Load(); v != nil {
		installed = v.(string)
	}
	latest, _ := availableUpdate()
	a.store.ReloadIfChanged()
	st := map[string]interface{}{
		"version":   Version,
		"installed": installed,
		"update": map[string]interface{}{
			"latest":    latest,
			"available": latest != "",
			"auto":      a.store.Data.AutoUpdate,
		},
		"db":        map[string]interface{}{"running": e.DBRunning(), "port": DefaultDBPort},
		"http":      map[string]interface{}{"port": DefaultHTTPPort, "listening": portOpen(DefaultHTTPPort), "front": FrontKind(a.store)},
		"runtimes":  a.store.Inventory().Runtimes(),
		"sites":     len(a.store.Sites()),
		"worktrees": len(a.store.Data.Worktrees),
	}
	ok(w, st)
}

// handleMedia reads or sets a site's media fallback: where missing uploads go.
func (a *APIServer) handleMedia(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	site := a.store.Site(slug)
	if site == nil {
		fail(w, 404, "no such site")
		return
	}
	if r.Method == http.MethodPost {
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, 400, "bad json: "+err.Error())
			return
		}
		if _, err := a.engine.SetMediaFallback(slug, req.URL); err != nil {
			fail(w, 400, err.Error())
			return
		}
		site = a.store.Site(slug)
	}
	ok(w, map[string]interface{}{
		"slug": slug, "media_fallback": EffectiveMediaFallback(site),
		"pinned": site.MediaFallback, "off": site.MediaOff,
		"htaccess_implies": a.engine.MediaFallbackHint(slug),
	})
}

// handleSitesDir reads or sets the parent directory new sites are created in.
func (a *APIServer) handleSitesDir(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			Dir string `json:"dir"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, 400, "bad json: "+err.Error())
			return
		}
		if err := a.store.SetSitesDir(req.Dir); err != nil {
			fail(w, 400, err.Error())
			return
		}
	}
	ok(w, map[string]interface{}{"dir": a.store.SitesDir(), "default": P().Sites()})
}

// handleAttach serves a directory the caller already has, with an empty database
// and their files untouched. The counterpart to /import, which copies a database.
func (a *APIServer) handleAttach(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir        string `json:"dir"`
		Name       string `json:"name"`
		Domain     string `json:"domain"`
		PHPVersion string `json:"php_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "bad json: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Dir) == "" {
		fail(w, 400, "dir required")
		return
	}
	site, err := a.engine.AttachSite(AttachOpts{
		Dir: req.Dir, Name: req.Name, Domain: req.Domain, PHPVer: req.PHPVersion,
	})
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, site)
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
	a.runJob(w, r, "create", func(cb func(string, string)) (any, error) {
		return a.engine.CreateSite(CreateOpts{
			Name: req.Name, Dir: req.Dir, Domain: req.Domain, PHPVersion: req.PHPVersion,
			WPVersion: req.WPVersion, Repo: req.Repo,
			AdminUser: req.AdminUser, AdminPass: req.AdminPass, AdminEmail: req.AdminEmail,
			Title: req.Title, Progress: cb,
		})
	})
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
	site, matched, wt, candidates := a.engine.ResolvePath(path)
	if site == nil {
		if len(candidates) > 1 {
			slugs := make([]string, 0, len(candidates))
			for _, s := range candidates {
				slugs = append(slugs, s.Slug)
			}
			writeJSON(w, 409, apiResp{Error: fmt.Sprintf("%s contains %d sites: %s — pass one of their paths",
				path, len(candidates), strings.Join(slugs, ", "))})
			return
		}
		fail(w, 404, "no site manages "+path)
		return
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
// a background daemon has nowhere to show a password prompt, so this path
// works only through the scoped allowlist `agent-local sudo` installs. When
// that is missing the error says so and names the CLI command, which a GUI
// integrator can run as a child of its own process to get the dialog.
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
		fail(w, 500, err.Error())
		return
	}
	ok(w, InspectCert(domain))
}

// handleStart boots the site and returns everything a caller needs to talk to
// it: URL, docroot, PHP version and live DB connection details. One call, so a
// provider integration never has to guess or follow up.
func (a *APIServer) handleStart(w http.ResponseWriter, r *http.Request) {
	if a.requireSite(w, r) == nil {
		return
	}
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

// requireSite resolves the {slug} path value or answers 404. "You asked for a
// site that does not exist" is a fact about the request, not a daemon fault:
// a 500 there made integrators retry and report agent-local as broken.
func (a *APIServer) requireSite(w http.ResponseWriter, r *http.Request) *Site {
	site := a.store.Site(r.PathValue("slug"))
	if site == nil {
		fail(w, 404, "no such site: "+r.PathValue("slug"))
		return nil
	}
	return site
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
	if a.requireSite(w, r) == nil {
		return
	}
	if err := a.engine.StopSite(r.PathValue("slug")); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "stopped")
}

func (a *APIServer) handleRestart(w http.ResponseWriter, r *http.Request) {
	if a.requireSite(w, r) == nil {
		return
	}
	slug := r.PathValue("slug")
	if err := a.engine.StopSite(slug); err != nil {
		fail(w, 500, fmt.Sprintf("stop: %v", err))
		return
	}
	if err := a.engine.StartSite(slug); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "restarted")
}

// handleDelete removes a site. `?files=keep` leaves the checkout, `?db=keep`
// leaves the schema and user so the folder can be re-adopted later.
func (a *APIServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	if a.requireSite(w, r) == nil {
		return
	}
	q := r.URL.Query()
	if err := a.engine.DeleteSite(r.PathValue("slug"), DeleteOpts{
		KeepFiles:  q.Get("files") == "keep",
		KeepDB:     q.Get("db") == "keep",
		NoSnapshot: q.Get("snapshot") == "off",
	}); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "deleted")
}

type phpReq struct {
	Version string `json:"version"`
	// Install lets the switch fetch or repair the runtime it needs. Default is
	// on: a caller asking for 7.4 wants the site on 7.4, and refusing because
	// the keg is absent left it with a failed call and no next step.
	Install *bool `json:"install"`
	// Tap allows the third-party tap that carries PHP releases homebrew-core has
	// deleted. Off by default — trusting a tap runs its code on this machine.
	Tap bool `json:"tap"`
}

func (a *APIServer) handleSwitchPHP(w http.ResponseWriter, r *http.Request) {
	if a.requireSite(w, r) == nil {
		return
	}
	var req phpReq
	json.NewDecoder(r.Body).Decode(&req)
	version := NormalizePHPVersion(req.Version)
	if version == "" {
		fail(w, 400, "version required")
		return
	}
	slug := r.PathValue("slug")
	install := req.Install == nil || *req.Install
	// Already installed: a plain switch, answered on this request.
	if a.store.Inventory().FindPHP(version) != nil || !install {
		if err := a.engine.SwitchPHPEnsure(slug, version, false, false, nil); err != nil {
			fail(w, 500, err.Error())
			return
		}
		ok(w, "php "+version)
		return
	}
	// A brew install or repair runs for minutes, so it goes through a job: the
	// caller can wait on this request or pass async=1 and poll get_job.
	a.runJob(w, r, "switch_php", func(cb func(stage, detail string)) (any, error) {
		err := a.engine.SwitchPHPEnsure(slug, version, true, req.Tap, func(line string) { cb("brew", line) })
		if err != nil {
			return nil, err
		}
		return map[string]string{"slug": slug, "php_version": version}, nil
	})
}

type domainReq struct {
	Domain string `json:"domain"`
}

func (a *APIServer) handleDomain(w http.ResponseWriter, r *http.Request) {
	if a.requireSite(w, r) == nil {
		return
	}
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
	Path       string `json:"path"`
	KeepURLs   bool   `json:"keep_urls"`   // skip domain search-replace
	NoSnapshot bool   `json:"no_snapshot"` // skip the automatic pre-import snapshot
}

// handleDBImport replaces a site's database contents with a .sql/.sql.gz dump.
func (a *APIServer) handleDBImport(w http.ResponseWriter, r *http.Request) {
	if a.requireSite(w, r) == nil {
		return
	}
	var req sqlFileReq
	json.NewDecoder(r.Body).Decode(&req)
	if req.Path == "" {
		fail(w, 400, "path required")
		return
	}
	slug := r.PathValue("slug")
	a.runJob(w, r, "db-import", func(cb func(string, string)) (any, error) {
		cb("database", "loading "+req.Path)
		return a.engine.ImportSQL(slug, req.Path, !req.KeepURLs, !req.NoSnapshot)
	})
}
func (a *APIServer) handleDBExport(w http.ResponseWriter, r *http.Request) {
	if a.requireSite(w, r) == nil {
		return
	}
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
	if a.requireSite(w, r) == nil {
		return
	}
	var req snapshotReq
	json.NewDecoder(r.Body).Decode(&req)
	msg, err := a.engine.ResetDBBackup(r.PathValue("slug"), !req.NoSnapshot)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, msg)
}

type snapshotReq struct {
	Name       string `json:"name"`
	NoSnapshot bool   `json:"no_snapshot"`
}

// handleDBSnapshot saves a database save-point.
func (a *APIServer) handleDBSnapshot(w http.ResponseWriter, r *http.Request) {
	if a.requireSite(w, r) == nil {
		return
	}
	var req snapshotReq
	json.NewDecoder(r.Body).Decode(&req)
	slug := r.PathValue("slug")
	a.runJob(w, r, "db-snapshot", func(cb func(string, string)) (any, error) {
		cb("database", "snapshotting "+slug)
		return a.engine.SnapshotDB(slug, req.Name)
	})
}

func (a *APIServer) handleDBSnapshots(w http.ResponseWriter, r *http.Request) {
	if a.requireSite(w, r) == nil {
		return
	}
	snaps, err := a.engine.Snapshots(r.PathValue("slug"))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, snaps)
}

// handleDBRestore loads a save-point back, the newest when no name is given.
func (a *APIServer) handleDBRestore(w http.ResponseWriter, r *http.Request) {
	if a.requireSite(w, r) == nil {
		return
	}
	var req snapshotReq
	json.NewDecoder(r.Body).Decode(&req)
	slug := r.PathValue("slug")
	a.runJob(w, r, "db-restore", func(cb func(string, string)) (any, error) {
		cb("database", "restoring "+slug)
		return a.engine.RestoreSnapshot(slug, req.Name, !req.NoSnapshot)
	})
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
	if a.requireSite(w, r) == nil {
		return
	}
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
	wts := a.store.WorktreesFor(r.PathValue("slug"))
	ids := make([]string, len(wts))
	for i, wt := range wts {
		ids[i] = wt.ID
	}
	alive := a.engine.fpmAliveBatch(ids)
	for _, wt := range wts {
		state := "stopped"
		if alive[wt.ID] {
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
	// Tap allows the third-party tap for PHP releases core has dropped.
	Tap bool `json:"tap"`
}

func (a *APIServer) handleInstall(w http.ResponseWriter, r *http.Request) {
	var req installReq
	json.NewDecoder(r.Body).Decode(&req)
	switch req.What {
	case "php", "mariadb", "mysql", "apache", "httpd", "brew", "homebrew":
	default:
		fail(w, 400, "what: php|mariadb|apache|brew")
		return
	}
	// brew builds take minutes. This used to hold the HTTP request open for the
	// whole install, so a client with any timeout at all gave up mid-build and
	// had no way to find out how it ended.
	a.runJob(w, r, "install:"+req.What, func(cb func(stage, detail string)) (any, error) {
		line := func(s string) {
			log.Printf("install: %s", s)
			cb("brew", s)
		}
		var err error
		switch req.What {
		case "php":
			version := NormalizePHPVersion(req.Version)
			if version == "" {
				version = latestBrewPHP()
			}
			err = InstallPHP(a.store, version, req.Tap, line)
			req.Version = version
		case "mariadb", "mysql":
			err = InstallMySQL(a.store, line)
		case "apache", "httpd":
			err = InstallApache(a.store, line)
		case "brew", "homebrew":
			err = InstallBrew(line)
		}
		if err != nil {
			return nil, err
		}
		DiscoverInventory(a.store)
		if err := a.store.Save(); err != nil {
			return nil, err
		}
		return map[string]any{"installed": req.What, "version": req.Version,
			"php": a.store.Inventory().Runtimes()}, nil
	})
}

func (a *APIServer) handleDoctor(w http.ResponseWriter, r *http.Request) {
	ok(w, Doctor(a.store))
}

func (a *APIServer) handleDoctorFix(w http.ResponseWriter, r *http.Request) {
	ok(w, DoctorFix(a.store, false))
}

// logName is what `logs NAME` accepts: a bare file stem such as apache,
// fpm-mysite or wp-mysite. Anything with a separator is not a log name.
var logName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (a *APIServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !logName.MatchString(name) || strings.Contains(name, "..") {
		fail(w, 400, "bad log name: "+name)
		return
	}
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
	Source     string `json:"source"` // LocalWP site name, DDEV project name, or docroot path
	Name       string `json:"name"`
	Domain     string `json:"domain"`
	PHPVersion string `json:"php_version"`
	Copy       bool   `json:"copy"`
	SQLDump    string `json:"sql_dump"`
	ServeOnly  bool   `json:"serve_only"`
	KeepDDEV   bool   `json:"keep_ddev"`
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
	a.runJob(w, r, "import", func(cb func(string, string)) (any, error) {
		return a.engine.ImportSite(ImportOpts{
			Source: req.Source, Name: req.Name, Domain: req.Domain,
			PHPVer: req.PHPVersion, Copy: req.Copy, SQLDump: req.SQLDump,
			ServeOnly: req.ServeOnly, KeepDDEV: req.KeepDDEV,
			DBHost: req.DBHost, DBPort: req.DBPort,
			DBUser: req.DBUser, DBPass: req.DBPass, DBName: req.DBName,
			Progress: cb,
		})
	})
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
