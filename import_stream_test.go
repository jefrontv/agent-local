package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixCollationsRewritesMySQL8Names(t *testing.T) {
	in := "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;"
	got := string(fixCollations([]byte(in)))
	if strings.Contains(got, "utf8mb4_0900_ai_ci") {
		t.Fatalf("still has 0900: %s", got)
	}
	if !strings.Contains(got, "utf8mb4_unicode_ci") {
		t.Fatalf("missing unicode_ci: %s", got)
	}
	in2 := "COLLATE utf8mb4_uca1400_as_cs"
	got2 := string(fixCollations([]byte(in2)))
	if strings.Contains(got2, "uca1400") {
		t.Fatalf("uca1400 survived: %s", got2)
	}
}

func TestStreamFixCollationsSplitsTokenAcrossReads(t *testing.T) {
	// Force tiny reads by feeding through a reader that yields one byte at a time.
	src := &byteAtATime{s: []byte("COLLATE=utf8mb4_0900_ai_ci END")}
	var dst bytes.Buffer
	if err := streamFixCollations(&dst, src); err != nil {
		t.Fatal(err)
	}
	got := dst.String()
	if strings.Contains(got, "0900") {
		t.Fatalf("split token not rewritten: %q", got)
	}
	if !strings.Contains(got, "utf8mb4_unicode_ci") {
		t.Fatalf("rewrite missing: %q", got)
	}
}

func TestStreamFixCollationsHandlesGiantLine(t *testing.T) {
	// The old scanner died at 64MB. A 70MB INSERT with no newlines must pass.
	var b strings.Builder
	b.WriteString("INSERT INTO t VALUES ('")
	b.WriteString(strings.Repeat("x", 70*1024*1024))
	b.WriteString("utf8mb4_0900_ai_ci');")
	src := strings.NewReader(b.String())
	var dst bytes.Buffer
	if err := streamFixCollations(&dst, src); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(dst.Bytes(), []byte("utf8mb4_unicode_ci")) {
		t.Fatal("70MB line did not rewrite the trailing collation")
	}
	if bytes.Contains(dst.Bytes(), []byte("utf8mb4_0900_ai_ci")) {
		t.Fatal("0900 survived a 70MB line")
	}
}

