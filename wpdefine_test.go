package main

import (
	"strings"
	"testing"
)

// define() values are PHP. The rewriters must splice them, not run them
// through regexp replacement templates or stop at the first ")".
func TestDefineRewritesKeepPHPIntact(t *testing.T) {
	src := "<?php\n" +
		"// define('WP_DEBUG', 'commented out');\n" +
		"define( 'WP_DEBUG', getenv('X') === '1' );\n" +
		"define('DB_PASSWORD', \"it's $1 (x)\");\n" +
		"define('WP_HOME', 'https://old.example');\n" +
		"require_once ABSPATH . 'wp-settings.php';\n"

	if got := readWPConstRaw(src, "WP_DEBUG"); got != "getenv('X') === '1'" {
		t.Errorf("readWPConstRaw(WP_DEBUG) = %q", got)
	}
	out := setWPConstRaw(src, "WP_DEBUG", "true")
	if want := "define( 'WP_DEBUG', true );"; !strings.Contains(out, want) {
		t.Errorf("setWPConstRaw lost the statement shape:\n%s", out)
	}
	if !strings.Contains(out, "// define('WP_DEBUG', 'commented out');") {
		t.Error("setWPConstRaw rewrote a commented-out define")
	}

	out = setWPConstRaw(src, "WP_HOME", "'http://' . $_SERVER['HTTP_HOST']")
	if !strings.Contains(out, "define('WP_HOME', 'http://' . $_SERVER['HTTP_HOST']);") {
		t.Errorf("$_SERVER expanded as a template:\n%s", out)
	}

	out = setWPConst(src, "DB_PASSWORD", `p$1'\x`)
	if !strings.Contains(out, `define('DB_PASSWORD', 'p$1\'\\x');`) {
		t.Errorf("setWPConst did not replace or escape the value:\n%s", out)
	}
	if n := strings.Count(out, "DB_PASSWORD"); n != 1 {
		t.Errorf("DB_PASSWORD defined %d times, want 1", n)
	}
}
