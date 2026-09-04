package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Every way a new build lands must read as a change: install.sh and
// SelfUpdate rename a staged file over the old one, Homebrew retargets its
// bin symlink at a new version directory, and a careless cp overwrites in
// place. A file nobody touched must not.
func TestBinaryIdentitySeesEveryKindOfSwap(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "agent-local")
	os.WriteFile(bin, []byte("build one"), 0o755)
	start, err := identify(bin)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := identify(bin); again != start {
		t.Fatal("an untouched file changed identity")
	}

	// Rename-swap: new inode.
	staged := bin + ".new"
	os.WriteFile(staged, []byte("build two"), 0o755)
	os.Rename(staged, bin)
	swapped, _ := identify(bin)
	if swapped == start {
		t.Error("rename-swap not seen")
	}

	// In-place overwrite: same inode, different size/mtime.
	os.WriteFile(bin, []byte("build three, longer"), 0o755)
	overwritten, _ := identify(bin)
	if overwritten == swapped {
		t.Error("in-place overwrite not seen")
	}

	// Symlink retarget, the cask's shape: the link stays, what it points at moves.
	v1, v2 := filepath.Join(dir, "1.0", "agent-local"), filepath.Join(dir, "2.0", "agent-local")
	os.MkdirAll(filepath.Dir(v1), 0o755)
	os.MkdirAll(filepath.Dir(v2), 0o755)
	os.WriteFile(v1, []byte("one"), 0o755)
	os.WriteFile(v2, []byte("two"), 0o755)
	link := filepath.Join(dir, "link")
	os.Symlink(v1, link)
	before, _ := identify(link)
	os.Remove(link)
	os.Symlink(v2, link)
	after, _ := identify(link)
	if before == after {
		t.Error("symlink retarget not seen: identify must follow the link")
	}
}

// The watcher must ignore a file that does not run yet - a copy in progress -
// and fire once, with the version the new binary reports.
func TestWatchBinaryWaitsForARunnableBuild(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "agent-local")
	script := func(version string) string {
		return "#!/bin/sh\necho 'agent-local " + version + "'\n"
	}
	os.WriteFile(bin, []byte(script("0.1.0")), 0o755)

	got := make(chan string, 1)
	go watchBinary(bin, 20*time.Millisecond, func(v string) { got <- v })

	// A half-written replacement: not executable, must not fire.
	time.Sleep(60 * time.Millisecond)
	os.WriteFile(bin+".new", []byte("garbage"), 0o644)
	os.Rename(bin+".new", bin)
	select {
	case v := <-got:
		t.Fatalf("fired on a build that does not run: %q", v)
	case <-time.After(150 * time.Millisecond):
	}

	// The real one lands.
	os.WriteFile(bin+".new", []byte(script("0.2.0")), 0o755)
	os.Rename(bin+".new", bin)
	select {
	case v := <-got:
		if v != "0.2.0" {
			t.Errorf("version = %q, want 0.2.0", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher never fired for a runnable replacement")
	}
}

// The cache is what every process reads instead of GitHub. A dev build has
// no release to be behind; an empty cache has nothing to say; a matching tag
// is not an update.
func TestNewerThanRunning(t *testing.T) {
	c := &updateCache{Latest: "v0.26.0", CheckedAt: time.Now()}
	for _, tc := range []struct {
		cache   *updateCache
		running string
		want    string
	}{
		{c, "0.25.0", "v0.26.0"},
		{c, "v0.25.0", "v0.26.0"},
		{c, "0.9.9", "v0.26.0"}, // numeric, not lexical
		{c, "0.26.0", ""},
		{c, "0.27.0", ""}, // ahead of what is published is not behind
		{c, "dev", ""},
		{c, "", ""},
		{nil, "0.25.0", ""},
	} {
		if got := newerThanRunning(tc.cache, tc.running); got != tc.want {
			t.Errorf("newerThanRunning(%v, %q) = %q, want %q", tc.cache, tc.running, got, tc.want)
		}
	}
}

func TestUpdateCacheRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	os.MkdirAll(P().Root, 0o755)
	if readUpdateCache() != nil {
		t.Fatal("a missing cache should read as nil")
	}
	rel := &ghRelease{TagName: "v0.26.0", HTMLURL: "https://example.test/r"}
	if err := writeUpdateCache(rel); err != nil {
		t.Fatal(err)
	}
	c := readUpdateCache()
	if c == nil || c.Latest != "v0.26.0" || c.URL != rel.HTMLURL || time.Since(c.CheckedAt) > time.Minute {
		t.Errorf("cache = %+v", c)
	}
}

