package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// runMCP implements a minimal Model Context Protocol server over stdio:
// JSON-RPC 2.0 with initialize, tools/list, tools/call. All mutations go
// through the daemon HTTP API so a single process owns state.

type mcpReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResp struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *mcpErr     `json:"error,omitempty"`
}

type mcpErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

func runMCP(args []string) error {
	// A person asking for the config wants to paste it somewhere, not speak
	// JSON-RPC at a prompt.
	if hasFlag(args, "--config") {
		fmt.Print(mcpClientConfig())
		return nil
	}
	// This is a stdio server: it reads JSON-RPC from stdin and writes to stdout.
	// Run by hand it therefore sits in silence, which reads exactly like a hang —
	// so say what it is waiting for. stderr, never stdout: stdout is the protocol.
	if isTerminal(os.Stdin) {
		fmt.Fprint(os.Stderr, mcpTTYNotice())
	}

	// ensure daemon is up (best-effort; some calls work offline)
	EnsureRouterDaemonQuiet()

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	// Every request runs on its own goroutine. The loop used to handle them
	// in order, so one long synchronous call (an import without async=true)
	// held the whole connection: even status or get_job — the calls meant to
	// watch that import — got no answer until it finished. JSON-RPC matches
	// replies by id, so out-of-order is fine; one encoder behind a mutex keeps
	// each reply a whole line.
	var (
		out   sync.Mutex
		enc   = json.NewEncoder(os.Stdout)
		inflt sync.WaitGroup
	)
	reply := func(resp *mcpResp) {
		out.Lock()
		defer out.Unlock()
		enc.Encode(resp)
	}
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req mcpReq
		if err := json.Unmarshal(line, &req); err != nil {
			reply(&mcpResp{JSONRPC: "2.0", Error: &mcpErr{Code: -32700, Message: "parse error: " + err.Error()}})
			continue
		}
		// A request without an id is a notification: the spec forbids answering
		// it, and a client that gets an answer anyway can wedge waiting to match
		// it against a request it never made.
		if req.ID == nil {
			continue
		}
		inflt.Add(1)
		go func(req mcpReq) {
			defer inflt.Done()
			resp := mcpHandle(&req)
			if resp == nil {
				return
			}
			// Set centrally so no handler can forget it. Responses used to go out
			// as `"jsonrpc":""`, which a strict client rejects — the server looked
			// like it was doing nothing at all.
			resp.JSONRPC = "2.0"
			resp.ID = req.ID
			reply(resp)
		}(req)
	}
	// stdin closed: the client is going away. Let in-flight calls finish so a
	// mutation that already reached the daemon still gets its result written.
	inflt.Wait()
	return sc.Err()
}

// isTerminal reports whether a file is a terminal rather than a pipe. Character
// devices are terminals; a pipe or a redirected file is not.
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// mcpTTYNotice explains the silence to whoever typed the command.
func mcpTTYNotice() string {
	return "agent-local mcp — Model Context Protocol server (stdio)\n\n" +
		"  Nothing will happen here: this speaks JSON-RPC over stdin/stdout and is\n" +
		"  meant to be launched by an MCP client, not run by hand.\n\n" +
		"  Client config:      agent-local mcp --config\n" +
		"  Register yourself:  agent-local connect\n" +
		"  Try one call:       echo '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}' | agent-local mcp\n" +
		"  Everything here is also a CLI command:  agent-local help\n\n" +
		"  Listening on stdin — ctrl-c to quit.\n"
}

// mcpClientConfig is the block to paste into a client's config file.
// `agent-local connect` does this automatically for the harnesses it knows —
// this is the manual fallback for anything else.
func mcpClientConfig() string {
	bin := agentLocalBinaryPath()
	entry := mcpServerEntry(bin)
	out, _ := json.MarshalIndent(map[string]interface{}{
		"mcpServers": map[string]interface{}{"agent-local": entry},
	}, "", "  ")
	return string(out) + "\n"
}

func mcpHandle(req *mcpReq) *mcpResp {
	switch req.Method {
	case "initialize":
		return &mcpResp{Result: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
			"serverInfo":      map[string]interface{}{"name": AppName, "version": Version},
		}}
	case "notifications/initialized", "initialized":
		return nil // notification, no response
	case "tools/list":
		return &mcpResp{Result: map[string]interface{}{"tools": tools()}}
	case "tools/call":
		return mcpCall(req.Params)
	case "ping":
		return &mcpResp{Result: map[string]interface{}{}}
	default:
		return &mcpResp{Error: &mcpErr{Code: -32601, Message: "method not found: " + req.Method}}
	}
}

