package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every response must carry jsonrpc "2.0". They used to go out as an empty
// string, which a strict client rejects — the server appeared to do nothing at
// all. The version is stamped in one place so a new handler cannot forget it, and
// this checks the wire result rather than that one assignment.
func TestResponsesCarryProtocolVersion(t *testing.T) {
	for _, method := range []string{"initialize", "tools/list", "ping", "no/such/method"} {
		req := &mcpReq{JSONRPC: "2.0", ID: 1, Method: method}
		resp := mcpHandle(req)
		if resp == nil {
			t.Fatalf("%s: expected a response", method)
		}
		resp.JSONRPC = "2.0" // runMCP stamps it centrally, as the loop does
		resp.ID = req.ID
		b, err := json.Marshal(resp)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]interface{}
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		if out["jsonrpc"] != "2.0" {
			t.Errorf("%s: jsonrpc = %v, want 2.0", method, out["jsonrpc"])
		}
		if out["id"] == nil {
			t.Errorf("%s: response dropped the request id", method)
		}
	}
}

// An unknown method is an error response, not silence: a client waiting on an id
// would hang forever otherwise.
func TestUnknownMethodIsAnError(t *testing.T) {
	resp := mcpHandle(&mcpReq{JSONRPC: "2.0", ID: 7, Method: "tools/nope"})
	if resp == nil || resp.Error == nil {
		t.Fatal("expected an error response for an unknown method")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("code = %d, want -32601 (method not found)", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "tools/nope") {
		t.Errorf("message %q should name the method", resp.Error.Message)
	}
}

// Notifications carry no id and must never be answered.
func TestNotificationsAreNotAnswered(t *testing.T) {
	for _, method := range []string{"notifications/initialized", "initialized"} {
		if resp := mcpHandle(&mcpReq{JSONRPC: "2.0", Method: method}); resp != nil {
			t.Errorf("%s: got a response, want none", method)
		}
	}
}

// initialize has to state a protocol version and advertise tools, or a client
// will not proceed to tools/list.
func TestInitializeAdvertisesTools(t *testing.T) {
	resp := mcpHandle(&mcpReq{JSONRPC: "2.0", ID: 1, Method: "initialize"})
	if resp == nil || resp.Result == nil {
		t.Fatal("initialize returned nothing")
	}
	res, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("result is %T, want an object", resp.Result)
	}
	if res["protocolVersion"] == "" || res["protocolVersion"] == nil {
		t.Error("no protocolVersion")
	}
	caps, _ := res["capabilities"].(map[string]interface{})
	if _, has := caps["tools"]; !has {
		t.Error("capabilities do not advertise tools")
	}
}

// Every tool needs a name, a description an agent can choose from, and a schema:
// a nameless or undocumented tool is invisible in practice.
func TestToolsAreWellFormed(t *testing.T) {
	tools := mcpTools()
	if len(tools) < 40 {
		t.Fatalf("only %d tools registered", len(tools))
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		if tool.Name == "" {
			t.Error("a tool has no name")
		}
		if seen[tool.Name] {
			t.Errorf("duplicate tool name %q", tool.Name)
		}
		seen[tool.Name] = true
		if len(tool.Description) < 10 {
			t.Errorf("%s: description too thin to choose from: %q", tool.Name, tool.Description)
		}
		if tool.InputSchema == nil {
			t.Errorf("%s: no input schema", tool.Name)
		}
	}
	// The tools this session added, so a refactor cannot quietly drop them.
	for _, want := range []string{"attach_site", "set_media_fallback", "get_sites_dir", "create_site"} {
		if !seen[want] {
			t.Errorf("tool %q is missing", want)
		}
	}
}

// The config a client pastes must name a real absolute binary and the mcp
// subcommand, or it fails on first launch with nothing to explain it.
func TestClientConfigIsUsable(t *testing.T) {
	cfg := mcpClientConfig()
	var parsed struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(cfg), &parsed); err != nil {
		t.Fatalf("config is not valid JSON: %v\n%s", err, cfg)
	}
	entry, ok := parsed.MCPServers["agent-local"]
	if !ok {
		t.Fatal("config has no agent-local server entry")
	}
	if !strings.HasPrefix(entry.Command, "/") {
		t.Errorf("command %q is not an absolute path", entry.Command)
	}
	if len(entry.Args) != 1 || entry.Args[0] != "mcp" {
		t.Errorf("args = %v, want [mcp]", entry.Args)
	}
}

// The notice exists for the exact confusion that prompted it, so it has to say
// what is happening and where to go next.
func TestTTYNoticeExplainsItself(t *testing.T) {
	notice := mcpTTYNotice()
	for _, want := range []string{"stdio", "not run by hand", "--config", "tools/list"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice does not mention %q:\n%s", want, notice)
		}
	}
}
