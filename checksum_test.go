package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// verifyChecksum runs inside the daemon's update loop, where a panic takes
// every site down. A malformed checksums.txt must come back as an error.
func TestVerifyChecksumRejectsMalformedEntries(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	os.WriteFile(archive, []byte("release bytes"), 0o644)
	sum := sha256.Sum256([]byte("release bytes"))
	good := hex.EncodeToString(sum[:])

	for name, c := range map[string]struct {
		body    string
		wantErr string
	}{
		"match":    {good + "  a.tar.gz\n", ""},
		"short":    {"abc  a.tar.gz\n", "not a sha256"},
		"mismatch": {strings.Repeat("0", 64) + "  a.tar.gz\n", "checksum mismatch"},
		"missing":  {good + "  other.tar.gz\n", "no entry"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Write([]byte(c.body))
			}))
			defer srv.Close()
			err := verifyChecksum(archive, "a.tar.gz", srv.URL)
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("err = %v, want it to mention %q", err, c.wantErr)
			}
		})
	}
}
