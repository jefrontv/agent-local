package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jsonWriteEntry must add our entry without disturbing unrelated keys or an
// editor's existing formatting intent (arbitrary nested values survive).
func TestConnectJSONWriteEntryPreservesUnrelatedKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	initial := `{
  "mcpServers": {
    "other-server": {"command": "/usr/bin/other", "args": ["run"]}
  },
  "unrelatedTopLevel": {"nested": [1, 2, 3], "flag": true}
}`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := jsonWriteEntry(path, "mcpServers", "agent-local", mcpServerEntry("/opt/agent-local")); err != nil {
		t.Fatal(err)
	}

	var got map[string]interface{}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, b)
	}

	unrelated, ok := got["unrelatedTopLevel"].(map[string]interface{})
	if !ok {
		t.Fatal("unrelatedTopLevel was dropped")
	}
	if unrelated["flag"] != true {
		t.Errorf("unrelatedTopLevel.flag = %v, want true", unrelated["flag"])
	}
	nested, _ := unrelated["nested"].([]interface{})
	if len(nested) != 3 {
		t.Errorf("unrelatedTopLevel.nested = %v, want 3 elements", nested)
	}

	servers, ok := got["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatal("mcpServers missing")
	}
	other, ok := servers["other-server"].(map[string]interface{})
	if !ok || other["command"] != "/usr/bin/other" {
		t.Errorf("other-server entry disturbed: %v", other)
	}
	mine, ok := servers["agent-local"].(map[string]interface{})
	if !ok || mine["command"] != "/opt/agent-local" {
		t.Errorf("agent-local entry = %v, want command /opt/agent-local", mine)
	}
}

// A file that does not exist yet must still get a usable config, with the
// parent directory created.
func TestConnectJSONWriteEntryCreatesMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "settings.json")

	if err := jsonWriteEntry(path, "mcpServers", "agent-local", mcpServerEntry("/opt/agent-local")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, b)
	}
	servers := got["mcpServers"].(map[string]interface{})
	if servers["agent-local"] == nil {
		t.Fatal("agent-local entry missing")
	}
}

// The Codex table gets appended when absent, and a second write against the
// same command is a no-op at the entryCommand level (idempotent apply).
func TestConnectTOMLWriteEntryAppendsWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	initial := "profile = \"default\"\n\n[mcp_servers.other]\ncommand = \"/usr/bin/other\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := tomlWriteEntry(path, "mcp_servers.agent-local", "/opt/agent-local"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(b)
	if !strings.Contains(content, "[mcp_servers.other]\ncommand = \"/usr/bin/other\"") {
		t.Errorf("unrelated table disturbed:\n%s", content)
	}
	if !strings.Contains(content, `[mcp_servers.agent-local]`) {
		t.Errorf("agent-local table not appended:\n%s", content)
	}
	if !strings.Contains(content, `command = "/opt/agent-local"`) {
		t.Errorf("command line missing or wrong:\n%s", content)
	}

	cmd, present, err := tomlEntryCommand(path, "mcp_servers.agent-local")
	if err != nil {
		t.Fatal(err)
	}
	if !present || cmd != "/opt/agent-local" {
		t.Errorf("tomlEntryCommand = (%q, %v), want (/opt/agent-local, true)", cmd, present)
	}
}

// Writing the same command again must not touch the file: ConnectHarness
// treats an already-matching entry as nothing to do.
func TestConnectTOMLWriteEntryNoOpWhenPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	initial := "[mcp_servers.agent-local]\ncommand = \"/opt/agent-local\"\nargs = [\"mcp\"]\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	h := Harness{ID: "codex", Path: path, Format: fmtCodexTOML}
	wrote, err := connectHarnessWithBinary(h, "/opt/agent-local")
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Error("expected no-op, ConnectHarness reported a write")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("file changed on a no-op apply:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// A table pointing at a different binary must be reported as stale, and
// re-running the writer updates only the command line.
func TestConnectTOMLStaleDetectionAndUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	initial := "[mcp_servers.agent-local]\ncommand = \"/old/agent-local\"\nargs = [\"mcp\"]\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	h := Harness{ID: "codex", Path: path, Format: fmtCodexTOML}
	st, err := statusFor(h, "/opt/agent-local")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Stale || st.Configured {
		t.Errorf("status = %+v, want stale and not configured", st)
	}

	wrote, err := connectHarnessWithBinary(h, "/opt/agent-local")
	if err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Error("expected a write for a stale entry")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(b)
	if !strings.Contains(content, `command = "/opt/agent-local"`) {
		t.Errorf("command not updated:\n%s", content)
	}
	if !strings.Contains(content, `args = ["mcp"]`) {
		t.Errorf("unrelated args line lost:\n%s", content)
	}
}

