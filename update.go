package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Self-update: ask GitHub for the newest release, verify it against the
// published checksum, and swap the running binary for it.
//
// The swap is a rename, never a write-in-place: macOS caches a code signature
// per inode, so overwriting the bytes of a running binary earns a SIGKILL on
// the next exec. Same reason install.sh stages and renames.

const (
	updateRepo    = "jefrontv/agent-local"
	updateAssetOS = "darwin_universal"
	// codesignIdentifier is the one name every build is signed under. The
	// release pipeline and install.sh use the same string; TCC consent for
	// Documents/Desktop/Downloads follows it from build to build.
	codesignIdentifier = "local.agent-local"
)

type ghRelease struct {
	TagName    string    `json:"tag_name"`
	Name       string    `json:"name"`
	HTMLURL    string    `json:"html_url"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Published  time.Time `json:"published_at"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// LatestRelease fetches the newest published release. Unauthenticated: the
// repo is public, and GitHub's anonymous rate limit is far above what a version
// check needs.
func LatestRelease() (*ghRelease, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", "https://api.github.com/repos/"+updateRepo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", AppName+"/"+Version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reach github: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("no published release yet for %s", updateRepo)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("github returned %s", resp.Status)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// UpdateAvailable compares the running build to a release tag. A "dev" build
// (plain `go build`) is never considered up to date: the whole point of asking
// is to find out what is published.
func UpdateAvailable(current string, rel *ghRelease) bool {
	return strings.TrimPrefix(rel.TagName, "v") != strings.TrimPrefix(current, "v")
}

// managedByHomebrew reports whether this binary belongs to a Homebrew cask, in
// which case `brew upgrade` owns the update and writing over it would leave
// Homebrew's metadata lying about what is installed.
func managedByHomebrew(path string) bool {
	real := path
	if r, err := filepath.EvalSymlinks(path); err == nil {
		real = r
	}
	return strings.Contains(real, "/Caskroom/") || strings.Contains(real, "/Cellar/")
}

// homebrewLink is the stable name Homebrew gives a cask binary - <prefix>/bin/
// agent-local - when path is one, else "". The versioned file it points at is
// replaced on upgrade; the link is what stays put.
func homebrewLink(path string) string {
	real := path
	if r, err := filepath.EvalSymlinks(path); err == nil {
		real = r
	}
	prefix, _, ok := strings.Cut(real, "/Caskroom/")
	if !ok {
		return ""
	}
	link := filepath.Join(prefix, "bin", AppName)
	if !fileExists(link) {
		return ""
	}
	return link
}

// watchedBinaryPath is the file whose replacement means "a new build is
// installed": the same path autostart runs, so the two can never disagree
// about which binary counts.
func watchedBinaryPath() string {
	if p, err := installedBinaryPath(); err == nil {
		return p
	}
	self, _ := os.Executable()
	return self
}

// installedVersion is what the binary on disk reports once the watcher has
// seen it change. Unset, the running build is the installed one.
var installedVersion atomic.Value

// binaryWatchEvery is how often the daemon stats its own executable. One
// syscall; ten seconds is the most a user waits after brew upgrade before the
// daemon is on the new build.
const binaryWatchEvery = 10 * time.Second

// handoverPoll is how often a daemon waiting to hand over re-checks for
// running jobs. A variable so a test need not wait seconds per check.
var handoverPoll = 5 * time.Second

// sitesInProtectedFolders lists sites whose docroot sits where macOS gates
// access per app - Documents, Desktop, Downloads. Every new build asks again
// before it may read those, which is what a user should know before letting
// the daemon install builds unattended.
func sitesInProtectedFolders(store *Store) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range store.Sites() {
		for _, dir := range []string{"Documents", "Desktop", "Downloads"} {
			if strings.HasPrefix(s.WPDir, filepath.Join(home, dir)+string(os.PathSeparator)) {
				out = append(out, s.Slug)
				break
			}
		}
	}
	return out
}

// daemonHandover is what the daemon does when its binary is replaced: wait for
// running jobs (an import cut off mid-write is worse than a late update), then
// ask the main goroutine to shut down through hand. Under launchd only - a
// daemon nobody would restart logs the fact and leaves it to `restart-daemon`.
func daemonHandover(jobs *JobHub, hand chan<- string) func(string) {
	return func(v string) {
		installedVersion.Store(v)
		if os.Getenv(launchdMarker) == "" {
			log.Printf("binary on disk is now %s (running %s): run `%s restart-daemon` to pick it up", v, Version, AppName)
			return
		}
		waited := false
		for jobs.anyRunning() {
			if !waited {
				log.Printf("binary on disk is now %s (running %s): handing over once jobs finish", v, Version)
				waited = true
			}
			time.Sleep(handoverPoll)
		}
		hand <- v
	}
}

// relaunchViaLaunchd is the last thing a handing-over daemon does. The agent's
// KeepAlive is failure-only, so the exit code says "failed" for launchd's sake;
// kickstart asks for the restart right away instead of after launchd's
// crash-respawn throttle. Never returns.
func relaunchViaLaunchd() {
	label := fmt.Sprintf("gui/%d/%s", os.Getuid(), daemonAgentLabel)
	_ = exec.Command("launchctl", "kickstart", "-k", label).Start()
	os.Exit(3)
}

// updateLoop is the daemon's daily look at GitHub, and the install when
// auto-update is on. It wakes hourly, asks GitHub only once the cache is a day
// old, and reads the setting each time so a toggle needs no restart. A dev
// build has no release to be behind and never asks.
func updateLoop(store *Store) {
	if Version == "dev" {
		return
	}
	// Not at boot: a restart must never wait on the network.
	time.Sleep(time.Minute)
	for {
		c := readUpdateCache()
		if c == nil || time.Since(c.CheckedAt) > updateCheckEvery {
			fresh, err := checkForUpdate()
			if err != nil {
				log.Printf("update check: %v", err)
			} else {
				c = fresh
			}
		}
		if tag := newerThanRunning(c, Version); tag != "" {
			store.ReloadIfChanged()
			self, _ := os.Executable()
			switch {
			case !store.Data.AutoUpdate:
				log.Printf("%s is available (running %s): %s update", tag, Version, AppName)
			case managedByHomebrew(self):
				log.Printf("%s is available; this binary is Homebrew's: brew upgrade %s", tag, AppName)
			default:
				log.Printf("auto-update: installing %s", tag)
				if v, err := SelfUpdate(func(stage string) { log.Printf("auto-update: %s", stage) }); err != nil {
					log.Printf("auto-update: %v", err)
				} else {
					log.Printf("auto-update: %s installed", v)
				}
				// The binary watcher sees the swap and hands over from here.
			}
		}
		time.Sleep(time.Hour)
	}
}

// ---------- Noticing a new binary ----------

// binaryIdentity is what tells one build of the executable from the next
// without reading it: the inode changes on a rename-swap or a cask's version
// directory flip, size and mtime on an in-place overwrite.
type binaryIdentity struct {
	ino   uint64
	size  int64
	mtime time.Time
}

// identify stats the executable with symlinks followed, so what is compared is
// the file that would actually run.
func identify(path string) (binaryIdentity, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return binaryIdentity{}, err
	}
	id := binaryIdentity{size: fi.Size(), mtime: fi.ModTime()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		id.ino = st.Ino
	}
	return id, nil
}

