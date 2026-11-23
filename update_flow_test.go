package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGitHub answers the release API and asset downloads in-process, and
// installs a stand-in binary that reports the given version. The real binary
// and the network are never involved. It returns a count of asset downloads.
func fakeGitHub(t *testing.T, installed string, rel *ghRelease, status int) *int {
	t.Helper()
	testEngine(t)
	bin := filepath.Join(os.Getenv("HOME"), ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho 'agent-local " + installed + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "agent-local"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	downloads := 0
	orig := http.DefaultTransport
	http.DefaultTransport = contractTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			if status != 200 {
				w.WriteHeader(status)
				break
			}
			json.NewEncoder(w).Encode(rel)
		case strings.HasSuffix(r.URL.Path, ".sigstore.json"):
			downloads++
			w.WriteString("not a bundle")
		default:
			downloads++
			w.WriteString("payload")
		}
		return w.Result(), nil
	})
	t.Cleanup(func() { http.DefaultTransport = orig })
	return &downloads
}

func release(tag string, assets ...string) *ghRelease {
	rel := &ghRelease{TagName: tag}
	for _, name := range assets {
		rel.Assets = append(rel.Assets, struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		}{Name: name, URL: "https://example.invalid/" + name})
	}
	return rel
}

func TestSelfUpdateStopsBeforeInstallingUnverifiedBytes(t *testing.T) {
	archive := "agent-local_9.9.9_darwin_universal.tar.gz"
	cases := []struct {
		name, installed string
		rel             *ghRelease
		status          int
		wantErr         string
		wantVersion     string
	}{
		{"no release", "0.1.0", nil, 404, "no published release", ""},
		{"github down", "0.1.0", nil, 502, "github returned", ""},
		{"current", "9.9.9", release("v9.9.9", archive, "checksums.txt"), 200, "", "9.9.9"},
		{"no archive", "0.1.0", release("v9.9.9", "checksums.txt"), 200, "no darwin_universal archive", ""},
		{"no checksums", "0.1.0", release("v9.9.9", archive), 200, "unverified", ""},
		{"bad signature", "0.1.0", release("v9.9.9", archive, "checksums.txt"), 200, "read release signature", ""},
		{"unparseable tag", "0.1.0", release("v9.9.9; rm", archive, "checksums.txt"), 200, "", "0.1.0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fakeGitHub(t, c.installed, c.rel, c.status)
			self := filepath.Join(os.Getenv("HOME"), ".local", "bin", "agent-local")
			before, _ := os.ReadFile(self)
			var stages []string
			got, err := SelfUpdate(func(s string) { stages = append(stages, s) })
			switch {
			case c.wantErr == "" && (err != nil || got != c.wantVersion):
				t.Fatalf("got %q %v", got, err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("want %q, got %q %v (stages %v)", c.wantErr, got, err, stages)
			}
			if after, _ := os.ReadFile(self); string(after) != string(before) {
				t.Fatal("the installed binary changed")
			}
		})
	}
}

func TestVerifyReleaseRejectsBadInputs(t *testing.T) {
	dir := t.TempDir()
	if err := verifyReleaseFiles(dir, "a", dir, dir, "latest"); err == nil || !strings.Contains(err.Error(), "invalid release tag") {
		t.Fatalf("tag: %v", err)
	}
	sig := filepath.Join(dir, "sig.json")
	os.WriteFile(sig, []byte("not json"), 0o644)
	if err := verifyReleaseFiles(dir, "a", dir, sig, "v1.2.3"); err == nil {
		t.Fatal("an empty bundle verified")
	}
}