// schema builds a tool's input schema. properties is always an object and
// required is omitted when empty: a nil map marshals to `null`, and a client that
// validates its side of the protocol rejects the whole tools/list over it — the
// server showed as connected with "tools fetch failed" and no capabilities.
func schema(props map[string]interface{}, required ...string) map[string]interface{} {
	if props == nil {
		props = map[string]interface{}{}
	}
	out := map[string]interface{}{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func prop(t, desc string) map[string]interface{} {
	return map[string]interface{}{"type": t, "description": desc}
}

// toolTable is the catalogue built once. mcpTools constructs ~60 descriptors
// with nested schema maps; rebuilding that on every tools/list and every
// tools/call — the old lookup was a linear scan over a fresh build — was
// pure allocation on the hot path of every agent request.
var (
	toolTableOnce sync.Once
	toolTable     []mcpTool
	toolIndex     map[string]*mcpTool
)

func tools() []mcpTool {
	toolTableOnce.Do(func() {
		toolTable = mcpTools()
		toolIndex = make(map[string]*mcpTool, len(toolTable))
		for i := range toolTable {
			toolIndex[toolTable[i].Name] = &toolTable[i]
		}
	})
	return toolTable
}

// toolByName is the tools/call lookup: one map hit, nil for an unknown name.
func toolByName(name string) *mcpTool {
	tools()
	return toolIndex[name]
}

// mcpTools builds the catalogue. Read it through tools(), which caches it.
func mcpTools() []mcpTool {
	return []mcpTool{
		{"status", "Daemon + stack status (db, http, runtimes, counts)", schema(nil)},
		{"list_sites", "List all managed WordPress sites with state", schema(nil)},
		{"get_site", "Full detail for one site incl. worktrees + credentials", schema(map[string]interface{}{"slug": prop("string", "site slug")}, "slug")},
		{"create_site", "Create, install and serve a WordPress site end-to-end", schema(map[string]interface{}{
			"name":        prop("string", "site name"),
			"dir":         prop("string", "where the site lives; must be empty or absent (default: ~/.agent-local/sites/<slug>). WordPress is installed into <dir>/wp"),
			"domain":      prop("string", "local domain (default: slug + configured suffix, see set_domain_suffix)"),
			"php_version": prop("string", "php version e.g. 8.3"),
			"wp_version":  prop("string", "wordpress version or 'latest'"),
			"repo":        prop("string", "git repo to clone instead of fresh wordpress"),
			"admin_user":  prop("string", "admin username"),
			"admin_pass":  prop("string", "admin password"),
			"title":       prop("string", "site title"),
			"async":       prop("boolean", "return a job id immediately and poll get_job instead of waiting"),
		}, "name")},
		{"attach_site", "Serve a directory that already exists as a site, with its own empty database. The caller's files are left alone: an existing wp-config.php is kept, and one is written only when WordPress core is present with no config at all. Use create_site for a fresh install, import_site when a database should be copied too.", schema(map[string]interface{}{
			"dir":         prop("string", "absolute path to the directory to serve; created if missing"),
			"name":        prop("string", "site name (default: the directory's own name)"),
			"domain":      prop("string", "local domain (default: slug + configured suffix)"),
			"php_version": prop("string", "php version e.g. 8.3"),
		}, "dir")},
		{"import_site", "Import a LocalWP site, a DDEV project, or any WordPress directory into agent-local. Copies the database (or loads a .sql dump, or serves with its existing DB), points wp-config at the embedded MariaDB, serves it. A stopped LocalWP site or DDEV project is started first so its database can be read. A DDEV project is then removed from DDEV (its own snapshot kept) unless keep_ddev is true.", schema(map[string]interface{}{
			"source":      prop("string", "LocalWP site name, DDEV project name, OR absolute path to a WordPress docroot"),
			"name":        prop("string", "site name (default: source name)"),
			"domain":      prop("string", "target domain (default: slug.test)"),
			"php_version": prop("string", "php version"),
			"copy":        prop("boolean", "copy files into ~/.agent-local instead of serving in place"),
			"sql_dump":    prop("string", "path to a .sql dump to load instead of copying from a live DB"),
			"serve_only":  prop("boolean", "don't touch any database; serve with the existing wp-config DB settings"),
			"keep_ddev":   prop("boolean", "leave a DDEV source project registered and running instead of moving it out (default false)"),
			"async":       prop("boolean", "return a job id immediately and poll get_job instead of waiting"),
			"db_host":     prop("string", "explicit source DB host"),
			"db_port":     prop("integer", "explicit source DB port"),
			"db_user":     prop("string", "explicit source DB user"),
			"db_pass":     prop("string", "explicit source DB password"),
			"db_name":     prop("string", "explicit source DB name"),
		}, "source")},
		{"localwp_sites", "List LocalWP sites available for import", schema(nil)},
		{"ddev_projects", "List DDEV projects available for import: name, status, type, approot, docroot, PHP version, primary URL. With Docker down, names and roots still come from DDEV's registry and status says so.", schema(nil)},
		{"set_media_fallback", "Point a site's missing uploads at an origin: any GET under /wp-content/uploads/ with no local file 302s there. This is what the Apache-only '.htaccess uploads rewrite' does, which the built-in router cannot read. Pass \"auto\" to adopt the rule already in the site's .htaccess, or an empty string to turn it off", schema(map[string]interface{}{
			"slug": prop("string", "site slug"),
			"url":  prop("string", "e.g. https://example.org — or \"auto\", or \"\" to disable")}, "slug", "url")},
		{"get_media_fallback", "A site's media fallback plus what its .htaccess implies", schema(map[string]interface{}{
			"slug": prop("string", "site slug")}, "slug")},
		{"set_sites_dir", "Set the parent directory new sites are created in. Existing sites stay where they are; pass an empty string to restore the default (~/.agent-local/sites)", schema(map[string]interface{}{
			"dir": prop("string", "e.g. ~/Sites — created if missing")}, "dir")},
		{"get_sites_dir", "Where new sites are created when create_site is called without a dir", schema(nil)},
		{"set_domain_suffix", "Set the default suffix for new sites/worktrees domains (default .al)", schema(map[string]interface{}{
			"suffix": prop("string", "e.g. .al, .test, .localhost — .test is the RFC 6761 reservation; avoid .local, which macOS resolves by mDNS")}, "suffix")},
		{"start_site", "Start a site (db + php-fpm + http)", schema(map[string]interface{}{"slug": prop("string", "site slug")}, "slug")},
		{"stop_site", "Stop a site", schema(map[string]interface{}{"slug": prop("string", "site slug")}, "slug")},
		{"restart_site", "Restart a site", schema(map[string]interface{}{"slug": prop("string", "site slug")}, "slug")},
		{"delete_site", "Delete a site. By default drops its database — after saving an automatic snapshot under ~/.agent-local/snapshots/<slug>/ — and removes files we created (an imported external checkout is only detached). keep_files/keep_db leave those behind so the folder can be re-adopted.", schema(map[string]interface{}{
			"slug":        prop("string", "site slug"),
			"keep_files":  prop("boolean", "leave the checkout on disk"),
			"keep_db":     prop("boolean", "leave the schema and user in place"),
			"no_snapshot": prop("boolean", "skip the automatic pre-delete snapshot")}, "slug")},
		{"switch_php", "Switch a site to another PHP version, installing or repairing that runtime first if it is not usable (default). An install runs brew and can take minutes, so pass async=1 style polling via get_job if you do not want to wait: the job id is in X-Job-Id. Versions homebrew-core has dropped (7.4, 8.0) need tap=true.", schema(map[string]interface{}{
			"slug":    prop("string", "site slug"),
			"version": prop("string", "php version, e.g. 7.4 or 8.3"),
			"install": prop("boolean", "install/repair the runtime if missing (default true); false = fail instead"),
			"tap":     prop("boolean", "allow the third-party shivammathur/php tap, the only source of PHP releases homebrew-core has deleted (7.4, 8.0)"),
			"async":   prop("boolean", "return a job id immediately and poll get_job instead of waiting on the install"),
		}, "slug", "version")},
		{"set_domain", "Change a site's local domain (hosts + cert follow)", schema(map[string]interface{}{
			"slug": prop("string", "site slug"), "domain": prop("string", "new domain")}, "slug", "domain")},
		{"db_creds", "Get DB connection params for a site (starts db if needed)", schema(map[string]interface{}{"slug": prop("string", "site slug")}, "slug")},
		{"db_query", "Run SQL as root. Pass slug to make that site's database the default schema; omit it for server-wide SQL (CREATE/DROP DATABASE, cross-db joins). Returns TSV with a header row. Any statement is allowed, including DDL and multi-statement scripts.", schema(map[string]interface{}{
			"sql":  prop("string", "SQL statement(s), semicolon-separated"),
			"slug": prop("string", "optional site slug: use its DB as default schema")}, "sql")},
		{"db_import", "Load a .sql or .sql.gz dump into a site's database, replacing current contents (streamed, so dump size does not matter). By default the dump's domains are search-replaced to this site's domain so it serves locally, and the current contents are saved as an automatic snapshot first.", schema(map[string]interface{}{
			"slug":        prop("string", "site slug"),
			"path":        prop("string", "absolute path to .sql or .sql.gz"),
			"keep_urls":   prop("boolean", "skip domain rewrite, keep URLs exactly as dumped"),
			"no_snapshot": prop("boolean", "skip the automatic pre-import snapshot")}, "slug", "path")},
		{"db_export", "Dump a site's database to a .sql file (default: ~/.agent-local/dumps/<slug>-<timestamp>.sql)", schema(map[string]interface{}{
			"slug": prop("string", "site slug"), "path": prop("string", "optional output path")}, "slug")},
		{"db_reset", "Empty a site's database (drop + recreate, grants preserved). The current contents are saved as an automatic snapshot first.", schema(map[string]interface{}{
			"slug":        prop("string", "site slug"),
			"no_snapshot": prop("boolean", "skip the automatic pre-reset snapshot")}, "slug")},
		{"db_snapshot", "Save a snapshot of a site's database: a gzipped dump under ~/.agent-local/snapshots/<slug>/. Automatic snapshots are also taken before db_import, db_reset, db_restore and delete_site; this is the manual save-point.", schema(map[string]interface{}{
			"slug": prop("string", "site slug"),
			"name": prop("string", "optional label; the returned timestamped name is what db_restore takes")}, "slug")},
		{"db_snapshots", "List a site's database snapshots, newest first", schema(map[string]interface{}{
			"slug": prop("string", "site slug")}, "slug")},
		{"db_restore", "Restore a database snapshot into a site, replacing current contents (default: the newest snapshot). A pre-restore snapshot is saved first, so a mis-aimed restore is itself restorable.", schema(map[string]interface{}{
			"slug":        prop("string", "site slug"),
			"name":        prop("string", "snapshot name from db_snapshots (default: newest)"),
			"no_snapshot": prop("boolean", "skip the automatic pre-restore snapshot")}, "slug")},
		{"db_tables", "List a site's tables with row counts and size in KB", schema(map[string]interface{}{
			"slug": prop("string", "site slug")}, "slug")},
		{"resolve_path", "Which managed site (or branch preview) owns a filesystem path — the lookup for integrations that key sites by checkout directory. Returns slug, url, docroot, php version, live DB connection details and cert state.", schema(map[string]interface{}{
			"path": prop("string", "absolute path to a site directory or any file inside it")}, "path")},
		{"cert_status", "TLS cert state for a domain: exists, trusted by the OS, expiry", schema(map[string]interface{}{
			"domain": prop("string", "e.g. mysite.test")}, "domain")},
		{"cert_trust", "Issue (if needed) and trust a domain's TLS cert in the system keychain", schema(map[string]interface{}{
			"domain": prop("string", "e.g. mysite.test")}, "domain")},
		{"yield_ports", "Free the bare-URL ports (:80/:443) for a window so another local-dev app (LocalWP) can start, then reclaim them automatically. Sites stay reachable on :1080 throughout.", schema(map[string]interface{}{
			"seconds": prop("number", "hand-off window, default 45, max 600")})},
		{"wp_cli", "Run wp-cli against a site", schema(map[string]interface{}{
			"slug": prop("string", "site slug"), "args": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "wp-cli args"}}, "slug")},
		{"add_worktree", "Create a git worktree branch served on its own domain", schema(map[string]interface{}{
			"slug": prop("string", "site slug"), "branch": prop("string", "git branch")}, "slug", "branch")},
		{"list_worktrees", "List worktrees of a site", schema(map[string]interface{}{"slug": prop("string", "site slug")}, "slug")},
		{"start_worktree", "Start a worktree's serving pool", schema(map[string]interface{}{"id": prop("string", "worktree id")}, "id")},
		{"stop_worktree", "Stop a worktree's serving pool", schema(map[string]interface{}{"id": prop("string", "worktree id")}, "id")},
		{"remove_worktree", "Remove a worktree (stops serving, deletes dir)", schema(map[string]interface{}{"id": prop("string", "worktree id")}, "id")},
		{"list_runtimes", "List installed PHP runtimes + db + http front. broken_phps lists kegs that are on disk but will not run, with the reason; install_runtime repairs those.", schema(nil)},
		{"install_runtime", "Install a runtime, or repair a broken PHP keg (same call: it detects which is needed). Runs brew, so it can take minutes — pass async and poll get_job, or wait. PHP 7.4 and 8.0 are no longer in homebrew-core and need tap=true.", schema(map[string]interface{}{
			"what":    prop("string", "php|mariadb|apache|brew"),
			"version": prop("string", "php version, e.g. 7.4 (default: the series brew's php formula tracks)"),
			"tap":     prop("boolean", "allow the third-party shivammathur/php tap for PHP releases homebrew-core has deleted (7.4, 8.0)"),
			"async":   prop("boolean", "return a job id immediately and poll get_job instead of waiting"),
		}, "what")},
		{"doctor", "Run health checks", schema(nil)},
		{"doctor_fix", "Auto-fix health issues that don't need a password prompt", schema(nil)},
		{"get_logs", "Tail a log: mysql, apache, daemon, fpm-<slug>, fpm-<worktree-id>, wp-<slug> (WordPress debug log, see set_wp_debug), or <slug>", schema(map[string]interface{}{
			"name": prop("string", "log name"), "lines": prop("number", "tail this many lines (default 100)")}, "name")},
		{"get_wp_debug", "A site's WP_DEBUG state and where its debug log goes", schema(map[string]interface{}{
			"slug": prop("string", "site slug")}, "slug")},
		{"set_wp_debug", "Flip a site's WP_DEBUG. On points WP_DEBUG_LOG at ~/.agent-local/logs/wp-<slug>.log (readable via get_logs with the returned log_name) and keeps errors out of rendered pages (WP_DEBUG_DISPLAY off). The go-to move when a site white-screens or misbehaves: turn it on, reproduce, read the log.", schema(map[string]interface{}{
			"slug": prop("string", "site slug"),
			"on":   prop("boolean", "true = enable, false = disable")}, "slug", "on")},
		{"get_http_front", "Which HTTP front is active (router|apache) and whether apache is installed", schema(nil)},
		{"set_http_front", "Switch the HTTP front; ports rebind within seconds", schema(map[string]interface{}{
			"front": prop("string", "router|apache")}, "front")},
		{"get_domain_suffix", "Current default domain suffix for new sites/previews", schema(nil)},
		{"list_branches", "Git branches of a site's repo (local + remote-only) plus existing previews — pick a target for add_worktree", schema(map[string]interface{}{
			"slug": prop("string", "site slug")}, "slug")},
		{"worktree_wp_cli", "Run wp-cli inside a branch preview (branch code, same DB + PHP as the site)", schema(map[string]interface{}{
			"id":   prop("string", "worktree id, e.g. sulo--no-footer"),
			"args": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "wp-cli args"}}, "id")},
		{"add_hosts_entries", "Register extra domains in /etc/hosts pointing at agent-local", schema(map[string]interface{}{
			"domains": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "domains"}}, "domains")},
		{"remove_hosts_entries", "Drop agent-local /etc/hosts entries for domains you registered", schema(map[string]interface{}{
			"domains": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "domains"}}, "domains")},
		{"list_jobs", "Recent long-running jobs (create, import, db-import) with status and progress steps", schema(nil)},
		{"get_job", "Status, progress steps and result of one job", schema(map[string]interface{}{
			"id": prop("string", "job id from X-Job-Id or an async start")}, "id")},
		{"open_adminer", "URL of the per-site Adminer database GUI (served at /.agent-local/adminer)", schema(map[string]interface{}{
			"slug": prop("string", "site slug")}, "slug")},
		{"list_mail", "Emails a site has sent, newest first. Every wp_mail()/PHP mail() is captured into a per-site inbox instead of vanishing into a mail server, so submitting a form and then reading the email it produced is a complete end-to-end check. Each entry carries url: the message page in the browser inbox, for looking at it with a browser tool.", schema(map[string]interface{}{
			"slug": prop("string", "site slug")}, "slug")},
		{"get_mail", "One captured email in full: decoded text and HTML bodies, headers, attachment metadata, plus url (the message page) and html_url (just the rendered HTML part, sandboxed) to open in a browser and see the email as the recipient would.", schema(map[string]interface{}{
			"slug": prop("string", "site slug"),
			"id":   prop("string", "message id from list_mail")}, "slug", "id")},
		{"clear_mail", "Empty a site's captured-mail inbox", schema(map[string]interface{}{
			"slug": prop("string", "site slug")}, "slug")},
		{"share_local_site", "Open a public URL for a local site through a Cloudflare quick tunnel — no account or token; anyone with the random https://….trycloudflare.com address can view the site while the tunnel is up. WordPress is mapped onto the tunnel host for tunnel requests only, so local URLs are untouched, and /.agent-local tooling (mail inbox, database GUI) stays local-only. Idempotent: an active share is returned rather than doubled. Auto-stops after minutes (default 60). Needs the router front; may brew-install cloudflared on first use — pass async and poll get_job if you do not want to wait.", schema(map[string]interface{}{
			"slug":    prop("string", "site slug"),
			"minutes": prop("number", "auto-stop after this many minutes (default 60; -1 = until stopped)"),
			"async":   prop("boolean", "return a job id immediately and poll get_job instead of waiting"),
		}, "slug")},
		{"unshare_local_site", "Close a site's public tunnel", schema(map[string]interface{}{
			"slug": prop("string", "site slug")}, "slug")},
	}
}

