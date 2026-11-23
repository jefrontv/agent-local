package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Stand in for the release binary: exercising the installer's setup handoff
// must not install services or edit the developer's machine.
func TestPipedInstallerRunsDownloadedSetup(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "bin")
	binary := []byte("#!/bin/sh\ncase \"$1\" in setup) echo setup > \"$HOME/did-setup\";; version) echo 1.0.0;; esac\n")
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tarball := tar.NewWriter(gz)
	if err := tarball.WriteHeader(&tar.Header{Name: "agent-local", Size: int64(len(binary)), Mode: 0755}); err != nil {
		t.Fatal(err)
	}
	tarball.Write(binary)
	tarball.Close()
	gz.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data.Bytes()) }))
	defer srv.Close()
	script, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agent-local"), []byte("#!/bin/sh\nexit 54\n"), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-s", "--", "--setup")
	cmd.Dir = root
	cmd.Stdin = bytes.NewReader(script)
	cmd.Env = append(os.Environ(), "HOME="+root, "INSTALL_DIR="+dest, "RELEASE_URL="+srv.URL, "PATH=/usr/bin:/bin:/usr/sbin:/sbin")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install: %v %s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(root, "did-setup"))
	if err != nil || string(got) != "setup\n" {
		t.Fatalf("setup marker %q: %v", got, err)
	}
}

func TestDatabaseRootClientKeepsPasswordOffCommandLine(t *testing.T) {
	e := testEngine(t)
	dir := t.TempDir()
	e.Store.Inventory().MySQL.Bin = filepath.Join(dir, "mysqld")
	script := `#!/bin/sh
for arg do case "$arg" in --password=*) exit 64;; esac; done
test "$MYSQL_PWD" = "$EXPECTED_PASSWORD" || exit 65
case "$*" in
 *"SELECT host FROM mysql.user"*) printf 'host\nlocalhost\n127.0.0.1\nexternal\n';;
 *"DROP USER"*) printf '%s\n' "$*" > "$HOME/drop.sql";;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "mariadb"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EXPECTED_PASSWORD", DBRootPassword())
	if err := e.secureRoot(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), "drop.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "'root'@'external'") || strings.Contains(string(b), "'root'@'localhost'") {
		t.Fatal("unexpected root account pruning")
	}
	if _, err := e.dbExec("incorrect", "SELECT 1"); err == nil {
		t.Fatal("client accepted wrong password")
	}
}

func TestParseCommandOptions(t *testing.T) {
	fs, pos, err := parseOptions([]string{"--scope", "database", "shop", "--list", "--", "--literal"}, "scope", "list")
	if err != nil || fmt.Sprint(pos) != "[shop --literal]" || fs.Lookup("scope").Value.String() != "database" || fs.Lookup("list").Value.String() != "true" {
		t.Fatalf("options %v %v", pos, err)
	}
	for _, args := range [][]string{{"--scope"}, {"--unknown"}, {"--scope", "--list"}} {
		if _, _, err := parseOptions(args, "scope", "list"); err == nil {
			t.Fatal(args)
		}
	}
}
