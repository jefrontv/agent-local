package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
	enc := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req mcpReq
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		// A request without an id is a notification: the spec forbids answering
		// it, and a client that gets an answer anyway can wedge waiting to match
		// it against a request it never made.
		if req.ID == nil {
			continue
		}
		resp := mcpHandle(&req)
		if resp != nil {
			// Set centrally so no handler can forget it. Responses used to go out
			// as `"jsonrpc":""`, which a strict client rejects — the server looked
			// like it was doing nothing at all.
			resp.JSONRPC = "2.0"
			resp.ID = req.ID
			enc.Encode(resp)
		}
	}
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
		"  Try one call:       echo '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}' | agent-local mcp\n" +
		"  Everything here is also a CLI command:  agent-local help\n\n" +
		"  Listening on stdin — ctrl-c to quit.\n"
}

// mcpClientConfig is the block to paste into a client's config file.
func mcpClientConfig() string {
	bin, err := os.Executable()
	if err != nil || bin == "" {
		bin = "agent-local"
	}
	if p, err := filepath.EvalSymlinks(bin); err == nil {
		bin = p
	}
	return fmt.Sprintf(`{
  "mcpServers": {
    "agent-local": {
      "command": %q,
      "args": ["mcp"]
    }
  }
}
`, bin)
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
		return &mcpResp{Result: map[string]interface{}{"tools": mcpTools()}}
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
		}, "name")},
		{"attach_site", "Serve a directory that already exists as a site, with its own empty database. The caller's files are left alone: an existing wp-config.php is kept, and one is written only when WordPress core is present with no config at all. Use create_site for a fresh install, import_site when a database should be copied too.", schema(map[string]interface{}{
			"dir":         prop("string", "absolute path to the directory to serve; created if missing"),
			"name":        prop("string", "site name (default: the directory's own name)"),
			"domain":      prop("string", "local domain (default: slug + configured suffix)"),
			"php_version": prop("string", "php version e.g. 8.3"),
		}, "dir")},
		{"import_site", "Import a LocalWP site or any WordPress directory into agent-local. Copies the database (or loads a .sql dump, or serves with its existing DB), points wp-config at the embedded MariaDB, serves it.", schema(map[string]interface{}{
			"source":      prop("string", "LocalWP site name OR absolute path to a WordPress docroot"),
			"name":        prop("string", "site name (default: source name)"),
			"domain":      prop("string", "target domain (default: slug.test)"),
			"php_version": prop("string", "php version"),
			"copy":        prop("boolean", "copy files into ~/.agent-local instead of serving in place"),
			"sql_dump":    prop("string", "path to a .sql dump to load instead of copying from a live DB"),
			"serve_only":  prop("boolean", "don't touch any database; serve with the existing wp-config DB settings"),
			"db_host":     prop("string", "explicit source DB host"),
			"db_port":     prop("integer", "explicit source DB port"),
			"db_user":     prop("string", "explicit source DB user"),
			"db_pass":     prop("string", "explicit source DB password"),
			"db_name":     prop("string", "explicit source DB name"),
		}, "source")},
		{"localwp_sites", "List LocalWP sites available for import", schema(nil)},
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
		{"delete_site", "Delete a site. By default drops its database and removes files we created (an imported external checkout is only detached). keep_files/keep_db leave those behind so the folder can be re-adopted.", schema(map[string]interface{}{
			"slug":       prop("string", "site slug"),
			"keep_files": prop("boolean", "leave the checkout on disk"),
			"keep_db":    prop("boolean", "leave the schema and user in place")}, "slug")},
		{"switch_php", "Switch a site to another installed PHP version", schema(map[string]interface{}{
			"slug": prop("string", "site slug"), "version": prop("string", "php version")}, "slug", "version")},
		{"set_domain", "Change a site's local domain (hosts + cert follow)", schema(map[string]interface{}{
			"slug": prop("string", "site slug"), "domain": prop("string", "new domain")}, "slug", "domain")},
		{"db_creds", "Get DB connection params for a site (starts db if needed)", schema(map[string]interface{}{"slug": prop("string", "site slug")}, "slug")},
		{"db_query", "Run SQL as root. Pass slug to make that site's database the default schema; omit it for server-wide SQL (CREATE/DROP DATABASE, cross-db joins). Returns TSV with a header row. Any statement is allowed, including DDL and multi-statement scripts.", schema(map[string]interface{}{
			"sql":  prop("string", "SQL statement(s), semicolon-separated"),
			"slug": prop("string", "optional site slug: use its DB as default schema")}, "sql")},
		{"db_import", "Load a .sql or .sql.gz dump into a site's database, replacing current contents (streamed, so dump size does not matter). By default the dump's domains are search-replaced to this site's domain so it serves locally.", schema(map[string]interface{}{
			"slug":      prop("string", "site slug"),
			"path":      prop("string", "absolute path to .sql or .sql.gz"),
			"keep_urls": prop("boolean", "skip domain rewrite, keep URLs exactly as dumped")}, "slug", "path")},
		{"db_export", "Dump a site's database to a .sql file (default: ~/.agent-local/dumps/<slug>-<timestamp>.sql)", schema(map[string]interface{}{
			"slug": prop("string", "site slug"), "path": prop("string", "optional output path")}, "slug")},
		{"db_reset", "Empty a site's database (drop + recreate, grants preserved)", schema(map[string]interface{}{
			"slug": prop("string", "site slug")}, "slug")},
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
		{"list_runtimes", "List installed PHP runtimes + db + http front", schema(nil)},
		{"install_runtime", "Install brew/php/mariadb/apache", schema(map[string]interface{}{
			"what": prop("string", "php|mariadb|apache|brew"), "version": prop("string", "version for php")}, "what")},
		{"doctor", "Run health checks", schema(nil)},
		{"doctor_fix", "Auto-fix health issues that don't need a password prompt", schema(nil)},
		{"get_logs", "Tail a log: mysql, apache, daemon, fpm-<slug>, fpm-<worktree-id>, or <slug>", schema(map[string]interface{}{
			"name": prop("string", "log name"), "lines": prop("number", "tail this many lines (default 100)")}, "name")},
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
	}
}

