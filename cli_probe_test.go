package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Read-mostly commands against the fixture site. Each case names the error it
// expects ("" for success) and a line its output must carry.
func TestCLIReadCommands(t *testing.T) {
	_, site := cliFixture(t)
	if err := os.MkdirAll(filepath.Dir(P().Log("daemon")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(P().Log("daemon"), []byte("first line\nsecond line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, wantErr, wantOut string
		fn                     func() error
	}{
		{"errors", "", "no PHP errors in the last 1h0m0s", func() error { return cmdErrors([]string{"demo"}) }},
		{"errors since", "", "last 2h0m0s", func() error { return cmdErrors([]string{"demo", "--since", "2h"}) }},
		{"errors bad since", "invalid duration", "", func() error { return cmdErrors([]string{"demo", "--since", "soon"}) }},
		{"wpconst list", "", "WP_DEBUG = false", func() error { return cmdWPConst([]string{"demo"}) }},
		{"wpconst set", "", "AL_TEST = true", func() error { return cmdWPConst([]string{"demo", "AL_TEST", "true"}) }},
		{"wpconst remove", "", "removed AL_TEST", func() error { return cmdWPConst([]string{"demo", "AL_TEST", "--remove"}) }},
		{"wpdebug read", "", "off", func() error { return cmdWPDebug([]string{"demo"}) }},
		{"wpdebug on", "", "wp-demo.log", func() error { return cmdWPDebug([]string{"demo", "on"}) }},
		{"wpdebug off", "", "off", func() error { return cmdWPDebug([]string{"demo", "off"}) }},
		{"wpdebug bad", "usage", "", func() error { return cmdWPDebug([]string{"demo", "loud"}) }},
		{"checkpoint list", "", "none yet", func() error { return cmdCheckpoint([]string{"demo", "--list"}) }},
		{"mail list", "", "nothing captured yet", func() error { return cmdMail([]string{"demo"}) }},
		{"mail clear", "", "cleared 0", func() error { return cmdMail([]string{"demo", "--clear"}) }},
		{"worktrees", "", "no branch previews", func() error { return cmdWorktrees([]string{"demo"}) }},
		{"resolve", "", "demo", func() error { return cmdResolve([]string{site.WPDir}) }},
		{"resolve none", "no site manages", "", func() error { return cmdResolve([]string{t.TempDir()}) }},
		{"logs daemon", "", "second line", func() error { return cmdLogs([]string{"daemon"}) }},
		{"logs bad", "no log named", "", func() error { return cmdLogs([]string{"nothing-here"}) }},
		{"jobs", "", "no jobs yet", func() error { return cmdJobs() }},
		{"job missing", "no such job", "", func() error { return cmdJob([]string{"abc"}) }},
		{"localwp", "sites.json not found", "", func() error { return cmdLocalWPSites() }},
		{"cert", "", "missing", func() error { return cmdCert([]string{"demo.test"}) }},
		{"domain bad", "invalid domain", "", func() error { return cmdDomain([]string{"demo", "-bad"}) }},
		{"rollback missing", "no checkpoint", "", func() error { return cmdRollback([]string{"demo", "nope"}) }},
		{"branches", "not a git repo", "", func() error { return cmdBranches([]string{"demo"}) }},
		{"worktree remove missing", "no such worktree", "", func() error { return cmdWorktree([]string{"demo", "nope", "--remove"}) }},
		{"import missing", "not found", "", func() error { return cmdImport([]string{filepath.Join(t.TempDir(), "gone")}) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := captureStdout(t, c.fn)
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v\n%s", err, out)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			case !strings.Contains(out, c.wantOut):
				t.Fatalf("want %q in output:\n%s", c.wantOut, out)
			}
		})
	}
}

func TestCLIConstantsLandInWPConfig(t *testing.T) {
	_, site := cliFixture(t)
	if _, err := captureStdout(t, func() error { return cmdWPConst([]string{"demo", "AL_FLAG", "42"}) }); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(site.WPDir, "wp-config.php"))
	if err != nil || !strings.Contains(string(b), "AL_FLAG") {
		t.Fatalf("constant not written: %v\n%s", err, b)
	}
	if !strings.Contains(string(b), "stop editing") {
		t.Fatal("wp-config lost its marker")
	}
}

func TestHelpListsEveryGroup(t *testing.T) {
	out := renderHelp()
	for _, g := range helpGroups {
		if !strings.Contains(out, strings.ToUpper(g.title)) {
			t.Errorf("help is missing %s", g.title)
		}
	}
	if _, err := captureStdout(t, func() error { printHelp(); return nil }); err != nil {
		t.Fatal(err)
	}
}