// binaryVersion asks a binary what it is. "" when it does not run, which is
// the answer a half-written or wrong-architecture file gives.
func binaryVersion(path string) string {
	out, err := exec.Command(path, "version").Output()
	if err != nil {
		return ""
	}
	// `version` prints "agent-local X" as its title line.
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	fields := strings.Fields(ansiEscapeRe.ReplaceAllString(first, ""))
	if len(fields) < 2 {
		return ""
	}
	return fields[len(fields)-1]
}

// watchBinary calls onChange(version) once the executable at path is replaced by
// one that runs. A stat every interval is the whole cost. It returns only when
// onChange does, or never: a daemon that has been told its binary changed has
// nothing more to learn from watching.
func watchBinary(path string, interval time.Duration, onChange func(version string)) {
	start, err := identify(path)
	if err != nil {
		return
	}
	for {
		time.Sleep(interval)
		now, err := identify(path)
		if err != nil || now == start {
			continue // gone or unchanged: nothing to hand over to
		}
		v := binaryVersion(path)
		if v == "" {
			continue // not runnable yet - a copy in progress, or a broken build
		}
		onChange(v)
		return
	}
}

// ---------- Knowing a release is out ----------

// updateCache is what the daemon's daily check learned, shared with every
// other process through a file so no CLI command ever waits on GitHub.
type updateCache struct {
	Latest    string    `json:"latest"` // release tag, e.g. v0.25.1
	URL       string    `json:"url"`
	CheckedAt time.Time `json:"checked_at"`
}