func TestHostFromURLKeepsPort(t *testing.T) {
	cases := map[string]string{
		"https://x.com:8443/path": "x.com:8443",
		"http://x.com/":           "x.com",
		"https://user:pw@x.com/a": "x.com",
		"x.com":                   "x.com",
	}
	for in, want := range cases {
		if got := hostFromURL(in); got != want {
			t.Errorf("hostFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRewriteWPConfigDomainsDoesNotSmashSalts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wp-config.php")
	src := `<?php
define('DB_NAME', 'x');
define('WP_HOME', 'https://old.test');
define('AUTH_KEY', 'random-old.test-salt-value');
define('EFRONT_URL_OVERRIDE', 'http://old.test');
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rewriteWPConfigDomains(path, map[string]bool{"old.test": true}, "new.test"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	body := string(got)
	if !strings.Contains(body, "https://new.test") {
		t.Errorf("WP_HOME not rewritten:\n%s", body)
	}
	if !strings.Contains(body, "http://new.test") {
		t.Errorf("EFRONT_URL_OVERRIDE scheme was not kept:\n%s", body)
	}
	if !strings.Contains(body, "random-old.test-salt-value") {
		t.Errorf("AUTH_KEY was smashed:\n%s", body)
	}
	// A scheme-less URL constant still rewrites.
	path2 := filepath.Join(dir, "wp-config2.php")
	if err := os.WriteFile(path2, []byte("define('WP_HOME', 'old.test');\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rewriteWPConfigDomains(path2, map[string]bool{"old.test": true}, "new.test"); err != nil {
		t.Fatal(err)
	}
	got2, _ := os.ReadFile(path2)
	if !strings.Contains(string(got2), "new.test") {
		t.Errorf("bare WP_HOME not rewritten: %s", got2)
	}
}

func TestMatchInstalledPHPMajorMinor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	s.Inventory().PHPs = []Runtime{{Version: "8.2"}}
	if got := matchInstalledPHP(s, "8.2.24"); got != "8.2" {
		t.Errorf("got %q, want 8.2", got)
	}
	if got := matchInstalledPHP(s, "8.3"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestImportUsesDocrootFor(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "app", "public")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "wp-load.php"), []byte("<?php"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DocrootFor(dir); got != nested {
		t.Errorf("DocrootFor = %q, want %q", got, nested)
	}
}

// The sydney-science-park shape: a production wp-config extracted over the
// docroot, then partially patched. WP_HOME/WP_SITEURL still name the origin,
// EFRONT_URL_OVERRIDE was repointed, and a commented-out pin does not run.
const sydneyConfig = `<?php
define('DB_NAME', 'al_ssp');
define('AUTH_KEY', 'salt-with-ssp.c1.efront.dev-inside');
define('EFRONT_URL_OVERRIDE', 'http://ssp.local');
define('WP_HOME', 'https://ssp.c1.efront.dev');
define( 'WP_SITEURL', 'https://ssp.c1.efront.dev' );
// define('WP_HOME', 'https://commented.out');
define( 'WP_DEBUG', true );
`

func TestWPConfigURLPinsReadsActiveDefinesOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wp-config.php"), []byte(sydneyConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	got := wpConfigURLPins(dir)
	want := []URLPin{
		{"EFRONT_URL_OVERRIDE", "http://ssp.local"},
		{"WP_HOME", "https://ssp.c1.efront.dev"},
		{"WP_SITEURL", "https://ssp.c1.efront.dev"},
	}
	if len(got) != len(want) {
		t.Fatalf("pins = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pin %d = %v, want %v", i, got[i], want[i])
		}
	}
	if wpConfigURLPins(filepath.Join(dir, "missing")) != nil {
		t.Error("a docroot without wp-config.php should have no pins")
	}
}

func TestForeignURLPinsSparesOwnNames(t *testing.T) {
	dir := t.TempDir()
	src := `<?php
define('WP_HOME', 'https://ssp.local');
define('WP_SITEURL', 'https://SSP.local:10443');
define('EFRONT_URL_OVERRIDE', 'http://alias.local');
define('DOMAIN_CURRENT_SITE', 'ssp.c1.efront.dev');
`
	if err := os.WriteFile(filepath.Join(dir, "wp-config.php"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	site := &Site{Domain: "ssp.local", Aliases: []string{"alias.local"}, WPDir: dir}
	got := foreignURLPins(site)
	// The domain, the domain with our port, and an alias are all ours; only the
	// multisite pin names another machine.
	if len(got) != 1 || got[0].Name != "DOMAIN_CURRENT_SITE" {
		t.Errorf("foreign pins = %v, want just DOMAIN_CURRENT_SITE", got)
	}
}

func TestRewriteWPConfigDomainsRepointsProductionPins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wp-config.php")
	if err := os.WriteFile(path, []byte(sydneyConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	site := &Site{Domain: "ssp.local", WPDir: dir}
	olds := map[string]bool{}
	for _, p := range foreignURLPins(site) {
		olds[hostFromURL(p.URL)] = true
	}
	if err := rewriteWPConfigDomains(path, olds, "ssp.local"); err != nil {
		t.Fatal(err)
	}
	if left := foreignURLPins(site); len(left) != 0 {
		t.Errorf("pins still foreign after rewrite: %v", left)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "define('WP_HOME', 'https://ssp.local')") {
		t.Errorf("WP_HOME scheme not kept:\n%s", body)
	}
	if !strings.Contains(string(body), "salt-with-ssp.c1.efront.dev-inside") {
		t.Errorf("AUTH_KEY was smashed:\n%s", body)
	}
}

func TestURLPinDetailNamesConstantsAndHosts(t *testing.T) {
	pins := []URLPin{{"WP_HOME", "https://ssp.c1.efront.dev"}, {"WP_SITEURL", "https://ssp.c1.efront.dev"}}
	got := urlPinDetail(pins, nil)
	want := "wp-config pins WP_HOME, WP_SITEURL to https://ssp.c1.efront.dev — every request redirects there"
	if got != want {
		t.Errorf("detail = %q\nwant     %q", got, want)
	}
	if got := urlPinDetail(nil, []string{"ssp.c1.efront.dev"}); !strings.HasPrefix(got, "database home is ssp.c1.efront.dev") {
		t.Errorf("db-only detail = %q", got)
	}
	if got := urlPinDetail(nil, nil); got != "" {
		t.Errorf("nothing wrong should say nothing, got %q", got)
	}
}

func TestOffSiteIgnoresOwnAndRelativeRedirects(t *testing.T) {
	site := &Site{Domain: "ssp.local", Aliases: []string{"alias.local"}}
	for loc, want := range map[string]bool{
		"https://ssp.local/":         false,
		"https://ssp.local:10443/x":  false,
		"http://alias.local/":        false,
		"/wp-login.php":              false,
		"":                           false,
		"https://ssp.c1.efront.dev/": true,
		"http://www.ssp.local/":      true,
	} {
		if got := offSite(site, loc); got != want {
			t.Errorf("offSite(%q) = %v, want %v", loc, got, want)
		}
	}
}

type byteAtATime struct {
	s []byte
	i int
}

func (r *byteAtATime) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	p[0] = r.s[r.i]
	r.i++
	return 1, nil
}
