package main

import (
	"os"
	"testing"
)

// TestMain isolates the suite from the machine it runs on. Tests redirect
// $HOME per test, but AGENT_LOCAL_HOME wins over $HOME (model.go), so a shell
// that exports it would send every test write into that real directory; and
// nothing a test boots may touch the login session's launchd agent.
func TestMain(m *testing.M) {
	os.Unsetenv("AGENT_LOCAL_HOME")
	autostartDisabled = true
	os.Exit(m.Run())
}