// strArgs coerces a JSON array argument into []string, tolerating absent or
// non-string members so a sloppy tool call still runs.
func strArgs(v interface{}) []string {
	out := []string{}
	arr, ok := v.([]interface{})
	if !ok {
		return out
	}
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

type toolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

func mcpCall(raw json.RawMessage) *mcpResp {
	var p toolCallParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return &mcpResp{Error: &mcpErr{Code: -32602, Message: "bad params"}}
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
		return apiPost("/sites", args)
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
			"db_host": get("db_host"), "db_user": get("db_user"),
			"db_pass": get("db_pass"), "db_name": get("db_name"),
		}
		if p, ok := args["db_port"].(float64); ok {
			body["db_port"] = int(p)
		}
		return apiPost("/import", body)
	case "localwp_sites":
		sites, err := ListLocalWPSites()
		if err != nil {
			return map[string]string{"error": err.Error()}, true
		}
		return sites, false
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
		path := "/sites/" + get("slug")
		if len(q) > 0 {
			path += "?" + strings.Join(q, "&")
		}
		return apiDelete(path)
	case "switch_php":
		return apiPost("/sites/"+get("slug")+"/php", map[string]string{"version": get("version")})
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
			"path": get("path"), "keep_urls": args["keep_urls"] == true})
	case "db_export":
		return apiPost("/sites/"+get("slug")+"/db/export", map[string]string{"path": get("path")})
	case "db_reset":
		return apiPost("/sites/"+get("slug")+"/db/reset", nil)
	case "resolve_path":
		return apiGet("/resolve?path=" + url.QueryEscape(get("path")))
	case "cert_status":
		return apiGet("/certs/" + get("domain"))
	case "cert_trust":
		return apiPost("/certs/"+get("domain")+"/trust", nil)
	case "db_tables":
		return apiGet("/sites/" + get("slug") + "/db/tables")
	case "wp_cli":
		return apiPost("/sites/"+get("slug")+"/wp-cli", map[string]interface{}{"args": strArgs(args["args"])})
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
		site := strings.SplitN(id, "--", 2)[0]
		return apiPost("/sites/"+site+"/worktrees/"+id+"/start", nil)
	case "stop_worktree":
		id := get("id")
		site := strings.SplitN(id, "--", 2)[0]
		return apiPost("/sites/"+site+"/worktrees/"+id+"/stop", nil)
	case "remove_worktree":
		id := get("id")
		site := strings.SplitN(id, "--", 2)[0]
		return apiDelete("/sites/" + site + "/worktrees/" + id)
	case "list_runtimes":
		return apiGet("/runtimes")
	case "install_runtime":
		return apiPost("/install", map[string]string{"what": get("what"), "version": get("version")})
	case "doctor":
		return apiGet("/doctor")
	case "doctor_fix":
		return apiPost("/doctor/fix", nil)
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
		site := strings.SplitN(id, "--", 2)[0]
		return apiPost("/sites/"+site+"/worktrees/"+id+"/wp-cli", map[string]interface{}{"args": strArgs(args["args"])})
	case "add_hosts_entries":
		return apiPost("/hosts", map[string]interface{}{"domains": strArgs(args["domains"])})
	case "remove_hosts_entries":
		return apiDeleteBody("/hosts", map[string]interface{}{"domains": strArgs(args["domains"])})
	default:
		return map[string]string{"error": "unknown tool: " + name}, true
	}
}

// ---------- daemon API client ----------

func apiBase() string { return fmt.Sprintf("http://127.0.0.1:%d", DefaultAPIPort) }

func apiClient() (*http.Client, string, error) {
	tok, err := APIToken()
	if err != nil {
		return nil, "", err
	}
	return &http.Client{Timeout: 1800 * time.Second}, tok, nil
}

func apiDo(method, path string, body interface{}) (interface{}, bool) {
	client, tok, err := apiClient()
	if err != nil {
		return map[string]string{"error": err.Error()}, true
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, apiBase()+path, rd)
	if err != nil {
		return map[string]string{"error": err.Error()}, true
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		// daemon down → try to start it, retry once
		if method == "GET" {
			EnsureRouterDaemonQuiet()
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
	json.NewDecoder(resp.Body).Decode(&out)
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