// updateCheckEvery is how often the daemon asks GitHub. A release a day is
// already more than this project ships; a stale answer just reads as
// "nothing new" until the next check.
const updateCheckEvery = 24 * time.Hour

// updateCacheStale is when a process without a daemon to rely on should go
// and look itself. An hour past the check interval, so a daemon that is a
// little late never races a CLI to the same request.
const updateCacheStale = updateCheckEvery + time.Hour

func updateCachePath() string { return filepath.Join(P().Root, "update.json") }

func readUpdateCache() *updateCache {
	b, err := os.ReadFile(updateCachePath())
	if err != nil {
		return nil
	}
	var c updateCache
	if json.Unmarshal(b, &c) != nil || c.Latest == "" {
		return nil
	}
	return &c
}

func writeUpdateCache(rel *ghRelease) error {
	b, err := json.Marshal(updateCache{Latest: rel.TagName, URL: rel.HTMLURL, CheckedAt: time.Now()})
	if err != nil {
		return err
	}
	tmp := updateCachePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, updateCachePath())
}

// checkForUpdate asks GitHub and records the answer for every other process.
func checkForUpdate() (*updateCache, error) {
	rel, err := LatestRelease()
	if err != nil {
		return nil, err
	}
	if err := writeUpdateCache(rel); err != nil {
		return nil, err
	}
	return readUpdateCache(), nil
}

// newerThanRunning is the release the cache knows about when it is ahead of
// the build in memory, else "". A dev build has no release to be behind, an
// empty cache has nothing to say, and a build ahead of what is published (a
// release candidate, a yanked tag) is not behind anything.
func newerThanRunning(c *updateCache, running string) string {
	if c == nil || running == "dev" || running == "" {
		return ""
	}
	if !verLess(strings.TrimPrefix(running, "v"), strings.TrimPrefix(c.Latest, "v")) {
		return ""
	}
	return c.Latest
}

// availableUpdate is the release a user could install right now, from the
// cache alone. "" is also the answer when nobody has checked yet.
func availableUpdate() (tag, url string) {
	c := readUpdateCache()
	if tag = newerThanRunning(c, Version); tag == "" {
		return "", ""
	}
	return tag, c.URL
}

// availableUpdateFresh is availableUpdate for the one caller allowed to wait:
// doctor goes and looks when the cache is older than a daemon would leave it.
func availableUpdateFresh(timeout time.Duration) (tag, url string) {
	if Version == "dev" {
		return "", ""
	}
	c := readUpdateCache()
	if c == nil || time.Since(c.CheckedAt) > updateCacheStale {
		done := make(chan *updateCache, 1)
		go func() {
			fresh, _ := checkForUpdate()
			done <- fresh
		}()
		select {
		case fresh := <-done:
			if fresh != nil {
				c = fresh
			}
		case <-time.After(timeout):
		}
	}
	if tag = newerThanRunning(c, Version); tag == "" {
		return "", ""
	}
	return tag, c.URL
}

// updateFinding is the doctor line for a release being out. Homebrew owns its
// own binaries, so there the fix is brew's command rather than ours.
func updateFinding(running, latest string, brewManaged bool) Finding {
	f := Finding{Check: "update", Status: "warn",
		Detail: strings.TrimPrefix(latest, "v") + " available (running " + running + ")"}
	if brewManaged {
		f.FixHint = "brew upgrade " + AppName
	} else {
		f.FixCmd, f.AutoFix = AppName+" update", true
	}
	return f
}

// daemonFinding is the doctor line for a daemon still running an older build
// than the binary that is asking. The daemon replaces itself within seconds
// under launchd; seeing this means it is not under launchd, or is waiting on
// a job to finish - either way the command that finishes it is the same.
func daemonFinding(cli, daemon string) *Finding {
	if daemon == "" || daemon == cli {
		return nil
	}
	return &Finding{Check: "daemon", Status: "warn",
		Detail: "running " + daemon + "; this binary is " + cli,
		FixCmd: AppName + " restart-daemon"}
}

