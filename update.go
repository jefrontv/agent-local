package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	// signature (or an arch-thinned one) would be killed on first exec.
	_ = exec.Command("codesign", "-f", "-s", "-", staged).Run()
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