// The doctor lines: a Homebrew binary gets brew's command, ours gets the
// auto-fixable one; a daemon on the same build is not a finding.
func TestUpdateAndDaemonFindings(t *testing.T) {
	f := updateFinding("0.25.0", "v0.26.0", false)
	if f.Check != "update" || f.Status != "warn" || !f.AutoFix || f.FixCmd != "agent-local update" {
		t.Errorf("our binary: %+v", f)
	}
	if f.Detail != "0.26.0 available (running 0.25.0)" {
		t.Errorf("detail = %q", f.Detail)
	}
	f = updateFinding("0.25.0", "v0.26.0", true)
	if f.AutoFix || f.FixCmd != "" || f.FixHint != "brew upgrade agent-local" {
		t.Errorf("homebrew binary: %+v", f)
	}

	if daemonFinding("0.26.0", "0.26.0") != nil {
		t.Error("same build is not a finding")
	}
	if daemonFinding("0.26.0", "") != nil {
		t.Error("no daemon answer is not a finding")
	}
	d := daemonFinding("0.26.0", "0.25.0")
	if d == nil || d.Status != "warn" || d.FixCmd != "agent-local restart-daemon" || d.AutoFix {
		t.Errorf("stale daemon: %+v", d)
	}
}

// Homebrew's stable name is the bin link; anything not in a Caskroom has none.
func TestHomebrewLink(t *testing.T) {
	// macOS temp dirs live under /var -> /private/var; compare resolved.
	prefix, _ := filepath.EvalSymlinks(t.TempDir())
	real := filepath.Join(prefix, "Caskroom", "agent-local", "0.25.0", "agent-local")
	os.MkdirAll(filepath.Dir(real), 0o755)
	os.WriteFile(real, []byte("x"), 0o755)
	os.MkdirAll(filepath.Join(prefix, "bin"), 0o755)
	link := filepath.Join(prefix, "bin", AppName)
	os.Symlink(real, link)

	if got := homebrewLink(real); got != link {
		t.Errorf("homebrewLink(real) = %q, want %q", got, link)
	}
	if got := homebrewLink(link); got != link {
		t.Errorf("homebrewLink(link) = %q, want %q", got, link)
	}
	if got := homebrewLink(filepath.Join(prefix, "elsewhere", "agent-local")); got != "" {
		t.Errorf("non-cask path gave %q", got)
	}
}

// A daemon told its binary changed must not step down under a running job: an
// import cut off mid-write is worse than a late update. And outside launchd
// there is nobody to bring the new build up, so it only says so.
func TestDaemonHandoverWaitsForJobsAndNeedsLaunchd(t *testing.T) {
	old := handoverPoll
	handoverPoll = 10 * time.Millisecond
	t.Cleanup(func() { handoverPoll = old })

	t.Setenv(launchdMarker, "1")
	jobs := NewJobHub()
	release := make(chan struct{})
	jobs.Start("import", func(func(string, string)) (any, error) {
		<-release
		return nil, nil
	})
	hand := make(chan string, 1)
	go daemonHandover(jobs, hand)("0.26.0")
	select {
	case v := <-hand:
		t.Fatalf("handed over to %s while a job was running", v)
	case <-time.After(80 * time.Millisecond):
	}
	close(release)
	select {
	case v := <-hand:
		if v != "0.26.0" {
			t.Errorf("handed over to %q", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("never handed over after the job finished")
	}
	if got := installedVersion.Load(); got != "0.26.0" {
		t.Errorf("installed version not recorded: %v", got)
	}

	// Not under launchd: recorded, logged, no handover.
	t.Setenv(launchdMarker, "")
	hand = make(chan string, 1)
	daemonHandover(NewJobHub(), hand)("0.27.0")
	select {
	case v := <-hand:
		t.Fatalf("handed over to %s with nothing to respawn it", v)
	default:
	}
	if got := installedVersion.Load(); got != "0.27.0" {
		t.Errorf("installed version not recorded outside launchd: %v", got)
	}
}
