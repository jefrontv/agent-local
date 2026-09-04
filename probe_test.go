package main

import (
	"html"
	"os"
	"path/filepath"
	"testing"
)

func TestProbeVerdictFatalBeatsEverything(t *testing.T) {
	site := &Site{Domain: "x.test"}
	reqs := map[string]requestReport{
		"/": {
			Status:    200,
			BodyBytes: 0, // would otherwise read as "blank"
			PHPErrors: []string{"[04-Sep-2026 05:35:11 UTC] PHP Fatal error:  boom in x.php on line 1"},
		},
		"/wp-login.php": {Status: 302, Location: "https://evil.example/"}, // would otherwise be "redirects_offsite"
	}
	verdict, reason := probeVerdict(reqs, site)
	if verdict != "fatal" {
		t.Fatalf("verdict = %q, want fatal", verdict)
	}
	if reason == "" {
		t.Fatal("expected the fatal error line as reason")
	}
}

func TestProbeVerdictOffsiteBeatsBlank(t *testing.T) {
	site := &Site{Domain: "x.test"}
	reqs := map[string]requestReport{
		"/":             {Status: 200, BodyBytes: 0},
		"/wp-login.php": {Status: 302, Location: "https://evil.example/wp-login.php"},
	}
	verdict, reason := probeVerdict(reqs, site)
	if verdict != "redirects_offsite" {
		t.Fatalf("verdict = %q, want redirects_offsite", verdict)
	}
	if reason != "evil.example" {
		t.Fatalf("reason = %q, want evil.example", reason)
	}
}

func TestProbeVerdictBlankBeatsDownAndSlow(t *testing.T) {
	site := &Site{Domain: "x.test"}
	reqs := map[string]requestReport{
		"/": {Status: 200, BodyBytes: 0, Ms: 5000},
	}
	verdict, _ := probeVerdict(reqs, site)
	if verdict != "blank" {
		t.Fatalf("verdict = %q, want blank", verdict)
	}
}

func TestProbeVerdictDownOnError(t *testing.T) {
	site := &Site{Domain: "x.test"}
	reqs := map[string]requestReport{
		"/": {Status: 0, Error: "dial tcp: connection refused"},
	}
	verdict, reason := probeVerdict(reqs, site)
	if verdict != "down" || reason != "dial tcp: connection refused" {
		t.Fatalf("got verdict=%q reason=%q", verdict, reason)
	}
}

func TestProbeVerdictErrorBeatsAsset404AndSlow(t *testing.T) {
	site := &Site{Domain: "x.test"}
	reqs := map[string]requestReport{
		"/":                                    {Status: 500, BodyBytes: 10, Ms: 9000},
		"/wp-includes/js/jquery/jquery.min.js": {Status: 404},
	}
	verdict, _ := probeVerdict(reqs, site)
	if verdict != "error" {
		t.Fatalf("verdict = %q, want error", verdict)
	}
}

func TestProbeVerdictAsset404BeatsSlow(t *testing.T) {
	site := &Site{Domain: "x.test"}
	reqs := map[string]requestReport{
		"/":                                    {Status: 200, BodyBytes: 10, Ms: 9000},
		"/wp-includes/js/jquery/jquery.min.js": {Status: 404},
	}
	verdict, _ := probeVerdict(reqs, site)
	if verdict != "asset_404" {
		t.Fatalf("verdict = %q, want asset_404", verdict)
	}
}

func TestProbeVerdictSlow(t *testing.T) {
	site := &Site{Domain: "x.test"}
	reqs := map[string]requestReport{
		"/": {Status: 200, BodyBytes: 10, Ms: 4000},
	}
	verdict, _ := probeVerdict(reqs, site)
	if verdict != "slow" {
		t.Fatalf("verdict = %q, want slow", verdict)
	}
}

func TestProbeVerdictHealthy(t *testing.T) {
	site := &Site{Domain: "x.test"}
	reqs := map[string]requestReport{
		"/":                                    {Status: 200, BodyBytes: 500, Ms: 120},
		"/wp-includes/js/jquery/jquery.min.js": {Status: 200},
	}
	verdict, reason := probeVerdict(reqs, site)
	if verdict != "healthy" || reason != "" {
		t.Fatalf("got verdict=%q reason=%q", verdict, reason)
	}
}

func TestLogDeltaOnlyReadsPastOffsetAndFiltersToPHPLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fpm.log")
	prefix := "[04-Sep-2026 05:00:00 UTC] some old line that must not appear\n"
	if err := os.WriteFile(path, []byte(prefix), 0o644); err != nil {
		t.Fatal(err)
	}
	from := int64(len(prefix))

	appended := "" +
		"[04-Sep-2026 05:35:11 UTC] plain notice not matching pattern\n" +
		"[04-Sep-2026 05:35:12 UTC] PHP Fatal error:  Uncaught Error in /x.php:1\n" +
		"[04-Sep-2026 05:35:13] PHP Warning:  Undefined variable $x in /y.php on line 2\n" +
		"[04-Sep-2026 05:35:14 UTC] WordPress database error Table doesn't exist\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(appended); err != nil {
		t.Fatal(err)
	}
	f.Close()

	got := logDelta(path, from)
	if len(got) != 3 {
		t.Fatalf("logDelta returned %d lines, want 3: %#v", len(got), got)
	}
	for _, line := range got {
		if line == "" {
			t.Fatal("empty line in result")
		}
	}
	for _, line := range got {
		if line == "plain notice not matching pattern" {
			t.Fatalf("old/non-matching line leaked into result: %q", line)
		}
	}

	// Offset past EOF must not panic or read stale bytes back in.
	if got := logDelta(path, int64(len(prefix)+len(appended))+1000); got != nil {
		t.Fatalf("expected nil past a clamp-corrected offset, got %#v", got)
	}
	// A missing file is not an error, just nothing to report.
	if got := logDelta(filepath.Join(dir, "missing.log"), 0); got != nil {
		t.Fatalf("expected nil for missing file, got %#v", got)
	}
}

func TestLogDeltaCapsLineCountAndLength(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fpm.log")
	var content string
	for i := range 30 {
		content += "[04-Sep-2026 05:35:11 UTC] PHP Fatal error: line " + string(rune('a'+i%26)) + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := logDelta(path, 0)
	if len(got) != 20 {
		t.Fatalf("logDelta returned %d lines, want capped at 20", len(got))
	}
}

func TestTitleExtractionCollapsesWhitespaceAndUnescapesEntities(t *testing.T) {
	body := []byte("<html><head>\n<title>\n  Hello &amp;   World  \n</title>\n</head></html>")
	m := titleRe.FindSubmatch(body)
	if m == nil {
		t.Fatal("title regexp did not match")
	}
	got := collapseWhitespace(html.UnescapeString(string(m[1])))
	if got != "Hello & World" {
		t.Fatalf("title = %q, want %q", got, "Hello & World")
	}
}

func TestFileSizeMissingIsZero(t *testing.T) {
	if got := fileSize(filepath.Join(t.TempDir(), "nope")); got != 0 {
		t.Fatalf("fileSize on missing path = %d, want 0", got)
	}
	if got := fileSize(""); got != 0 {
		t.Fatalf("fileSize(\"\") = %d, want 0", got)
	}
}