// strArgs coerces a JSON array argument into []string, erroring on a
// non-string member instead of silently dropping it — a malformed element
// (e.g. a stray number in a wp-cli arg list) is a caller bug worth surfacing,
// not something to swallow.
func strArgs(v interface{}) ([]string, error) {
	if v == nil {
		return []string{}, nil
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil, fmt.Errorf("expected an array, got %T", v)
	}
	out := make([]string, 0, len(arr))
	for i, x := range arr {
		s, ok := x.(string)
		if !ok {
			return nil, fmt.Errorf("element %d is not a string (got %T)", i, x)
		}
		out = append(out, s)
	}
	return out, nil
}

type toolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// validateRequired checks a tool call's arguments against the "required"
// list baked into its schema (see mcpTools/schema), so every dispatchTool
// case can assume its required arguments are present instead of building a
// path out of an empty string. Array-typed required arguments (e.g.
// "domains") only need to be present and array-shaped; string-typed ones
// must also be non-empty.
func validateRequired(tool mcpTool, args map[string]interface{}) *mcpErr {
	sch, _ := tool.InputSchema.(map[string]interface{})
	var required []string
	switch r := sch["required"].(type) {
	case []string:
		required = r
	case []interface{}:
		for _, x := range r {
			if s, ok := x.(string); ok {
				required = append(required, s)
			}
		}
	}
	props, _ := sch["properties"].(map[string]interface{})
	for _, key := range required {
		v, present := args[key]
		if !present || v == nil {
			return &mcpErr{Code: -32602, Message: "missing required argument: " + key}
		}
		if p, ok := props[key].(map[string]interface{}); ok {
			if t, _ := p["type"].(string); t == "array" {
				if _, ok := v.([]interface{}); !ok {
					return &mcpErr{Code: -32602, Message: "argument " + key + " must be an array"}
				}
				continue
			}
		}
		if s, ok := v.(string); !ok || s == "" {
			return &mcpErr{Code: -32602, Message: "missing required argument: " + key}
		}
	}
	return nil
}

