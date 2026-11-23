package main

import "testing"

// Every command through the real dispatcher: 0 when it worked, 1 when it
// failed or lacked an argument, 2 for a command that does not exist. Only
// commands that stay inside the temp HOME and the fake database are run.
func TestRunCLIExitCodes(t *testing.T) {
	e, site := cliFixture(t)
	site.DBName, site.DBUser = "al_demo", "al_demo"
	e.Store.PutSite(site)
	withFakeDB(t, e)
	ok := [][]string{
		{"help"}, {"--help"}, {"version"}, {"list"}, {"ls", "--json"}, {"sites-dir"}, {"suffix"}, {"idle"}, {"autoupdate"},
		{"api-token"}, {"jobs"}, {"media", "demo"}, {"wpdebug", "demo"}, {"wpconst", "demo"}, {"errors", "demo"},
		{"checkpoint", "demo", "--list"}, {"mail", "demo"}, {"worktrees", "demo"}, {"cert", "demo.test"},
		{"resolve", site.WPDir}, {"db", "demo"}, {"mcp", "--config"}, {"connect", "--list"},
	}
	failed := [][]string{
		{"job"}, {"logs", "daemon"}, {"verify-release"}, {"start"}, {"stop"}, {"restart"}, {"delete"}, {"open"},
		{"domain"}, {"move"}, {"worktree"}, {"branches", "demo"}, {"wp"}, {"create"}, {"attach"}, {"import"},
		{"localwp-sites"}, {"probe"}, {"rollback"}, {"login"}, {"wpinfo"}, {"share"}, {"wpdebug"}, {"mail"}, {"errors"},
	}
	check := func(args []string, want int) {
		t.Helper()
		var code int
		captureStdout(t, func() error { code = runCLI(args); return nil })
		if code != want {
			t.Errorf("%v: exit %d, want %d", args, code, want)
		}
	}
	for _, args := range ok {
		check(args, 0)
	}
	for _, args := range failed {
		check(args, 1)
	}
	check([]string{"no-such-command"}, 2)
}
