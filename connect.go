package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ---------- registry ----------
//
// Each entry names a coding-agent harness whose MCP config location and
// schema has been verified against its own docs or source (see the comment
// on each one). A harness that could not be verified is left out rather than
// guessed at — a wrong path silently does nothing, which is worse than not
// offering it.

// harnessFormat is how a harness's config file holds server entries.
type harnessFormat int

const (
	fmtMCPServers     harnessFormat = iota // {"mcpServers": {name: {...}}}
	fmtServers                             // {"servers": {name: {...}}}   (VS Code)
	fmtContextServers                      // {"context_servers": {name: {"source":"custom", ...}}}  (Zed)
	fmtCodexTOML                           // [mcp_servers.name] table     (Codex)
)

// Harness describes one coding-agent MCP client agent-local can register
// itself with.
type Harness struct {
	ID     string
	Name   string
	Path   string // absolute config file path (~ already expanded)
	Bin    string // CLI binary name checked on PATH, "" if none
	Format harnessFormat
}

// harnessRegistry builds the list of harnesses against a given home
// directory, so tests can point it at a temp HOME.
//
// Sources (config path + schema), verified 2026-09-02:
//   - Claude Code:    user-scope entries live in ~/.claude.json under
//     "mcpServers"; `claude mcp add --scope user` is documented to write
//     there (code.claude.com/docs/en/mcp-servers, anthropics/claude-code#54803).
//   - Claude Desktop: ~/Library/Application Support/Claude/claude_desktop_config.json,
//     "mcpServers" (Anthropic's standard desktop config, same shape everywhere it's used).
//   - Codex CLI:      ~/.codex/config.toml, one [mcp_servers.<name>] table with
//     command/args (developers.openai.com/codex/config-reference).
//   - Gemini CLI:     ~/.gemini/settings.json, "mcpServers"
//     (google-gemini/gemini-cli docs/tools/mcp-server.md).
//   - Cursor:         ~/.cursor/mcp.json, "mcpServers" (cursor.com/docs/mcp).
//   - Windsurf:       ~/.codeium/windsurf/mcp_config.json, "mcpServers"
//     (Codeium's legacy directory survived the Windsurf rebrand).
//   - VS Code:        ~/Library/Application Support/Code/User/mcp.json, top-level
//     "servers" key, not "mcpServers" (code.visualstudio.com/docs/agents/reference/mcp-configuration).
//   - Qwen Code:      ~/.qwen/settings.json, "mcpServers" (QwenLM/qwen-code docs/users/features/mcp.md;
//     Qwen Code is a Gemini CLI fork and kept the same schema).
//   - Zed:            ~/.config/zed/settings.json, "context_servers", entries carry
//     "source":"custom" for a hand-written command (zed.dev/docs/ai/mcp).
//
// Oh My Pi / pi has no documented, stable MCP config file location as of this
// writing, so it is left out rather than guessed at.
func harnessRegistry(home string) []Harness {
	return []Harness{
		{ID: "claude-code", Name: "Claude Code", Bin: "claude", Format: fmtMCPServers,
			Path: filepath.Join(home, ".claude.json")},
		{ID: "claude-desktop", Name: "Claude Desktop", Format: fmtMCPServers,
			Path: filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")},
		{ID: "codex", Name: "Codex CLI", Bin: "codex", Format: fmtCodexTOML,
			Path: filepath.Join(home, ".codex", "config.toml")},
		{ID: "gemini", Name: "Gemini CLI", Bin: "gemini", Format: fmtMCPServers,
			Path: filepath.Join(home, ".gemini", "settings.json")},
		{ID: "cursor", Name: "Cursor", Bin: "cursor", Format: fmtMCPServers,
			Path: filepath.Join(home, ".cursor", "mcp.json")},
		{ID: "windsurf", Name: "Windsurf", Format: fmtMCPServers,
			Path: filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")},
		{ID: "vscode", Name: "VS Code", Bin: "code", Format: fmtServers,
			Path: filepath.Join(home, "Library", "Application Support", "Code", "User", "mcp.json")},
		{ID: "qwen", Name: "Qwen Code", Bin: "qwen", Format: fmtMCPServers,
			Path: filepath.Join(home, ".qwen", "settings.json")},
		{ID: "zed", Name: "Zed", Bin: "zed", Format: fmtContextServers,
			Path: filepath.Join(home, ".config", "zed", "settings.json")},
	}
}

const connectServerName = "agent-local"

// ---------- status ----------

// HarnessStatus is one row of `connect --list` / `connect --json`: what was
// found, against what agent-local binary is currently running.
type HarnessStatus struct {
	Harness
	Installed  bool   `json:"installed"`
	Configured bool   `json:"configured"`
	Stale      bool   `json:"stale"`
	StaleCmd   string `json:"stale_command,omitempty"`
}

// agentLocalBinaryPath resolves the absolute, symlink-free path to the binary
// currently running — the same resolution mcpClientConfig uses, factored out
// so both writers agree on what "this binary" means.
func agentLocalBinaryPath() string {
	bin, err := os.Executable()
	if err != nil || bin == "" {
		return "agent-local"
	}
	if p, err := filepath.EvalSymlinks(bin); err == nil {
		bin = p
	}
	return bin
}

// mcpServerEntry is the JSON shape written for a stdio MCP server everywhere
// except Zed, which adds a "source" tag (see serverEntryFor).
func mcpServerEntry(bin string) map[string]interface{} {
	return map[string]interface{}{
		"command": bin,
		"args":    []string{"mcp"},
	}
}

func serverEntryFor(h Harness, bin string) map[string]interface{} {
	if h.Format == fmtContextServers {
		return map[string]interface{}{
			"source":  "custom",
			"command": bin,
			"args":    []string{"mcp"},
			"env":     map[string]interface{}{},
		}
	}
	return mcpServerEntry(bin)
}

// topKeyFor names the object a harness's entries live under.
func topKeyFor(f harnessFormat) string {
	switch f {
	case fmtServers:
		return "servers"
	case fmtContextServers:
		return "context_servers"
	default:
		return "mcpServers"
	}
}

// entryCommand reads back the "command" a harness's config currently has for
// agent-local, so status and idempotency can be judged against it.
func entryCommand(h Harness) (cmd string, present bool, err error) {
	if h.Format == fmtCodexTOML {
		return tomlEntryCommand(h.Path, "mcp_servers."+connectServerName)
	}
	raw, err := readJSONObject(h.Path)
	if err != nil {
		return "", false, err
	}
	top, _ := raw[topKeyFor(h.Format)].(map[string]interface{})
	if top == nil {
		return "", false, nil
	}
	entry, ok := top[connectServerName].(map[string]interface{})
	if !ok {
		return "", false, nil
	}
	cmd, _ = entry["command"].(string)
	return cmd, true, nil
}

// binOnPath reports whether name resolves on PATH.
func binOnPath(name string) bool {
	if name == "" {
		return false
	}
	_, err := exec.LookPath(name)
	return err == nil
}

// statusFor computes one harness's status against the current binary.
func statusFor(h Harness, bin string) (HarnessStatus, error) {
	st := HarnessStatus{Harness: h}
	if _, err := os.Stat(filepath.Dir(h.Path)); err == nil {
		st.Installed = true
	}
	if binOnPath(h.Bin) {
		st.Installed = true
	}
	cmd, present, err := entryCommand(h)
	if err != nil {
		return st, err
	}
	if present {
		if cmd == bin {
			st.Configured = true
		} else {
			st.Stale = true
			st.StaleCmd = cmd
		}
	}
	return st, nil
}

// DetectHarnesses returns the status of every registered harness, home
// resolved from $HOME so tests can override it.
func DetectHarnesses() ([]HarnessStatus, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	bin := agentLocalBinaryPath()
	var out []HarnessStatus
	for _, h := range harnessRegistry(home) {
		st, err := statusFor(h, bin)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", h.ID, err)
		}
		out = append(out, st)
	}
	return out, nil
}

func findHarness(statuses []HarnessStatus, id string) *HarnessStatus {
	for i := range statuses {
		if statuses[i].ID == id {
			return &statuses[i]
		}
	}
	return nil
}

// ---------- writers ----------

// ConnectHarness registers agent-local in one harness's config, in place. A
// harness already pointing at the current binary is left untouched — this is
// the idempotency `connect` promises on a second run.
func ConnectHarness(h Harness) (wrote bool, err error) {
	return connectHarnessWithBinary(h, agentLocalBinaryPath())
}

// connectHarnessWithBinary is ConnectHarness with the target binary
// explicit, so tests can exercise it without depending on os.Executable().
func connectHarnessWithBinary(h Harness, bin string) (wrote bool, err error) {
	cmd, present, err := entryCommand(h)
	if err != nil {
		return false, err
	}
	if present && cmd == bin {
		return false, nil
	}
	// `claude mcp add` refuses a name that already exists, so it only serves
	// the fresh case; a stale entry is rewritten in the file it lives in.
	if h.ID == "claude-code" && !present && binOnPath("claude") {
		out, err := exec.Command("claude", "mcp", "add", "--scope", "user", connectServerName, "--", bin, "mcp").CombinedOutput()
		if err != nil {
			return false, fmt.Errorf("claude mcp add: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return true, nil
	}
	if h.Format == fmtCodexTOML {
		if err := tomlWriteEntry(h.Path, "mcp_servers."+connectServerName, bin); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := jsonWriteEntry(h.Path, topKeyFor(h.Format), connectServerName, serverEntryFor(h, bin)); err != nil {
		return false, err
	}
	return true, nil
}

// readJSONObject reads a JSON file into a map, treating a missing file as an
// empty object. A file that exists but fails to parse is an error, never
// silently discarded — that would mean overwriting whatever the user has.
func readJSONObject(path string) (map[string]interface{}, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]interface{}{}, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return map[string]interface{}{}, nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// jsonWriteEntry sets raw[topKey][name] = entry, preserving every other key
// exactly as it stood, and writes the file atomically.
func jsonWriteEntry(path, topKey, name string, entry map[string]interface{}) error {
	raw, err := readJSONObject(path)
	if err != nil {
		return err
	}
	top, _ := raw[topKey].(map[string]interface{})
	if top == nil {
		top = map[string]interface{}{}
	}
	top[name] = entry
	raw[topKey] = top

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return atomicWrite(path, out)
}

// atomicWrite writes via a temp file + rename so a crash mid-write never
// truncates the original. A file that already existed keeps its permission
// bits (0600 stays 0600); a new one gets 0644.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".agent-local-connect-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// ---------- Codex TOML ----------
//
// Codex's config is TOML and there is no TOML library in this module. Rather
// than pull one in for a single table, this does the narrow thing the format
// actually needs: find `[mcp_servers.agent-local]`, read or set its `command`
// line, and never touch anything outside that table.

var codexTableHeader = regexp.MustCompile(`^\[([^]]+)\]\s*$`)
var codexCommandLine = regexp.MustCompile(`^command\s*=\s*"([^"]*)"\s*$`)

// tomlTableBounds returns the [start,end) line range of a table's body (the
// lines strictly between its header and the next header, or EOF), and
// whether the table was found at all.
func tomlTableBounds(lines []string, table string) (start, end int, found bool) {
	header := "[" + table + "]"
	for i, l := range lines {
		if strings.TrimSpace(l) != header {
			continue
		}
		start = i + 1
		end = len(lines)
		for j := start; j < len(lines); j++ {
			if codexTableHeader.MatchString(strings.TrimSpace(lines[j])) {
				end = j
				break
			}
		}
		return start, end, true
	}
	return 0, 0, false
}

func tomlEntryCommand(path, table string) (cmd string, present bool, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	lines := strings.Split(string(b), "\n")
	start, end, found := tomlTableBounds(lines, table)
	if !found {
		return "", false, nil
	}
	for i := start; i < end; i++ {
		if m := codexCommandLine.FindStringSubmatch(strings.TrimSpace(lines[i])); m != nil {
			return m[1], true, nil
		}
	}
	return "", true, nil // table exists but has no command line yet
}

// tomlWriteEntry appends the table when absent, or rewrites only its command
// line in place when present with a different value.
func tomlWriteEntry(path, table, bin string) error {
	b, err := os.ReadFile(path)
	notExist := os.IsNotExist(err)
	if err != nil && !notExist {
		return err
	}
	content := string(b)
	lines := strings.Split(content, "\n")
	if notExist {
		lines = nil
	}
	start, end, found := tomlTableBounds(lines, table)
	commandLine := fmt.Sprintf(`command = %q`, bin)

	var out []byte
	if !found {
		body := content
		if len(body) > 0 && !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		if len(body) > 0 {
			body += "\n"
		}
		body += "[" + table + "]\n" + commandLine + "\n" + `args = ["mcp"]` + "\n"
		out = []byte(body)
	} else {
		replaced := false
		for i := start; i < end; i++ {
			if codexCommandLine.MatchString(strings.TrimSpace(lines[i])) {
				lines[i] = commandLine
				replaced = true
				break
			}
		}
		if !replaced {
			// Table exists (e.g. only an `args` line so far) but has no command
			// yet: insert right after the header, which is line start-1.
			ins := start
			lines = append(lines[:ins], append([]string{commandLine}, lines[ins:]...)...)
		}
		out = []byte(strings.Join(lines, "\n"))
	}
	return atomicWrite(path, out)
}

// ---------- CLI ----------

func cmdConnect(args []string) error {
	if hasFlag(args, "--json") {
		return connectPrintJSON()
	}
	if hasFlag(args, "--list") {
		return connectPrintList()
	}
	if hasFlag(args, "--all") {
		return connectApplyAll(args)
	}
	ids := positional(args)
	if len(ids) > 0 {
		return connectApplyNamed(ids)
	}
	if !isTerminal(os.Stdout) {
		if err := connectPrintList(); err != nil {
			return err
		}
		fmt.Println("\nnon-interactive: pass --all, or name harnesses (agent-local connect claude-code codex …)")
		return nil
	}
	return runConnectTUI()
}

func connectPrintJSON() error {
	statuses, err := DetectHarnesses()
	if err != nil {
		return err
	}
	type row struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Installed  bool   `json:"installed"`
		Configured bool   `json:"configured"`
		Stale      bool   `json:"stale"`
		Path       string `json:"path"`
	}
	rows := make([]row, 0, len(statuses))
	for _, s := range statuses {
		rows = append(rows, row{s.ID, s.Name, s.Installed, s.Configured, s.Stale, s.Path})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rows)
}

func statusLabel(s HarnessStatus) string {
	switch {
	case s.Stale:
		return "configured but stale"
	case s.Configured:
		return "configured"
	case s.Installed:
		return "installed, not configured"
	default:
		return "not installed"
	}
}

func connectPrintList() error {
	statuses, err := DetectHarnesses()
	if err != nil {
		return err
	}
	fmt.Printf("%-16s %-28s %s\n", "HARNESS", "STATUS", "CONFIG")
	for _, s := range statuses {
		fmt.Printf("%-16s %-28s %s\n", s.ID, statusLabel(s), shortHome(s.Path))
	}
	return nil
}

func connectApplyAll(args []string) error {
	statuses, err := DetectHarnesses()
	if err != nil {
		return err
	}
	var targets []HarnessStatus
	for _, s := range statuses {
		if s.Installed {
			targets = append(targets, s)
		}
	}
	if len(targets) == 0 {
		fmt.Println("no installed harnesses found")
		return nil
	}
	if !hasFlag(args, "--yes") && isTerminal(os.Stdin) {
		names := make([]string, len(targets))
		for i, t := range targets {
			names[i] = t.Name
		}
		fmt.Printf("register agent-local in: %s? [y/N] ", strings.Join(names, ", "))
		var resp string
		fmt.Scanln(&resp)
		resp = strings.ToLower(strings.TrimSpace(resp))
		if resp != "y" && resp != "yes" {
			fmt.Println("cancelled")
			return nil
		}
	}
	for _, t := range targets {
		if err := applyAndReport(t.Harness); err != nil {
			return err
		}
	}
	fmt.Println("\nrestart any running harness above to pick up the new server.")
	return nil
}

func connectApplyNamed(ids []string) error {
	statuses, err := DetectHarnesses()
	if err != nil {
		return err
	}
	for _, id := range ids {
		st := findHarness(statuses, id)
		if st == nil {
			return fmt.Errorf("unknown harness %q (agent-local connect --list to see ids)", id)
		}
		if err := applyAndReport(st.Harness); err != nil {
			return err
		}
	}
	fmt.Println("\nrestart any running harness above to pick up the new server.")
	return nil
}

func applyAndReport(h Harness) error {
	wrote, err := ConnectHarness(h)
	if err != nil {
		return fmt.Errorf("%s: %w", h.ID, err)
	}
	if !wrote {
		fmt.Printf("%-16s already configured (%s)\n", h.ID, shortHome(h.Path))
		return nil
	}
	fmt.Printf("%-16s wrote %s\n", h.ID, shortHome(h.Path))
	return nil
}