// Status computation end-to-end against a fake HOME: nothing installed reads
// as such, and a JSON harness with a matching entry reads as configured.
func TestConnectStatusComputationWithFakeHome(t *testing.T) {
	home := t.TempDir()

	// windsurf has no PATH binary to fall back on, so a fresh home reads as
	// plain "not installed" regardless of what else is on this machine's PATH.
	statuses := detectHarnessesForHome(t, home, "/opt/agent-local")
	windsurf := findHarness(statuses, "windsurf")
	if windsurf == nil {
		t.Fatal("windsurf missing from registry")
	}
	if windsurf.Installed || windsurf.Configured || windsurf.Stale {
		t.Errorf("fresh home: windsurf status = %+v, want all false", *windsurf)
	}

	// Now put a real windsurf config in with a matching entry.
	wsPath := filepath.Join(home, ".codeium", "windsurf", "mcp_config.json")
	if err := jsonWriteEntry(wsPath, "mcpServers", "agent-local", mcpServerEntry("/opt/agent-local")); err != nil {
		t.Fatal(err)
	}
	statuses = detectHarnessesForHome(t, home, "/opt/agent-local")
	windsurf = findHarness(statuses, "windsurf")
	if windsurf == nil || !windsurf.Installed || !windsurf.Configured || windsurf.Stale {
		t.Fatalf("configured home: windsurf status = %+v", windsurf)
	}
}

// detectHarnessesForHome runs the same status logic DetectHarnesses does,
// but against an explicit home and binary rather than $HOME and the test
// binary, so the test is hermetic.
func detectHarnessesForHome(t *testing.T, home, bin string) []HarnessStatus {
	t.Helper()
	var out []HarnessStatus
	for _, h := range harnessRegistry(home) {
		st, err := statusFor(h, bin)
		if err != nil {
			t.Fatalf("%s: %v", h.ID, err)
		}
		out = append(out, st)
	}
	return out
}

// Removal is the mirror of registration: only our entry goes, siblings and
// unrelated keys stay, and a TOML file keeps its trailing newline.
func TestConnectRemoveLeavesSiblingsIntact(t *testing.T) {
	dir := t.TempDir()

	jsonPath := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(jsonPath, []byte(`{"$schema":"x","mcp":{"other":{"type":"local","command":["x"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	oc := Harness{ID: "opencode", Path: jsonPath, Format: fmtOpenCode}
	if _, err := connectHarnessWithBinary(oc, "/opt/agent-local"); err != nil {
		t.Fatal(err)
	}
	if cmd, present, _ := entryCommand(oc); !present || cmd != "/opt/agent-local" {
		t.Fatalf("opencode entry not readable back as argv[0]: %q %v", cmd, present)
	}
	removed, err := DisconnectHarness(oc)
	if err != nil || !removed {
		t.Fatalf("remove: %v %v", removed, err)
	}
	var got map[string]interface{}
	b, _ := os.ReadFile(jsonPath)
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	mcp := got["mcp"].(map[string]interface{})
	if _, still := mcp["agent-local"]; still {
		t.Fatal("agent-local still present after remove")
	}
	if _, ok := mcp["other"]; !ok || got["$schema"] != "x" {
		t.Fatalf("siblings disturbed: %s", b)
	}
	if removed, _ := DisconnectHarness(oc); removed {
		t.Fatal("second remove reported a change")
	}

	tomlPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(tomlPath, []byte("[model]\nname = \"o3\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cx := Harness{ID: "codex", Path: tomlPath, Format: fmtCodexTOML}
	if _, err := connectHarnessWithBinary(cx, "/opt/agent-local"); err != nil {
		t.Fatal(err)
	}
	if _, err := DisconnectHarness(cx); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(tomlPath)
	if string(after) != "[model]\nname = \"o3\"\n" {
		t.Fatalf("toml not restored byte-for-byte: %q", after)
	}
}
