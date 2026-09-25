package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Delete removes a directory only when agent-local filled it. Sitting inside the
// configured sites directory is not ownership: attached checkouts and in-place
// imports live there too, and deleting the site must leave those repos alone.
func TestDeleteRemovesOnlyWhatItOwns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_LOCAL_HOME", "")
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	sitesDir := filepath.Join(home, "Sites")
	if err := store.SetSitesDir(sitesDir); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(store)

	type tc struct {
		name      string
		site      Site
		layout    string // WPDir relative to WorkDir ("" = same dir)
		wantGone  bool
		keepFiles bool
	}
	cases := []tc{
		{name: "attached-in-sites-dir", site: Site{Attached: true}, wantGone: false},
		{name: "inplace-import-in-sites-dir", site: Site{}, layout: "public", wantGone: false},
		{name: "installed-in-sites-dir", site: Site{Installed: true}, layout: "wp", wantGone: true},
		{name: "copy-import-own-tree", site: Site{}, layout: "wp", wantGone: true},
		{name: "installed-in-sites-dir-keep", site: Site{Installed: true}, layout: "wp", keepFiles: true, wantGone: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := filepath.Join(sitesDir, c.name)
			if c.name == "copy-import-own-tree" {
				root = filepath.Join(P().Sites(), c.name)
			}
			docroot := filepath.Join(root, c.layout)
			if err := os.MkdirAll(docroot, 0o755); err != nil {
				t.Fatal(err)
			}
			userFile := filepath.Join(root, "README.md")
			os.WriteFile(userFile, []byte("client work"), 0o644)
			cfg := filepath.Join(docroot, "wp-config.php")
			os.WriteFile(cfg+".agent-local.bak", []byte("<?php // original"), 0o644)
			os.WriteFile(cfg, []byte("<?php // ours"), 0o644)

			s := c.site
			s.Slug, s.Name, s.WorkDir, s.WPDir = c.name, c.name, root, docroot
			if c.name == "inplace-import-in-sites-dir" {
				s.WorkDir = root // what an in-place import of <root>/public records
			}
			store.PutSite(&s)
			if err := store.Save(); err != nil {
				t.Fatal(err)
			}
			if err := e.DeleteSite(c.name, DeleteOpts{KeepDB: true, KeepFiles: c.keepFiles}); err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(userFile)
			if gone := os.IsNotExist(statErr); gone != c.wantGone {
				t.Fatalf("user file gone = %v, want %v", gone, c.wantGone)
			}
			if !c.wantGone && !c.keepFiles {
				b, _ := os.ReadFile(cfg)
				if string(b) != "<?php // original" {
					t.Errorf("wp-config not restored from backup: %q", b)
				}
			}
			if store.Site(c.name) != nil {
				t.Error("site record survived delete")
			}
		})
	}
}

// A serve-only import records whatever schema and user its wp-config names.
// Deleting it must not drop them: that can be another site's schema, or root.
func TestDeleteLeavesForeignDatabase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_LOCAL_HOME", "")
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(store)
	docroot := filepath.Join(home, "elsewhere", "public")
	os.MkdirAll(docroot, 0o755)
	site := &Site{Slug: "foreign", Name: "foreign", WorkDir: docroot, WPDir: docroot, DBName: "al_other", DBUser: "root"}
	store.PutSite(site)
	store.Save()

	if ownsDB(site) {
		t.Fatal("ownsDB accepted a schema named by the site's own wp-config")
	}
	if err := e.DropSiteDB(site); err == nil {
		t.Fatal("DropSiteDB dropped a database agent-local did not create")
	}
	// No MariaDB runs in this test: reaching the engine at all would fail, so a
	// clean delete also proves neither the snapshot nor the drop was attempted.
	if err := e.DeleteSite("foreign", DeleteOpts{}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !ownsDB(&Site{Slug: "x", DBName: "al_x", DBUser: "al_x"}) {
		t.Error("ownsDB rejected a provisioned al_<slug> schema")
	}
}
