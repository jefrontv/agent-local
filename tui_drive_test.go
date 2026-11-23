package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

var (
	keyEsc   = tea.KeyMsg{Type: tea.KeyEsc}
	keyEnter = tea.KeyMsg{Type: tea.KeyEnter}
	keyTab   = tea.KeyMsg{Type: tea.KeyTab}
	keyBack  = tea.KeyMsg{Type: tea.KeyBackspace}
	keyUp    = tea.KeyMsg{Type: tea.KeyUp}
	keyDown  = tea.KeyMsg{Type: tea.KeyDown}
)

// driveModel is a dashboard over a temp HOME with one site, one branch preview
// and a canned doctor report, so no key in these tests starts the real checks.
func driveModel(t *testing.T) model {
	t.Helper()
	m := testModel(t, modeBrowse)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wp-config.php"), []byte("<?php\n/* That's all, stop editing! */\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.store.PutSite(&Site{Slug: "demo", Name: "Demo", Domain: "demo.test", Kind: KindWordPress, WPDir: dir, WorkDir: dir, PHPVersion: "8.3", State: StateStopped, DBName: "al_demo", AdminUser: "admin", AdminPass: "pw"})
	m.store.PutWorktree(&Worktree{ID: "demo--feature", Site: "demo", Branch: "feature", Path: t.TempDir(), Domain: "feature.demo.test"})
	if err := m.store.Save(); err != nil {
		t.Fatal(err)
	}
	m.doctor = &DoctorReport{Findings: []Finding{
		{Check: "php", Status: "ok", Detail: "8.3 runs"},
		{Check: "dns", Status: "warn", Detail: "slow resolver", FixHint: "check resolver"},
		{Check: "certs", Status: "fail", Detail: "leaf missing", FixCmd: "agent-local cert demo.test --trust", FixRoot: true, AutoFix: true},
	}}
	m.refresh()
	m.width, m.height = 120, 30
	return m
}

// press feeds keys through Update, rendering after each so every state the
// user could see is drawn at least once. Returned commands are never run.
func press(t *testing.T, m model, keys ...tea.KeyMsg) model {
	t.Helper()
	for _, k := range keys {
		next, _ := m.Update(k)
		m = next.(model)
		if m.View() == "" && !m.quitting {
			t.Fatalf("empty view after %q", k.String())
		}
	}
	return m
}

