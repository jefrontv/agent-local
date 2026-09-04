package main

import (
	"os"
	"os/exec"
	"testing"
)

func TestExtractJSONObjectSkipsNoise(t *testing.T) {
	in := "PHP Notice: something happened\n{\"wp_version\":\"6.4\"}\n"
	got := extractJSONObject(in)
	want := `{"wp_version":"6.4"}`
	if got != want {
		t.Fatalf("extractJSONObject() = %q, want %q", got, want)
	}
}

func TestExtractJSONObjectNoBraces(t *testing.T) {
	if got := extractJSONObject("no json here"); got != "" {
		t.Fatalf("extractJSONObject() = %q, want empty", got)
	}
}

func TestFinishWPInfoMatch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	site := &Site{Slug: "example", Domain: "example.test", WPDir: dir}
	info := &wpInfo{Home: "https://example.test"}
	finishWPInfo(info, site)
	if !info.ServedDomainMatches {
		t.Fatalf("expected served_domain_matches true for %q vs %q", info.Home, site.Domain)
	}
	if info.ServedDomain != "example.test" {
		t.Fatalf("served_domain = %q, want example.test", info.ServedDomain)
	}
}

func TestFinishWPInfoMismatch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	site := &Site{Slug: "example", Domain: "example.test", WPDir: dir}
	info := &wpInfo{Home: "https://staging.other.test"}
	finishWPInfo(info, site)
	if info.ServedDomainMatches {
		t.Fatalf("expected served_domain_matches false for %q vs %q", info.Home, site.Domain)
	}
}

func TestFinishWPInfoPins(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.WriteFile(dir+"/wp-config.php", []byte("<?php\ndefine('WP_HOME', 'https://example.test');\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	site := &Site{Slug: "example", Domain: "example.test", WPDir: dir}
	info := &wpInfo{Home: "https://example.test"}
	finishWPInfo(info, site)
	if len(info.Pins) != 1 || info.Pins[0] != "WP_HOME=https://example.test" {
		t.Fatalf("pins = %v, want [WP_HOME=https://example.test]", info.Pins)
	}
}

// TestWPInfoPHPSnippetSyntax lints the embedded PHP with `php -l`. wp-cli
// wraps eval'd code in a function body, so the snippet is checked the same
// way here to catch unbalanced quotes/braces before they ever reach wp-cli.
func TestWPInfoPHPSnippetSyntax(t *testing.T) {
	phpBin, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php not on PATH")
	}
	src := "<?php\nfunction __wp_info_check() {\n" + wpInfoPHP + "\n}\n"
	f, err := os.CreateTemp(t.TempDir(), "wpinfo-*.php")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(src); err != nil {
		t.Fatal(err)
	}
	f.Close()
	cmd := exec.Command(phpBin, "-l", f.Name())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("php -l failed: %v\n%s", err, out)
	}
}
