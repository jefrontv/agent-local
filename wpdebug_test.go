package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const stockWPConfig = `<?php
define( 'DB_NAME', 'al_s' );
define( 'WP_DEBUG', false );

/* That's all, stop editing! Happy publishing. */

if ( ! defined( 'ABSPATH' ) ) {
	define( 'ABSPATH', __DIR__ . '/' );
}
require_once ABSPATH . 'wp-settings.php';
`

func TestSetWPConstRaw(t *testing.T) {
	// Replacing an existing boolean define, whatever its current value.
	out := setWPConstRaw(stockWPConfig, "WP_DEBUG", "true")
	if !strings.Contains(out, "define( 'WP_DEBUG', true )") {
		t.Errorf("WP_DEBUG not replaced:\n%s", out)
	}
	if strings.Count(out, "WP_DEBUG") != 1 {
		t.Errorf("WP_DEBUG duplicated:\n%s", out)
	}

	// A missing define lands above the stop-editing marker, where WordPress
	// reads it before wp-settings.php runs.
	out = setWPConstRaw(out, "WP_DEBUG_LOG", "'/tmp/x.log'")
	logAt := strings.Index(out, "WP_DEBUG_LOG")
	stopAt := strings.Index(out, "/* That's all")
	if logAt < 0 || stopAt < 0 || logAt > stopAt {
		t.Errorf("WP_DEBUG_LOG not inserted before the stop-editing marker:\n%s", out)
	}

	// No marker: fall back to above the wp-settings require.
	noMarker := strings.Replace(stockWPConfig, "/* That's all, stop editing! Happy publishing. */\n", "", 1)
	out = setWPConstRaw(noMarker, "WP_DEBUG_DISPLAY", "false")
	dispAt := strings.Index(out, "WP_DEBUG_DISPLAY")
	reqAt := strings.Index(out, "require_once ABSPATH")
	if dispAt < 0 || dispAt > reqAt {
		t.Errorf("insert did not land before wp-settings require:\n%s", out)
	}

	// Neither anchor: appended, still syntactically after everything else.
	out = setWPConstRaw("<?php\n", "WP_DEBUG", "true")
	if !strings.Contains(out, "define( 'WP_DEBUG', true );") {
		t.Errorf("append fallback failed:\n%s", out)
	}
}

func TestReadWPConstRaw(t *testing.T) {
	src := stockWPConfig + "define( 'WP_DEBUG_LOG', '/tmp/wp.log' );\n"
	if v := readWPConstRaw(src, "WP_DEBUG"); v != "false" {
		t.Errorf("WP_DEBUG = %q, want false", v)
	}
	if v := readWPConstRaw(src, "WP_DEBUG_LOG"); v != "'/tmp/wp.log'" {
		t.Errorf("WP_DEBUG_LOG = %q", v)
	}
	if v := readWPConstRaw(src, "NOT_THERE"); v != "" {
		t.Errorf("missing const = %q, want empty", v)
	}
}

func TestSetWPDebugRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	docroot := filepath.Join(HomeDir(), "site")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docroot, "wp-config.php"), []byte(stockWPConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	site := &Site{Slug: "s", Domain: "s.test", WPDir: docroot}
	store.PutSite(site)
	e := NewEngine(store)

	st, err := e.SetWPDebug("s", true)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled {
		t.Error("enable did not report enabled")
	}
	wantLog := P().Log(WPDebugLogName("s"))
	if st.LogPath != wantLog {
		t.Errorf("log path = %s, want %s", st.LogPath, wantLog)
	}
	if st.LogName != "wp-s" {
		t.Errorf("log name = %s, want wp-s", st.LogName)
	}
	b, _ := os.ReadFile(filepath.Join(docroot, "wp-config.php"))
	if !strings.Contains(string(b), "define( 'WP_DEBUG_DISPLAY', false )") {
		t.Errorf("display not forced off:\n%s", b)
	}

	// Off flips only WP_DEBUG; status reads back disabled.
	st, err = e.SetWPDebug("s", false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Enabled {
		t.Error("disable did not report disabled")
	}
	if WPDebugStatus(site).Enabled {
		t.Error("status still enabled after off")
	}

	// wp-config one level above the docroot is found too: site "a" serves
	// docroot/wp while the config stays at docroot/wp-config.php.
	nested := filepath.Join(docroot, "wp")
	os.MkdirAll(nested, 0o755)
	store.PutSite(&Site{Slug: "a", Domain: "a.test", WPDir: nested})
	if _, err := e.SetWPDebug("a", true); err != nil {
		t.Fatalf("wp-config above docroot not found: %v", err)
	}
}
