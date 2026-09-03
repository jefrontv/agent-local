package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// bindBusy reports whether err is a port conflict with something else on this
// machine — the developer's own daemon, a rival tool — rather than a product
// failure. Those runs skip instead of failing.
func bindBusy(err error) bool {
	return err != nil && strings.Contains(err.Error(), "cannot bind")
}

// The daemon must stay up after a successful boot. The router reports bind
// failures only — a nil return means "serving" — so waiting on its return as
// an exit signal once shut the whole daemon down seconds after boot, with
// every site going dark and no error logged.
func TestDaemonStaysUpAfterBoot(t *testing.T) {
	for _, p := range []int{DefaultAPIPort, DefaultHTTPPort, DefaultHTTPSPort} {
		if portOpen(p) {
			t.Skipf("port %d is taken, a daemon is already serving here", p)
		}
	}
	t.Setenv("HOME", t.TempDir())
	errCh := make(chan error, 1)
	go func() { errCh <- RunDaemon(true) }()
	deadline := time.Now().Add(30 * time.Second)
	for !portOpen(DefaultAPIPort) {
		select {
		case err := <-errCh:
			if bindBusy(err) {
				t.Skipf("port taken mid-boot by another process: %v", err)
			}
			t.Fatalf("daemon exited during boot: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon never opened the API port")
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Past the boot window: still serving, and answering — not just bound.
	time.Sleep(3 * time.Second)
	select {
	case err := <-errCh:
		if bindBusy(err) {
			t.Skipf("port taken mid-boot by another process: %v", err)
		}
		t.Fatalf("daemon exited right after a clean boot: %v", err)
	default:
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", DefaultAPIPort))
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", resp.StatusCode)
	}
}