func mcpCall(raw json.RawMessage) *mcpResp {
	var p toolCallParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return &mcpResp{Error: &mcpErr{Code: -32602, Message: "bad params: " + err.Error()}}
	}
	tool := toolByName(p.Name)
	if tool == nil {
		out := map[string]string{"error": "unknown tool: " + p.Name}
		text, _ := json.Marshal(out)
		content := []map[string]interface{}{{"type": "text", "text": string(text)}}
		return &mcpResp{Result: map[string]interface{}{"content": content, "isError": true}}
	}
	if verr := validateRequired(*tool, p.Arguments); verr != nil {
		return &mcpResp{Error: verr}
	}
	out, isErr := dispatchTool(p.Name, p.Arguments)
	text, _ := json.Marshal(out)
	content := []map[string]interface{}{{"type": "text", "text": string(text)}}
	return &mcpResp{Result: map[string]interface{}{"content": content, "isError": isErr}}
}

// dispatchTool maps a tool call onto a daemon API request.
func dispatchTool(name string, args map[string]interface{}) (interface{}, bool) {
	get := func(k string) string {
		if v, ok := args[k].(string); ok {
			return v
		}
		return ""
	}
	switch name {
	case "status":
		return apiGet("/status")
	case "list_sites":
		return apiGet("/sites")
	case "get_site":
		return apiGet("/sites/" + get("slug"))
	case "create_site":
		path := "/sites"
		if args["async"] == true {
			path += "?async=1"
		}
		return apiPost(path, args)
	case "attach_site":
		return apiPost("/attach", map[string]interface{}{
			"dir": get("dir"), "name": get("name"), "domain": get("domain"),
			"php_version": get("php_version"),
		})
	case "import_site":
		body := map[string]interface{}{
			"source": get("source"), "name": get("name"), "domain": get("domain"),
			"php_version": get("php_version"), "copy": args["copy"] == true,
			"sql_dump": get("sql_dump"), "serve_only": args["serve_only"] == true,
			"keep_ddev": args["keep_ddev"] == true,
			"db_host":   get("db_host"), "db_user": get("db_user"),
			"db_pass": get("db_pass"), "db_name": get("db_name"),
		}
		if p, ok := args["db_port"].(float64); ok {
			body["db_port"] = int(p)
		}
		path := "/import"
		if args["async"] == true {
			path += "?async=1"
		}
		return apiPost(path, body)
	case "localwp_sites":
		sites, err := ListLocalWPSites()
		if err != nil {
			return map[string]string{"error": err.Error()}, true
		}
		return sites, false
	case "ddev_projects":
		ps, err := ListDDEVProjects()
		if err != nil {
			return map[string]string{"error": err.Error()}, true
		}
		return ps, false
	case "set_media_fallback":
		return apiPost("/sites/"+get("slug")+"/media", map[string]string{"url": get("url")})
	case "get_media_fallback":
		return apiGet("/sites/" + get("slug") + "/media")
	case "set_sites_dir":
		return apiPost("/sites-dir", map[string]string{"dir": get("dir")})
	case "get_sites_dir":
		return apiGet("/sites-dir")
	case "set_domain_suffix":
		return apiPost("/suffix", map[string]string{"suffix": get("suffix")})
	case "start_site":
		return apiPost("/sites/"+get("slug")+"/start", nil)
	case "stop_site":
		return apiPost("/sites/"+get("slug")+"/stop", nil)
	case "restart_site":
		return apiPost("/sites/"+get("slug")+"/restart", nil)
	case "delete_site":
		q := []string{}
		if args["keep_files"] == true {
			q = append(q, "files=keep")
		}
		if args["keep_db"] == true {
			q = append(q, "db=keep")
		}
		if args["no_snapshot"] == true {
			q = append(q, "snapshot=off")
		}
		path := "/sites/" + get("slug")
		if len(q) > 0 {
			path += "?" + strings.Join(q, "&")
		}
		return apiDelete(path)
	case "switch_php":
		body := map[string]interface{}{"version": get("version"), "tap": args["tap"] == true}
		if v, okv := args["install"].(bool); okv {
			body["install"] = v
		}
		path := "/sites/" + get("slug") + "/php"
		if args["async"] == true {
			path += "?async=1"
		}
		return apiPost(path, body)
	case "set_domain":
		return apiPost("/sites/"+get("slug")+"/domain", map[string]string{"domain": get("domain")})
	case "db_creds":
		return apiPost("/sites/"+get("slug")+"/db", nil)
	case "db_query":
		if slug := get("slug"); slug != "" {
			return apiPost("/sites/"+slug+"/db/query", map[string]string{"sql": get("sql")})
		}
		return apiPost("/db/query", map[string]string{"sql": get("sql")})
	case "db_import":
		return apiPost("/sites/"+get("slug")+"/db/import", map[string]interface{}{
			"path": get("path"), "keep_urls": args["keep_urls"] == true,
			"no_snapshot": args["no_snapshot"] == true})
	case "db_export":
		return apiPost("/sites/"+get("slug")+"/db/export", map[string]string{"path": get("path")})
	case "db_reset":
		return apiPost("/sites/"+get("slug")+"/db/reset", map[string]interface{}{
			"no_snapshot": args["no_snapshot"] == true})
	case "db_snapshot":
		return apiPost("/sites/"+get("slug")+"/db/snapshot", map[string]string{"name": get("name")})
	case "db_snapshots":
		return apiGet("/sites/" + get("slug") + "/db/snapshots")
	case "db_restore":
		return apiPost("/sites/"+get("slug")+"/db/restore", map[string]interface{}{
			"name": get("name"), "no_snapshot": args["no_snapshot"] == true})
	case "resolve_path":
		return apiGet("/resolve?path=" + url.QueryEscape(get("path")))
	case "cert_status":
		return apiGet("/certs/" + get("domain"))
	case "cert_trust":
		return apiPost("/certs/"+get("domain")+"/trust", nil)
	case "db_tables":
		return apiGet("/sites/" + get("slug") + "/db/tables")
	case "wp_cli":
		wpArgs, err := strArgs(args["args"])
		if err != nil {
			return map[string]string{"error": "args: " + err.Error()}, true
		}
		return apiPost("/sites/"+get("slug")+"/wp-cli", map[string]interface{}{"args": wpArgs})
	case "yield_ports":
		body := map[string]interface{}{}
		if n, okn := args["seconds"].(float64); okn {
			body["seconds"] = int(n)
		}
		return apiPost("/yield", body)
	case "add_worktree":
		return apiPost("/sites/"+get("slug")+"/worktrees", map[string]string{"branch": get("branch")})
	case "list_worktrees":
		return apiGet("/sites/" + get("slug") + "/worktrees")
	case "start_worktree":
		id := get("id")
		site, ok := worktreeSiteSlug(id)
		if !ok {
			return map[string]string{"error": "invalid worktree id: " + id}, true
		}
		return apiPost("/sites/"+site+"/worktrees/"+id+"/start", nil)
	case "stop_worktree":
		id := get("id")
		site, ok := worktreeSiteSlug(id)
		if !ok {
			return map[string]string{"error": "invalid worktree id: " + id}, true
		}
		return apiPost("/sites/"+site+"/worktrees/"+id+"/stop", nil)
	case "remove_worktree":
		id := get("id")
		site, ok := worktreeSiteSlug(id)
		if !ok {
			return map[string]string{"error": "invalid worktree id: " + id}, true
		}
		return apiDelete("/sites/" + site + "/worktrees/" + id)
	case "list_runtimes":
		return apiGet("/runtimes")
	case "install_runtime":
		path := "/install"
		if args["async"] == true {
			path += "?async=1"
		}
		return apiPost(path, map[string]interface{}{
			"what": get("what"), "version": get("version"), "tap": args["tap"] == true})
	case "doctor":
		return apiGet("/doctor")
	case "doctor_fix":
		return apiPost("/doctor/fix", nil)
	case "get_wp_debug":
		return apiGet("/sites/" + get("slug") + "/wp-debug")
	case "set_wp_debug":
		return apiPost("/sites/"+get("slug")+"/wp-debug", map[string]interface{}{"on": args["on"] == true})
	case "get_logs":
		q := "/logs/" + get("name")
		if n, okn := args["lines"].(float64); okn && n > 0 {
			q += fmt.Sprintf("?lines=%d", int(n))
		}
		return apiGet(q)
	case "get_http_front":
		return apiGet("/front")
	case "set_http_front":
		return apiPost("/front", map[string]string{"front": get("front")})
	case "get_domain_suffix":
		return apiGet("/suffix")
	case "list_branches":
		return apiGet("/sites/" + get("slug") + "/branches")
	case "worktree_wp_cli":
		id := get("id")
		site, ok := worktreeSiteSlug(id)
		if !ok {
			return map[string]string{"error": "invalid worktree id: " + id}, true
		}
		wtArgs, err := strArgs(args["args"])
		if err != nil {
			return map[string]string{"error": "args: " + err.Error()}, true
		}
		return apiPost("/sites/"+site+"/worktrees/"+id+"/wp-cli", map[string]interface{}{"args": wtArgs})
	case "add_hosts_entries":
		domains, err := strArgs(args["domains"])
		if err != nil {
			return map[string]string{"error": "domains: " + err.Error()}, true
		}
		return apiPost("/hosts", map[string]interface{}{"domains": domains})
	case "remove_hosts_entries":
		domains, err := strArgs(args["domains"])
		if err != nil {
			return map[string]string{"error": "domains: " + err.Error()}, true
		}
		return apiDeleteBody("/hosts", map[string]interface{}{"domains": domains})
	case "list_jobs":
		return apiGet("/jobs")
	case "get_job":
		return apiGet("/jobs/" + get("id"))
	case "open_adminer":
		return apiGet("/sites/" + get("slug") + "/adminer")
	case "list_mail":
		return apiGet("/sites/" + get("slug") + "/mail")
	case "get_mail":
		return apiGet("/sites/" + get("slug") + "/mail/" + get("id"))
	case "clear_mail":
		return apiDelete("/sites/" + get("slug") + "/mail")
	case "share_local_site":
		body := map[string]interface{}{}
		if n, okn := args["minutes"].(float64); okn {
			body["minutes"] = int(n)
		}
		path := "/sites/" + get("slug") + "/share"
		if args["async"] == true {
			path += "?async=1"
		}
		return apiPost(path, body)
	case "unshare_local_site":
		return apiDelete("/sites/" + get("slug") + "/share")
	default:
		return map[string]string{"error": "unknown tool: " + name}, true
	}
}

