package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A DDEV wp-config.php has no DB_* defines and no $table_prefix: both live in
// wp-config-ddev.php behind IS_DDEV_PROJECT. Importing one must add them, not
// silently leave the file pointing at a container that no longer exists.
func TestRewriteWPConfigDBAddsMissingDefinesAndPrefix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wp-config.php")
	src := "<?php\n" +
		"/** DDEV-style config: settings come from wp-config-ddev.php */\n" +
		"if (getenv('IS_DDEV_PROJECT') == 'true' && is_readable(__DIR__ . '/wp-config-ddev.php')) {\n" +
		"\trequire_once(__DIR__ . '/wp-config-ddev.php');\n}\n" +
		"require_once ABSPATH . 'wp-settings.php';\n"
	os.WriteFile(path, []byte(src), 0o644)
	site := &Site{DBName: "al_ddevwp", DBUser: "al_ddevwp", DBPass: "s3cret'quote"}
	if err := rewriteWPConfigDB(path, site, "wp_"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	got := string(b)
	for _, want := range []string{
		"define( 'DB_NAME', 'al_ddevwp' );",
		"define( 'DB_USER', 'al_ddevwp' );",
		`define( 'DB_PASSWORD', 's3cret\'quote' );`,
		"define( 'DB_HOST', '127.0.0.1:10360' );",
		"$table_prefix = 'wp_';",
		"// added by agent-local",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// The additions sit before the file's own code, and the marker appears once.
	if strings.Index(got, "DB_NAME") > strings.Index(got, "IS_DDEV_PROJECT") {
		t.Errorf("defines were added after the file's own code:\n%s", got)
	}
	if strings.Count(got, "added by agent-local") != 1 {
		t.Errorf("marker should appear once:\n%s", got)
	}
	if !fileExists(path + ".agent-local.bak") {
		t.Error("no backup written")
	}
}

// An ordinary wp-config.php keeps its own defines and prefix: values are
// replaced in place, and nothing is inserted.
func TestRewriteWPConfigDBReplacesInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wp-config.php")
	src := "<?php\ndefine( 'DB_NAME', 'local' );\ndefine( 'DB_USER', 'root' );\ndefine( 'DB_PASSWORD', 'root' );\ndefine( 'DB_HOST', 'localhost' );\n$table_prefix = 'xy_';\n"
	os.WriteFile(path, []byte(src), 0o644)
	if err := rewriteWPConfigDB(path, &Site{DBName: "al_x", DBUser: "al_x", DBPass: "p"}, "wp_"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	got := string(b)
	if strings.Contains(got, "added by agent-local") {
		t.Errorf("nothing should be inserted when every define exists:\n%s", got)
	}
	if !strings.Contains(got, "define( 'DB_NAME', 'al_x' );") || strings.Contains(got, "'local'") {
		t.Errorf("DB_NAME not replaced:\n%s", got)
	}
	if !strings.Contains(got, "$table_prefix = 'xy_';") || strings.Contains(got, "'wp_'") {
		t.Errorf("existing table prefix must win:\n%s", got)
	}
}

func TestInsertAfterPHPOpen(t *testing.T) {
	got := insertAfterPHPOpen("<?php\n$a = 1;\n", "$b = 2;\n")
	if got != "<?php\n// added by agent-local\n$b = 2;\n$a = 1;\n" {
		t.Errorf("unexpected: %q", got)
	}
	// A second insertion shares the marker rather than stacking one per line.
	got = insertAfterPHPOpen(got, "$c = 3;\n")
	if strings.Count(got, "added by agent-local") != 1 || !strings.Contains(got, "$c = 3;\n$b = 2;") {
		t.Errorf("second insert: %q", got)
	}
	// No opening tag at all: one is created.
	if got := insertAfterPHPOpen("", "$x = 1;\n"); !strings.HasPrefix(got, "<?php\n// added by agent-local\n$x = 1;\n") {
		t.Errorf("no tag: %q", got)
	}
}

// project_list.yaml is what stands in for `ddev list` when Docker is down.
func TestDDEVRegistryParsesNamesAndRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".ddev"), 0o755)
	os.WriteFile(filepath.Join(home, ".ddev", "project_list.yaml"), []byte(
		"# managed by ddev\ntest:\n    approot: /Users/me/Sites/test\nshop:\n    approot: \"/Users/me/Sites/shop\"\n    other: ignored\n"), 0o644)
	ps := ddevRegistry()
	if len(ps) != 2 {
		t.Fatalf("want 2 projects, got %+v", ps)
	}
	if ps[0].Name != "test" || ps[0].AppRoot != "/Users/me/Sites/test" {
		t.Errorf("first: %+v", ps[0])
	}
	if ps[1].Name != "shop" || ps[1].AppRoot != "/Users/me/Sites/shop" {
		t.Errorf("second (quoted root): %+v", ps[1])
	}
}

// The match rule: name, or a directory at/below the approot. Never a parent.
func TestDDEVMatches(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "web")
	os.MkdirAll(web, 0o755)
	p := DDEVProject{Name: "shop", AppRoot: root}
	cases := []struct {
		source string
		want   bool
	}{
		{"shop", true},
		{root, true},
		{web, true},
		{filepath.Dir(root), false},
		{"shopping", false},
		{filepath.Join(root, "missing"), false},
	}
	for _, c := range cases {
		if got := ddevMatches(p, c.source); got != c.want {
			t.Errorf("ddevMatches(%q) = %v, want %v", c.source, got, c.want)
		}
	}
}

func TestDDEVDBCredsDefaultsWhenDescribeIsThin(t *testing.T) {
	var p DDEVProject
	if port, _, _, _ := p.dbCreds(); port != 0 {
		t.Errorf("stopped project must report no port, got %d", port)
	}
	p.DBInfo = &ddevDBInfo{PublishedPort: 32773}
	port, user, pass, name := p.dbCreds()
	if port != 32773 || user != "db" || pass != "db" || name != "db" {
		t.Errorf("defaults: %d %s %s %s", port, user, pass, name)
	}
}

func TestParseDockerPort(t *testing.T) {
	if got := parseDockerPort("127.0.0.1:32773\n"); got != 32773 {
		t.Errorf("ipv4: %d", got)
	}
	if got := parseDockerPort("[::]:32773\n127.0.0.1:32773"); got != 32773 {
		t.Errorf("ipv6 first: %d", got)
	}
	if got := parseDockerPort(""); got != 0 {
		t.Errorf("empty: %d", got)
	}
}