func TestTUIRendersEveryTabAtSeveralSizes(t *testing.T) {
	want := map[tab]string{tabSites: "demo", tabWorktrees: "feature", tabDoctor: "checks pass"}
	for _, size := range [][2]int{{120, 30}, {80, 20}, {60, 14}} {
		m := driveModel(t)
		next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = next.(model)
		for i := 0; i < 4; i++ {
			view := m.View()
			if s, ok := want[m.tab]; ok && !strings.Contains(view, s) {
				t.Errorf("%dx%d tab %d: missing %q", size[0], size[1], m.tab, s)
			}
			m = press(t, m, keyDown, keyUp, runes("j"), runes("k"), runes("G"), tea.KeyMsg{Type: tea.KeyEnd}, tea.KeyMsg{Type: tea.KeyHome}, tea.KeyMsg{Type: tea.KeyPgDown}, tea.KeyMsg{Type: tea.KeyPgUp})
			m = press(t, m, keyTab)
		}
		if m.tab != tabSites {
			t.Fatalf("tab cycle ended on %d", m.tab)
		}
		m = press(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
		if m.tab != tabDoctor {
			t.Fatalf("shift+tab went to %d", m.tab)
		}
	}
}

func TestTUIPromptsOpenAndCancel(t *testing.T) {
	cases := []struct {
		tab  tab
		keys string
	}{
		{tabSites, "nipdamQ"},
		{tabWorktrees, "a"},
		{tabRuntimes, "i"},
	}
	for _, c := range cases {
		for _, key := range c.keys {
			m := driveModel(t)
			m.tab = c.tab
			m = press(t, m, runes(string(key)))
			if m.mode != modeInput {
				t.Logf("tab %d key %q: mode %d msg %q", c.tab, key, m.mode, m.msg)
				continue
			}
			m = press(t, m, runes("ab"), tea.KeyMsg{Type: tea.KeySpace}, keyBack, keyTab, keyEsc)
			if m.mode != modeBrowse {
				t.Errorf("tab %d key %q: esc left mode %d", c.tab, key, m.mode)
			}
		}
	}
}

func TestTUIConfirmsCanBeDeclined(t *testing.T) {
	cases := []struct {
		tab tab
		key string
	}{
		{tabSites, "D"},
		{tabWorktrees, "D"},
		{tabRuntimes, "b"},
		{tabRuntimes, "m"},
		{tabRuntimes, "h"},
	}
	for _, c := range cases {
		for _, decline := range []tea.KeyMsg{runes("n"), keyEsc} {
			m := driveModel(t)
			m.tab = c.tab
			m = press(t, m, runes(c.key))
			if m.mode != modeConfirm {
				t.Logf("tab %d key %s: mode %d msg %q", c.tab, c.key, m.mode, m.msg)
				continue
			}
			m = press(t, m, runes("z"), decline)
			if m.mode != modeBrowse || m.confirm != "" {
				t.Errorf("tab %d key %s: decline left mode %d", c.tab, c.key, m.mode)
			}
		}
	}
	m := driveModel(t)
	if m.store.Site("demo") == nil {
		t.Fatal("a declined delete removed the site")
	}
}

func TestTUISearchFiltersSites(t *testing.T) {
	m := driveModel(t)
	m = press(t, m, runes("/"), runes("de"), keyDown, keyUp, keyBack, runes("e"))
	if !strings.Contains(m.View(), "demo") {
		t.Fatal("filter hid the matching site")
	}
	m = press(t, m, keyEnter)
	if m.mode != modeBrowse {
		t.Fatalf("enter left mode %d", m.mode)
	}
	m = press(t, m, runes("/"), runes("zzz"), keyEsc)
	if m.mode != modeBrowse {
		t.Fatalf("esc left mode %d", m.mode)
	}
}

func TestTUIMediaPromptSavesFallback(t *testing.T) {
	m := driveModel(t)
	m = press(t, m, runes("m"))
	if m.mode != modeInput {
		t.Fatalf("media key opened mode %d (%s)", m.mode, m.msg)
	}
	m = press(t, m, runes("https://cdn.example"), keyEnter)
	if got := m.store.Site("demo").MediaFallback; got != "https://cdn.example" {
		t.Fatalf("fallback = %q (%s)", got, m.msg)
	}
}

func TestTUIHelpBusyAndMessages(t *testing.T) {
	m := driveModel(t)
	m = press(t, m, runes("?"))
	if m.mode != modeHelp {
		t.Fatalf("? opened mode %d", m.mode)
	}
	m = press(t, m, keyEsc, runes("u"), runes("r"))
	if m.msg == "" {
		t.Fatal("refresh set no message")
	}
	m.mode, m.busy = modeBusy, "copying files"
	if !strings.Contains(m.View(), "copying files") {
		t.Fatal("busy label not drawn")
	}
	next, _ := m.Update(spinTickMsg{})
	m = next.(model)
	m = press(t, m, runes("q")) // ignored while busy
	if m.quitting {
		t.Fatal("q quit during a busy action")
	}
	next, _ = m.Update(actionMsg{done: true, err: os.ErrPermission})
	m = next.(model)
	if m.mode != modeBrowse || !m.msgErr {
		t.Fatalf("failed action: mode %d err %t", m.mode, m.msgErr)
	}
	next, _ = m.Update(refreshMsg{})
	m = next.(model)
	next, _ = m.Update(runtimeHealthMsg{alive: map[string]bool{"demo": true}, health: healthSnapshot{db: true, front: "router"}})
	m = next.(model)
	if !strings.Contains(m.View(), "demo") {
		t.Fatal("site row missing after health update")
	}
	m = press(t, m, runes("q"))
	if !m.quitting {
		t.Fatal("q did not quit")
	}
}