// worktreeSiteSlug extracts the site-slug prefix from a worktree id. Worktree
// ids are built as slug + "--" + branchSlug (see AddWorktree in sites.go);
// both halves are produced by Slugify, which collapses every run of non-
// alphanumeric characters to a single "-", so neither half can itself
// contain "--". The id's first "--" is therefore always the separator, and
// splitting there is unambiguous for any id this codebase produced — it only
// fails for a caller-supplied id that never came from AddWorktree.
func worktreeSiteSlug(id string) (string, bool) {
	i := strings.Index(id, "--")
	if i <= 0 || i+2 >= len(id) {
		return "", false
	}
	return id[:i], true
}

// ---------- daemon API client ----------

func apiBase() string { return fmt.Sprintf("http://127.0.0.1:%d", DefaultAPIPort) }

// apiHTTP is the one client every call shares, so loopback connections are
// reused across a session instead of a fresh dial per call. No client-level
// timeout: that is set per request in apiDo by method.
var apiHTTP = &http.Client{}

// apiTimeout bounds one daemon call. Reads must fail fast — a wedged daemon
// used to leave `status` or a `get_job` poll hanging for the full 30 minutes
// meant for imports, with no way to tell "working" from "stuck". Mutations
// keep the long bound: create/import/install genuinely run for minutes, and
// callers wanting a quick return already have async=true plus get_job.
func apiTimeout(method string) time.Duration {
	if method == http.MethodGet {
		return 20 * time.Second
	}
	return 30 * time.Minute
}

