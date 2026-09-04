package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParsePHPLogLine(t *testing.T) {
	cases := []struct {
		name        string
		line        string
		wantOK      bool
		level       string
		msgContains string
		file        string
		wantLine    int
	}{
		{
			name:        "fatal with colon file:line",
			line:        `[04-Sep-2026 05:35:11 UTC] PHP Fatal error:  Uncaught TypeError: implode(): Argument #1 ($separator) must be of type string, array given in /Users/x/wp-content/plugins/wp-rocket/vendor/matthiasmullie/minify/src/CSS.php:528`,
			wantOK:      true,
			level:       "fatal",
			msgContains: "Uncaught TypeError",
			file:        "/Users/x/wp-content/plugins/wp-rocket/vendor/matthiasmullie/minify/src/CSS.php",
			wantLine:    528,
		},
		{
			name:        "warning with on line form",
			line:        `[04-Sep-2026 05:36:00 UTC] PHP Warning:  Undefined array key "foo" in /var/www/html/wp-content/themes/x/functions.php on line 186`,
			wantOK:      true,
			level:       "warning",
			msgContains: "Undefined array key",
			file:        "/var/www/html/wp-content/themes/x/functions.php",
			wantLine:    186,
		},
		{
			name:        "deprecated",
			line:        `[04-Sep-2026 05:36:01 UTC] PHP Deprecated:  strlen(): Passing null to parameter in /var/www/html/wp-includes/x.php on line 10`,
			wantOK:      true,
			level:       "deprecated",
			msgContains: "strlen()",
			file:        "/var/www/html/wp-includes/x.php",
			wantLine:    10,
		},
		{
			name:        "notice",
			line:        `[04-Sep-2026 05:36:02 UTC] PHP Notice:  Undefined variable in /var/www/html/x.php on line 5`,
			wantOK:      true,
			level:       "notice",
			msgContains: "Undefined variable",
			file:        "/var/www/html/x.php",
			wantLine:    5,
		},
		{
			name:        "parse error",
			line:        `[04-Sep-2026 05:36:03 UTC] PHP Parse error:  syntax error, unexpected '}' in /var/www/html/x.php on line 20`,
			wantOK:      true,
			level:       "parse",
			msgContains: "syntax error",
			file:        "/var/www/html/x.php",
			wantLine:    20,
		},
		{
			name:        "wordpress db error",
			line:        `[04-Sep-2026 05:36:04 UTC] WordPress database error Table 'wp_x' doesn't exist for query SELECT * FROM wp_x`,
			wantOK:      true,
			level:       "db",
			msgContains: "Table 'wp_x' doesn't exist",
			file:        "",
			wantLine:    0,
		},
		{
			name:   "fpm notice is not a php error",
			line:   `[04-Sep-2026 15:31:01] NOTICE: fpm is running, pid 123`,
			wantOK: false,
		},
		{
			name:   "stack trace line",
			line:   `#0 /var/www/html/x.php(10): foo()`,
			wantOK: false,
		},
		{
			name:   "stack trace header",
			line:   `Stack trace:`,
			wantOK: false,
		},
		{
			name:   "thrown in line",
			line:   `  thrown in /var/www/html/x.php on line 528`,
			wantOK: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			level, msg, file, line, ok := parsePHPLogLine(c.line)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v (msg=%q)", ok, c.wantOK, msg)
			}
			if !c.wantOK {
				return
			}
			if level != c.level {
				t.Errorf("level = %q, want %q", level, c.level)
			}
			if c.msgContains != "" && !substrMatch(msg, c.msgContains) {
				t.Errorf("message = %q, want substring %q", msg, c.msgContains)
			}
			if file != c.file {
				t.Errorf("file = %q, want %q", file, c.file)
			}
			if line != c.wantLine {
				t.Errorf("line = %d, want %d", line, c.wantLine)
			}
		})
	}
}

func substrMatch(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestCollectErrors(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "pool.log")

	now := time.Now().UTC()
	fmtTime := func(t time.Time) string { return t.Format("02-Jan-2006 15:04:05 MST") }

	oldTime := now.Add(-48 * time.Hour)
	recent := now.Add(-time.Minute)

	lines := []string{
		"[" + fmtTime(oldTime) + "] PHP Warning:  old warning in /x.php on line 1",
		"[" + fmtTime(recent) + "] PHP Warning:  dup warning in /x.php on line 2",
		"[" + fmtTime(recent) + "] PHP Warning:  dup warning in /x.php on line 2",
		"[" + fmtTime(recent) + "] PHP Warning:  dup warning in /x.php on line 2",
		"[" + fmtTime(recent) + "] PHP Fatal error:  boom in /x.php on line 3",
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, scanned := collectErrors([]string{logPath}, []string{"fpm"}, time.Hour, 50)
	if scanned != len(lines) {
		t.Fatalf("scanned = %d, want %d", scanned, len(lines))
	}

	var warning, fatal *errorEntry
	for i := range entries {
		switch entries[i].Level {
		case "warning":
			warning = &entries[i]
		case "fatal":
			fatal = &entries[i]
		}
	}
	if warning == nil {
		t.Fatal("no warning entry found (old one excluded, recent dup kept)")
	}
	if warning.Count != 3 {
		t.Errorf("warning count = %d, want 3 (old entry outside `since` must not be counted)", warning.Count)
	}
	if fatal == nil {
		t.Fatal("no fatal entry found")
	}
	if fatal.Count != 1 {
		t.Errorf("fatal count = %d, want 1", fatal.Count)
	}

	// Ordering: most recently seen first. Both entries here share the same
	// `recent` timestamp, so just check the old-only entry never appears.
	for _, e := range entries {
		if e.Message == "old warning" {
			t.Errorf("entry outside `since` window leaked into results: %+v", e)
		}
	}
}

func TestParseSince(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", time.Hour},
		{"1h", time.Hour},
		{"24h", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
	}
	for _, c := range cases {
		got, err := parseSince(c.in)
		if err != nil {
			t.Fatalf("parseSince(%q) error: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("parseSince(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	if _, err := parseSince("not-a-duration"); err == nil {
		t.Error("parseSince(bad) should error")
	}
}

func TestSiteErrorsUsesFPMLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	e := NewEngine(nil)
	site := &Site{Slug: "testsite", WPDir: filepath.Join(home, "wp")}
	if err := os.MkdirAll(filepath.Dir(e.fpmLog(site.Slug)), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	line := "[" + now.Format("02-Jan-2006 15:04:05 MST") + "] PHP Fatal error:  boom in /x.php on line 1\n"
	if err := os.WriteFile(e.fpmLog(site.Slug), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, scanned := e.SiteErrors(site, time.Hour, 50)
	if scanned != 1 {
		t.Fatalf("scanned = %d, want 1", scanned)
	}
	if len(entries) != 1 || entries[0].Level != "fatal" || entries[0].Source != "fpm" {
		t.Fatalf("entries = %+v, want one fatal fpm entry", entries)
	}
}