// SelfUpdate downloads the latest release and replaces the running binary.
// Returns the version installed.
func SelfUpdate(progress func(string)) (string, error) {
	if progress == nil {
		progress = func(string) {}
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	if managedByHomebrew(self) {
		return "", fmt.Errorf("installed by Homebrew — update with: brew upgrade agent-local")
	}
	rel, err := LatestRelease()
	if err != nil {
		return "", err
	}
	if !UpdateAvailable(Version, rel) {
		return rel.TagName, nil
	}
	var assetURL, assetName, sumsURL string
	for _, a := range rel.Assets {
		switch {
		case strings.Contains(a.Name, updateAssetOS) && strings.HasSuffix(a.Name, ".tar.gz"):
			assetURL, assetName = a.URL, a.Name
		case a.Name == "checksums.txt":
			sumsURL = a.URL
		}
	}
	if assetURL == "" {
		return "", fmt.Errorf("release %s has no %s archive", rel.TagName, updateAssetOS)
	}

	progress("downloading " + rel.TagName)
	archive, err := downloadTemp(assetURL, "agent-local-update-*.tar.gz")
	if err != nil {
		return "", err
	}
	defer os.Remove(archive)

	// Verify before unpacking: a truncated or tampered download must never
	// reach the point where it can replace a working binary.
	if sumsURL == "" {
		return "", fmt.Errorf("release %s publishes no checksums.txt; refusing to install unverified bytes", rel.TagName)
	}
	progress("verifying checksum")
	if err := verifyChecksum(archive, assetName, sumsURL); err != nil {
		return "", err
	}

	progress("unpacking")
	staged := self + ".new"
	if err := extractBinary(archive, "agent-local", staged); err != nil {
		return "", err
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		os.Remove(staged)
		return "", err
	}
	// Re-sign: the archive is ad-hoc signed already, but a copy that lost its
	// signature (or an arch-thinned one) would be killed on first exec. With the
	// fixed identifier, because macOS keys Documents/Desktop consent on it: the
	// grant the previous build earned carries over instead of being asked again.
	_ = exec.Command("codesign", "-f", "-s", "-", "-i", codesignIdentifier, staged).Run()
	// Clear the quarantine flag a browser or curl download can attach.
	_ = exec.Command("xattr", "-d", "com.apple.quarantine", staged).Run()

	if out, err := exec.Command(staged, "version").CombinedOutput(); err != nil {
		os.Remove(staged)
		return "", fmt.Errorf("downloaded binary does not run: %v %s", err, tail(string(out), 200))
	}
	progress("installing")
	if err := os.Rename(staged, self); err != nil {
		os.Remove(staged)
		return "", fmt.Errorf("replace %s: %w (try: sudo agent-local update)", self, err)
	}
	return rel.TagName, nil
}

// maxDownloadSize bounds any file this process pulls over the network — the
// release archive and checksums.txt both fit comfortably inside it, and it
// matches extractBinary's own cap on what it will unpack from the archive.
const maxDownloadSize = 200 << 20

func downloadTemp(url, pattern string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Minute}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", AppName+"/"+Version)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download %s: %s", url, resp.Status)
	}
	if resp.ContentLength > maxDownloadSize {
		return "", fmt.Errorf("download %s: %d bytes exceeds the %d byte limit", url, resp.ContentLength, int64(maxDownloadSize))
	}
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxDownloadSize+1))
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if n > maxDownloadSize {
		os.Remove(f.Name())
		return "", fmt.Errorf("download %s: exceeds the %d byte limit", url, int64(maxDownloadSize))
	}
	return f.Name(), nil
}

// verifyChecksum matches the archive against checksums.txt, binding the
// digest to assetName — the exact release asset this download was meant to
// be — instead of accepting any line in the file that happens to carry a
// matching hash.
func verifyChecksum(archivePath, assetName, sumsURL string) error {
	sums, err := downloadTemp(sumsURL, "agent-local-sums-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(sums)
	b, err := os.ReadFile(sums)
	if err != nil {
		return err
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	var want string
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		// checksums.txt pairs a hash with a filename; goreleaser emits
		// "hash  filename" but the ordering costs nothing to accept either way.
		switch {
		case fields[1] == assetName:
			want = fields[0]
		case fields[0] == assetName:
			want = fields[1]
		default:
			continue
		}
		break
	}
	if want == "" {
		return fmt.Errorf("checksums.txt has no entry for %s", assetName)
	}
	if !strings.EqualFold(want, got) {
		return fmt.Errorf("checksum mismatch for %s: got %s, published %s", assetName, got[:12], want[:12])
	}
	return nil
}

// extractBinary pulls one file out of a .tar.gz.
func extractBinary(archivePath, want, dst string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s not found in archive", want)
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != want {
			continue
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		defer out.Close()
		// Bounded copy: a hostile archive must not fill the disk.
		if _, err := io.Copy(out, io.LimitReader(tr, 200<<20)); err != nil {
			return err
		}
		return nil
	}
}