func apiClient() (*http.Client, string, error) {
	tok, err := APIToken()
	if err != nil {
		return nil, "", err
	}
	return apiHTTP, tok, nil
}

func apiDo(method, path string, body interface{}) (interface{}, bool) {
	client, tok, err := apiClient()
	if err != nil {
		return map[string]string{"error": err.Error()}, true
	}
	var rd io.Reader
	if body != nil {
		b, merr := json.Marshal(body)
		if merr != nil {
			return map[string]string{"error": "encoding request body: " + merr.Error()}, true
		}
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout(method))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, apiBase()+path, rd)
	if err != nil {
		return map[string]string{"error": err.Error()}, true
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		// Daemon down. GET is idempotent, so it is safe to start the daemon and
		// retry once — that's what makes the first call after a reboot spawn it
		// instead of failing. A POST/PUT/DELETE might have partially landed on
		// the daemon side before the connection dropped; retrying it blindly
		// could run a mutation twice, so it only gets the daemon started for
		// next time and fails this call.
		EnsureRouterDaemonQuiet()
		if method == http.MethodGet {
			resp, err = client.Do(req)
		}
		if err != nil {
			return map[string]string{"error": "daemon unreachable: " + err.Error()}, true
		}
	}
	defer resp.Body.Close()
	var out struct {
		OK    bool            `json:"ok"`
		Error string          `json:"error"`
		Data  json.RawMessage `json:"data"`
	}
	if derr := json.NewDecoder(resp.Body).Decode(&out); derr != nil {
		return map[string]string{"error": "decoding response: " + derr.Error()}, true
	}
	if resp.StatusCode >= 400 || !out.OK {
		msg := out.Error
		if msg == "" {
			msg = resp.Status
		}
		return map[string]string{"error": msg}, true
	}
	var data interface{}
	if len(out.Data) > 0 {
		json.Unmarshal(out.Data, &data)
	}
	return data, false
}

func apiGet(path string) (interface{}, bool)    { return apiDo("GET", path, nil) }
func apiDelete(path string) (interface{}, bool) { return apiDo("DELETE", path, nil) }
func apiDeleteBody(path string, body interface{}) (interface{}, bool) {
	return apiDo("DELETE", path, body)
}
func apiPost(path string, body interface{}) (interface{}, bool) {
	return apiDo("POST", path, body)
}

// EnsureRouterDaemonQuiet starts the daemon if the agent API is not answering.
// The API is what MCP talks to, and it lives in the daemon under either front.
func EnsureRouterDaemonQuiet() {
	if portOpen(DefaultAPIPort) {
		// Already serving — but make sure a reboot brings it back without anyone
		// having to run a command.
		_ = EnsureDaemonAutostart()
		return
	}
	_ = EnsureDaemonAutostart()
	_ = spawnDaemon()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if portOpen(DefaultAPIPort) {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
}
