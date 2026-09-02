package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// ---------- look ----------
//
// The screen is an operator's panel for a rack of local sites: ports, PHP
// pools, sockets. So structure is steel blue and quiet, and the one loud
// element is the lamp gutter — a dot per row, lit when that thing is actually
// serving. It reads at a glance and it repeats on every tab, which is why the
// tables no longer carry a "STATE" word column.
//
// Colours are adaptive where text is involved so a light terminal stays
// readable; the lamps stay fixed, because a signal lamp that changes hue with
// the theme is not a signal lamp.

var (
	cInk   = lipgloss.AdaptiveColor{Light: "235", Dark: "253"}
	cDim   = lipgloss.AdaptiveColor{Light: "243", Dark: "245"}
	cSteel = lipgloss.AdaptiveColor{Light: "23", Dark: "73"} // phosphor teal, a broadcast monitor
	cLamp  = lipgloss.Color("78")                            // serving
	cOff   = lipgloss.Color("240")                           // parked
	cAlert = lipgloss.Color("203")                           // broken
	cAmber = lipgloss.Color("178")                           // needs attention / in flight
	cRule  = lipgloss.AdaptiveColor{Light: "253", Dark: "236"}
)

var (
	stName      = lipgloss.NewStyle().Bold(true).Foreground(cInk)
	stVersion   = lipgloss.NewStyle().Foreground(cDim)
	stTabOn     = lipgloss.NewStyle().Bold(true).Foreground(cInk)
	stTabOff    = lipgloss.NewStyle().Foreground(cDim)
	stRail      = lipgloss.NewStyle().Foreground(cSteel)
	stRailFaint = lipgloss.NewStyle().Foreground(cRule)
	stHead      = lipgloss.NewStyle().Foreground(cDim)
	stSelBar    = lipgloss.NewStyle().Foreground(cSteel).Bold(true)
	stSelRow    = lipgloss.NewStyle().Bold(true).Foreground(cInk)
	stRow       = lipgloss.NewStyle().Foreground(cInk)
	stDim       = lipgloss.NewStyle().Foreground(cDim)
	stKey       = lipgloss.NewStyle().Bold(true).Foreground(cSteel)
	stErr       = lipgloss.NewStyle().Foreground(cAlert)
	stOK        = lipgloss.NewStyle().Foreground(cLamp)
	stWarn      = lipgloss.NewStyle().Foreground(cAmber)
	stPanel     = lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).BorderForeground(cSteel).Padding(0, 1)
	stModal     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cSteel).Padding(0, 1)
	stLabel     = lipgloss.NewStyle().Foreground(cDim).Width(8).Align(lipgloss.Right)
	stCapsule   = lipgloss.NewStyle().Foreground(cInk)
)

// lamp is the signature element: one dot, three states, same meaning everywhere.
func lamp(on bool) string {
	if on {
		return lipgloss.NewStyle().Foreground(cLamp).Render("●")
	}
	return lipgloss.NewStyle().Foreground(cOff).Render("●")
}

// lampFor maps a doctor status onto the same three lamps.
func lampFor(status string) string {
	switch status {
	case "ok":
		return lamp(true)
	case "warn":
		return lipgloss.NewStyle().Foreground(cAmber).Render("●")
	default:
		return lipgloss.NewStyle().Foreground(cAlert).Render("●")
	}
}

// ---------- model ----------

type tab int

const (
	tabSites tab = iota
	tabWorktrees
	tabRuntimes
	tabDoctor
)

type mode int

const (
	modeBrowse mode = iota
	modeInput
	modeConfirm
	modeBusy
	modeHelp
	modeSearch
)

type inputTarget int

const (
	inputNone          inputTarget = iota
	inputNewWhere                  // step 1: the shared directory, or a path of your own
	inputSitesDir                  // change the shared directory itself
	inputMediaFallback             // where missing uploads come from
	inputNewDir                    // step 1b: where the site lives (tab-completes)
	inputNewFresh                  // step 2: install WordPress here, or just point at it
	inputNewName                   // step 3: name/slug
	inputNewDomain
	inputCreateName
	inputCreateDomain
	inputCreatePHP
	inputWorktreeBranch
	inputSwitchPHP
	inputSetDomain
	inputSQL
	inputInstallPHP
	inputImportSource
	inputImportName
	inputImportDomain
)

type inputSpec struct {
	target inputTarget
	prompt string
	value  string
	hint   string
	slug   string // site context when relevant
	// wizard state, carried across the steps of "new site"
	dir   string
	fresh bool
	name  string
	// note is shown under the prompt: what we found at the path, what will happen
	note string
}

type model struct {
	store    *Store
	engine   *Engine
	tab      tab
	siteCur  int
	wtCur    int
	rtCur    int
	docCur   int
	mode     mode
	input    inputSpec
	confirm  string
	confirmA func() error
	// confirmLabel is what the busy line says while a confirmed action runs.
	confirmLabel string
	// pendingCmd carries a command out of applyInput, which can only return an
	// error, to the key handler that must hand it to bubbletea.
	pendingCmd tea.Cmd
	// inv is a snapshot of the toolchain: rendering used to read the store
	// directly, which a background `brew install` writes to at the same time.
	inv Inventory
	// A long action runs in a goroutine and reports back through progress; the
	// UI keeps rendering while it does. Doing the work inline froze the whole
	// screen, because bubbletea only paints once Update returns.
	busyDetail string
	busySince  time.Time
	spin       int
	progress   chan actionMsg
	busy       string
	msg        string
	msgErr     bool
	sites      []*Site
	doctor     *DoctorReport
	width      int
	height     int
	health     healthSnapshot
	sizes      map[string]string
	quitting   bool
	// siteFilter is the Sites-tab search. The full catalog stays in sites;
	// visibleSites() is what the cursor and the table walk.
	siteFilter string
	// siteTop is the first table row drawn on the Sites tab. The table used to
	// render every site, so on a short terminal the detail panel and the footer
	// were pushed off the bottom and the cursor could sit somewhere unseen.
	siteTop int
	// docTop is the first finding drawn on the Doctor tab — same job as
	// siteTop, for a report that outgrows the window (site/dns/media checks
	// arrive per site, so with a rack of sites it always does).
	docTop int
	// pageRows is the size of the last window the Sites table drew, so page
	// keys move by a screenful of the list actually on screen.
	pageRows int
}

// healthSnapshot is the stack state the header lamps read from, sampled on
// refresh rather than per render.
type healthSnapshot struct {
	db, http, https, api bool
	front                string
}

type refreshMsg struct{}

func initialModel() model {
	store, err := OpenStore()
	m := model{}
	if err != nil {
		m.msg = err.Error()
		m.msgErr = true
		return m
	}
	EnsureInventory(store)
	m.store = store
	m.engine = NewEngine(store)
	m.refresh()
	return m
}

// refresh re-reads the store and samples liveness once. The lamps must not be
// computed in View: a render happens on every keypress, and dialling four ports
// per frame turns typing into stutter.
func (m *model) refresh() {
	// Pick up writes from other processes first: the CLI, the daemon and any
	// agent all share sites.json, and the TUI is usually the long-lived reader.
	m.store.ReloadIfChanged()
	m.sites = m.store.Sites()
	m.inv = *m.store.Inventory()
	// Clamp each cursor to its own list: deleting the last row otherwise leaves
	// a cursor pointing past the end.
	for _, t := range []tab{tabSites, tabWorktrees, tabRuntimes, tabDoctor} {
		c, n := m.cursorFor(t), m.rowsFor(t)
		if *c >= n {
			*c = n - 1
		}
		if *c < 0 {
			*c = 0
		}
	}
	m.health = healthSnapshot{
		db:    m.engine.DBRunning(),
		http:  portOpen(DefaultHTTPPort),
		https: portOpen(DefaultHTTPSPort),
		api:   portOpen(DefaultAPIPort),
		front: FrontKind(m.store),
	}
}

// siteSize reports a site's disk usage, once per slug per session. Measuring it
// walks the whole tree, and the panel re-renders on every keypress: an imported
// multi-gigabyte checkout would turn arrow keys into a stall.
func (m *model) siteSize(slug string) string {
	if m.sizes == nil {
		m.sizes = map[string]string{}
	}
	if v, ok := m.sizes[slug]; ok {
		return v
	}
	v := SiteDirSize(slug)
	m.sizes[slug] = v
	return v
}

// phpInUse is the set of PHP versions actually serving something right now, so
// the lamp on the Runtimes tab means "in use" rather than merely "installed" —
// a green dot that is always green tells you nothing.
func (m model) phpInUse() map[string]bool {
	used := map[string]bool{}
	for _, s := range m.sites {
		if m.engine.FPMRunning(s.Slug) {
			used[s.PHPVersion] = true
		}
	}
	return used
}

const refreshInterval = 5 * time.Second

func refreshTick() tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return refreshMsg{} })
}

func (m model) Init() tea.Cmd { return refreshTick() }

func (m *model) setMsg(s string, isErr bool) { m.msg = s; m.msgErr = isErr }

// visibleSites is the Sites table: the full catalog when there is no filter,
// otherwise a case-insensitive substring match on name, slug, domain, aliases
// and paths. Worktrees still walk m.sites so a filter here cannot hide a preview.
func (m model) visibleSites() []*Site {
	q := strings.ToLower(strings.TrimSpace(m.siteFilter))
	if q == "" {
		return m.sites
	}
	out := make([]*Site, 0, len(m.sites))
	for _, s := range m.sites {
		if siteMatches(s, q) {
			out = append(out, s)
		}
	}
	return out
}

func siteMatches(s *Site, q string) bool {
	if s == nil || q == "" {
		return true
	}
	if strings.Contains(strings.ToLower(s.Slug), q) ||
		strings.Contains(strings.ToLower(s.Name), q) ||
		strings.Contains(strings.ToLower(s.Domain), q) ||
		strings.Contains(strings.ToLower(s.PHPVersion), q) ||
		strings.Contains(strings.ToLower(s.WPDir), q) ||
		strings.Contains(strings.ToLower(s.WorkDir), q) {
		return true
	}
	for _, a := range s.Aliases {
		if strings.Contains(strings.ToLower(a), q) {
			return true
		}
	}
	return false
}

func (m *model) clampSiteCur() {
	n := len(m.visibleSites())
	if m.siteCur >= n {
		m.siteCur = n - 1
	}
	if m.siteCur < 0 {
		m.siteCur = 0
	}
	if m.siteTop > m.siteCur {
		m.siteTop = m.siteCur
	}
	if m.siteTop < 0 {
		m.siteTop = 0
	}
}

func (m model) currentSite() *Site {
	sites := m.visibleSites()
	if len(sites) == 0 || m.siteCur >= len(sites) {
		return nil
	}
	return sites[m.siteCur]
}

// cursorFor hands out the cursor belonging to a tab. One shared index used to
// do three jobs at once — it selected a site, scoped the Worktrees tab, and
// indexed the worktree rows — so moving it on Worktrees re-scoped the list and
// left every action pointing past the end of it.
func (m *model) cursorFor(t tab) *int {
	switch t {
	case tabWorktrees:
		return &m.wtCur
	case tabRuntimes:
		return &m.rtCur
	case tabDoctor:
		return &m.docCur
	default:
		return &m.siteCur
	}
}

// rowsFor is how many selectable rows a tab has, so a cursor cannot run past
// its own list.
func (m model) rowsFor(t tab) int {
	switch t {
	case tabWorktrees:
		return len(m.allWorktrees())
	case tabRuntimes:
		return len(m.inv.PHPs)
	case tabDoctor:
		if m.doctor != nil {
			return len(m.doctor.Findings)
		}
		return 0
	default:
		return len(m.visibleSites())
	}
}

// allWorktrees lists every preview across every site, ordered site then branch.
// The tab used to show only the site highlighted on another tab, which meant the
// list changed under you and nothing on screen said why.
func (m model) allWorktrees() []*Worktree {
	out := make([]*Worktree, 0, len(m.store.Data.Worktrees))
	for _, s := range m.sites {
		out = append(out, m.store.WorktreesFor(s.Slug)...)
	}
	return out
}

// currentWorktree is the highlighted preview row, nil when the list is empty.
func (m model) currentWorktree() *Worktree {
	wts := m.allWorktrees()
	if m.wtCur >= len(wts) {
		return nil
	}
	return wts[m.wtCur]
}

// actionMsg is a step or the end of a long action, sent from its goroutine.
type actionMsg struct {
	stage  string
	detail string
	done   bool
	result string
	err    error
	// payload carries a value produced in the goroutine. Assigning to the model
	// from there would write to a copy nobody renders.
	payload any
}

// spinTickMsg advances the spinner while an action runs.
type spinTickMsg struct{}

// runAction starts work in the background and returns the commands that keep the
// UI alive: one waiting on the action's next message, one ticking the spinner.
// fn reports progress through cb, which is safe to call from the goroutine.
func (m model) runAction(label string, fn func(cb func(stage, detail string)) (string, error)) (model, tea.Cmd) {
	return m.runActionWith(label, func(cb func(string, string)) (string, any, error) {
		msg, err := fn(cb)
		return msg, nil, err
	})
}

// runActionWith is runAction for work that produces a value the UI needs back.
func (m model) runActionWith(label string, fn func(cb func(stage, detail string)) (string, any, error)) (model, tea.Cmd) {
	ch := make(chan actionMsg, 64)
	m.mode = modeBusy
	m.busy = label
	m.busyDetail = ""
	m.busySince = time.Now()
	m.spin = 0
	m.progress = ch
	go func() {
		defer close(ch)
		cb := func(stage, detail string) {
			// Never block the work on a UI that is slow to drain.
			select {
			case ch <- actionMsg{stage: stage, detail: detail}:
			default:
			}
		}
		result, payload, err := fn(cb)
		ch <- actionMsg{done: true, result: result, err: err, payload: payload}
	}()
	return m, tea.Batch(waitForAction(ch), spinTick())
}

// startBackground is runAction for callers that cannot return a command — the
// input handlers, which report an error and nothing else. The command is stashed
// and picked up by the key handler.
func (m *model) startBackground(label string, fn func(cb func(stage, detail string)) (string, error)) {
	next, cmd := m.runAction(label, fn)
	*m = next
	m.pendingCmd = cmd
}

// waitForAction blocks in bubbletea's command goroutine, not in Update.
func waitForAction(ch chan actionMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			// Channel closed without a done message: treat as finished rather
			// than leaving the UI spinning forever.
			return actionMsg{done: true}
		}
		return msg
	}
}

func spinTick() tea.Cmd {
	return tea.Tick(110*time.Millisecond, func(time.Time) tea.Msg { return spinTickMsg{} })
}

// spinFrames is a quiet spinner: the lamp language of the rest of the UI, moving.
var spinFrames = []string{"◐", "◓", "◑", "◒"}

// ---------- update ----------

// Update runs the message through the handlers, then re-lays the Sites window
// on whatever model comes back. The window has to be recomputed here, not in
// View: View takes a value receiver, so a scroll position worked out while
// rendering is written to a copy the next frame never sees.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	if nm, ok := next.(model); ok {
		nm.layout()
		return nm, cmd
	}
	return next, cmd
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case spinTickMsg:
		if m.mode != modeBusy {
			return m, nil
		}
		m.spin++
		return m, spinTick()
	case refreshMsg:
		// CLI / MCP / another TUI can mutate sites.json while this session
		// sits idle. Reload quietly: a "refreshed" toast every five seconds
		// would be the whole UI.
		if !m.quitting {
			m.refresh()
		}
		return m, refreshTick()
	case actionMsg:
		if msg.done {
			m.mode = modeBrowse
			m.busy, m.busyDetail, m.progress = "", "", nil
			if rep, ok := msg.payload.(*DoctorReport); ok {
				m.doctor = rep
			}
			if msg.err != nil {
				m.setMsg(msg.err.Error(), true)
			} else if msg.result != "" {
				m.setMsg(msg.result, false)
			}
			m.refresh()
			return m, nil
		}
		// A step: show it and keep listening.
		m.busyDetail = strings.TrimSpace(msg.stage + " " + msg.detail)
		return m, waitForAction(m.progress)
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeInput:
		return m.handleInputKey(k)
	case modeConfirm:
		switch k.String() {
		case "y":
			action, label := m.confirmA, m.confirmLabel
			m.confirm, m.confirmA, m.confirmLabel = "", nil, ""
			if label == "" {
				label = "working"
			}
			return m.runAction(label, func(func(string, string)) (string, error) {
				if err := action(); err != nil {
					return "", err
				}
				return "done", nil
			})
		case "n", "esc":
			m.mode = modeBrowse
			m.confirm = ""
			return m, nil
		}
		return m, nil
	case modeBusy:
		// Quitting must still work; everything else waits, since acting on a
		// half-changed site is how people end up with two problems.
		if k.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	case modeHelp:
		switch k.String() {
		case "?", "esc", "q", "enter":
			m.mode = modeBrowse
		}
		return m, nil
	case modeSearch:
		return m.handleSearchKey(k)
	}

	// browse mode
	switch k.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "tab", "right":
		m.tab = tab((int(m.tab) + 1) % 4)
		m.msg = ""
		if m.tab == tabDoctor && m.doctor == nil {
			// First visit: run the checks off the UI loop rather than let
			// the view block on them.
			return m.runDoctor("running checks")
		}
		return m, nil
	case "shift+tab", "left":
		m.tab = tab((int(m.tab) + 3) % 4)
		if m.tab == tabDoctor && m.doctor == nil {
			return m.runDoctor("running checks")
		}
		return m, nil
	case "up", "k":
		if c := m.cursorFor(m.tab); *c > 0 {
			*c--
		}
		return m, nil
	case "down", "j":
		if c, n := m.cursorFor(m.tab), m.rowsFor(m.tab); *c < n-1 {
			*c++
		}
		return m, nil
	case "pgup", "ctrl+u":
		c := m.cursorFor(m.tab)
		*c -= m.pageStep()
		if *c < 0 {
			*c = 0
		}
		return m, nil
	case "pgdown", "ctrl+d":
		c, n := m.cursorFor(m.tab), m.rowsFor(m.tab)
		*c += m.pageStep()
		if *c > n-1 {
			*c = n - 1
		}
		if *c < 0 {
			*c = 0
		}
		return m, nil
	case "home":
		*m.cursorFor(m.tab) = 0
		return m, nil
	case "end":
		if n := m.rowsFor(m.tab); n > 0 {
			*m.cursorFor(m.tab) = n - 1
		}
		return m, nil
	case "r":
		// Refreshing is a read. This used to Save() as well, which wrote the
		// TUI's in-memory snapshot back over the file and silently erased
		// anything the CLI, the daemon or an agent had changed since the TUI
		// opened — sites turning up "stopped" for no reason was this.
		if m.tab == tabDoctor {
			// This global match also swallowed the Doctor tab's advertised
			// re-run key, so "r" must mean the checks here.
			return m.runDoctor("re-running checks")
		}
		m.refresh()
		m.setMsg("refreshed", false)
		return m, nil
	case "?":
		m.mode = modeHelp
		return m, nil
	}

	switch m.tab {
	case tabSites:
		return m.handleSitesKey(k)
	case tabWorktrees:
		return m.handleWorktreesKey(k)
	case tabRuntimes:
		return m.handleRuntimesKey(k)
	case tabDoctor:
		return m.handleDoctorKey(k)
	}
	return m, nil
}

func (m model) startInput(t inputTarget, prompt, hint, slug string) model {
	m.mode = modeInput
	m.input = inputSpec{target: t, prompt: prompt, hint: hint, slug: slug}
	return m
}

func (m model) handleInputKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "ctrl+c":
		m.mode = modeBrowse
		m.input = inputSpec{}
		return m, nil
	case "enter":
		spec := m.input
		m.mode = modeBrowse
		m.input = inputSpec{}
		if err := m.applyInput(spec); err != nil {
			m.setMsg(err.Error(), true)
		}
		// A backgrounded action refreshes when it finishes; refreshing now would
		// only re-read state it is in the middle of changing.
		if cmd := m.pendingCmd; cmd != nil {
			m.pendingCmd = nil
			return m, cmd
		}
		m.refresh()
		return m, nil
	case "tab":
		// Completion only means something for a path. Elsewhere tab would
		// otherwise leak through to the tab bar and abandon a half-typed answer.
		switch m.input.target {
		case inputNewDir, inputSitesDir:
			v, note := completeDir(m.input.value)
			m.input.value, m.input.note = v, note
		case inputImportSource:
			v, note := completeImportSource(m.input.value)
			m.input.value, m.input.note = v, note
		case inputMediaFallback:
			// Nothing to complete on disk, but the .htaccess origin is the answer
			// nine times out of ten — so tab fills it in.
			if h := m.engine.MediaFallbackHint(m.input.slug); h != "" {
				m.input.value = h
			}
		}
		return m, nil
	case "backspace":
		if len(m.input.value) > 0 {
			m.input.value = m.input.value[:len(m.input.value)-1]
			if m.input.target == inputNewDir || m.input.target == inputSitesDir || m.input.target == inputImportSource {
				m.input.note = ""
			}
		}
		return m, nil
	case "space":
		m.input.value += " "
		return m, nil
	default:
		// Take the runes, not the key name: a paste or a fast typist arrives as
		// one multi-rune message, and matching on len(String())==1 silently threw
		// the whole thing away — pasting a path is the obvious way to answer a
		// "which directory" prompt.
		if k.Type == tea.KeyRunes && !k.Alt {
			m.input.value += string(k.Runes)
			if m.input.target == inputNewDir {
				m.input.note = ""
			}
		}
		return m, nil
	}
}

func (m *model) applyInput(spec inputSpec) error {
	val := strings.TrimSpace(spec.value)
	switch spec.target {
	case inputNewWhere:
		if strings.HasPrefix(strings.ToLower(val), "d") {
			// Change the shared directory itself, then come back to this question.
			next := m.startInput(inputSitesDir, "keep sites in", "⇥ completes · applies to new sites only", "")
			next.input.value = shortHome(m.store.SitesDir())
			*m = next
			return nil
		}
		if strings.HasPrefix(strings.ToLower(val), "n") {
			*m = m.startInput(inputNewDir, "where should the site live?",
				"⇥ completes · absolute or ~ paths both work", "")
			return nil
		}
		// The shared directory: the site is named, and the path follows from it.
		next := m.startInput(inputNewName, "name for this site",
			"becomes the slug, the folder and the default domain", "")
		next.input.fresh, next.input.note = true, m.newSiteNote()
		*m = next
		return nil
	case inputMediaFallback:
		got, err := m.engine.SetMediaFallback(spec.slug, val)
		if err != nil {
			return err
		}
		if got == "" {
			m.setMsg("media fallback off — missing uploads 404", false)
			return nil
		}
		m.setMsg("missing uploads redirect to "+got, false)
		return nil
	case inputSitesDir:
		if err := m.store.SetSitesDir(val); err != nil {
			return err
		}
		next := m.startInput(inputNewWhere, "new site in "+shortHome(m.store.SitesDir())+"?",
			"enter or y → keep them together · n → a path for this one · d → change this directory", "")
		next.input.note = m.newSiteNote()
		*m = next
		return nil
	case inputNewDir:
		dir, err := ResolveDir(val)
		if err != nil {
			return err
		}
		if st, err := os.Stat(dir); err == nil && !st.IsDir() {
			return fmt.Errorf("%s is a file, not a directory", shortHome(dir))
		}
		// The shared resolver, not the narrow one: SiteForPath alone missed a
		// directory that *contains* sites, which let you attach a second site on
		// top of one you already had.
		if s, how, _, below := m.engine.ResolvePath(dir); s != nil {
			return fmt.Errorf("%s already serves that directory (%s)", s.Slug, how)
		} else if len(below) > 1 {
			names := make([]string, 0, len(below))
			for _, b := range below {
				names = append(names, b.Slug)
			}
			return fmt.Errorf("%s holds several sites: %s", shortHome(dir), strings.Join(names, " "))
		}
		spec.dir = dir
		// What is already there decides which questions are worth asking.
		switch {
		case DirUsable(dir):
			// Empty or absent: a fresh install is possible, so offer it.
			what := "is empty"
			if _, err := os.Stat(dir); err != nil {
				what = "does not exist yet"
			}
			next := m.startInput(inputNewFresh, "install WordPress into it?",
				"enter or y → installs into "+shortHome(filepath.Join(dir, "wp"))+
					" · n → serve the folder as-is with an empty database", "")
			next.input.dir, next.input.note = dir, shortHome(dir)+" "+what
			*m = next
			return nil
		default:
			// Files are already there: installing over them is not on offer.
			docroot := DocrootFor(dir)
			note := "serving " + shortHome(docroot)
			if docroot == dir && !fileExists(filepath.Join(dir, "wp-load.php")) {
				note = "no WordPress found — serving " + shortHome(dir) + " as-is"
			}
			next := m.startInput(inputNewName, "name for this site",
				"becomes the slug and the default domain", "")
			next.input.dir, next.input.fresh, next.input.note = dir, false, note
			next.input.value = defaultSiteName(dir)
			*m = next
			return nil
		}
	case inputNewFresh:
		fresh := !strings.HasPrefix(strings.ToLower(val), "n")
		next := m.startInput(inputNewName, "name for this site",
			"becomes the slug and the default domain", "")
		next.input.dir, next.input.fresh = spec.dir, fresh
		if fresh {
			next.input.note = "fresh WordPress into " + shortHome(filepath.Join(spec.dir, "wp"))
		} else {
			next.input.note = "serving " + shortHome(spec.dir) + " with an empty database"
		}
		next.input.value = defaultSiteName(spec.dir)
		*m = next
		return nil
	case inputNewName:
		if val == "" {
			return fmt.Errorf("name required")
		}
		slug, err := SanitizeName(val)
		if err != nil {
			return err
		}
		dir, note := spec.dir, spec.note
		if dir == "" {
			// Came in via the shared directory: the name decides the folder.
			dir = m.store.SiteDirFor(slug)
			if !DirUsable(dir) {
				return fmt.Errorf("%s already exists — pick another name, or answer n to choose a path", shortHome(dir))
			}
			note = "fresh WordPress into " + shortHome(filepath.Join(dir, "wp"))
		}
		next := m.startInput(inputNewDomain, "domain for "+slug, "must resolve locally — /etc/hosts is updated for you", "")
		next.input.dir, next.input.fresh, next.input.name = dir, spec.fresh, val
		next.input.note = note
		next.input.value = m.store.DefaultDomain(slug)
		*m = next
		return nil
	case inputNewDomain:
		if val == "" {
			return fmt.Errorf("domain required")
		}
		if spec.fresh {
			opts := CreateOpts{Name: spec.name, Dir: spec.dir, Domain: val}
			m.startBackground("installing WordPress into "+shortHome(spec.dir),
				func(cb func(string, string)) (string, error) {
					opts.Progress = cb
					site, err := m.engine.CreateSite(opts)
					if err != nil {
						return "", err
					}
					return "created " + BareURL(site) + "  admin=" + site.AdminUser + "/" + site.AdminPass, nil
				})
			return nil
		}
		opts := AttachOpts{Name: spec.name, Dir: spec.dir, Domain: val}
		m.startBackground("attaching "+shortHome(spec.dir), func(cb func(string, string)) (string, error) {
			var config string
			opts.Progress = func(stage, detail string) {
				if stage == "config" {
					config = detail
				}
				cb(stage, detail)
			}
			site, err := m.engine.AttachSite(opts)
			if err != nil {
				return "", err
			}
			msg := "attached " + BareURL(site) + "  db " + site.DBName + " / " + site.DBUser
			if config != "" {
				msg += "  — " + config
			}
			return msg, nil
		})
		return nil
	case inputCreateName:
		if val == "" {
			return fmt.Errorf("name required")
		}
		name := val
		m.startBackground("creating "+name, func(cb func(string, string)) (string, error) {
			site, err := m.engine.CreateSite(CreateOpts{Name: name, Progress: cb})
			if err != nil {
				return "", err
			}
			return "created " + BareURL(site) + "  admin=" + site.AdminUser + "/" + site.AdminPass, nil
		})
	case inputCreateDomain:
		slug, domain := spec.slug, val
		m.startBackground("changing "+slug+" to "+domain, func(func(string, string)) (string, error) {
			if err := m.engine.SetDomain(slug, domain); err != nil {
				return "", err
			}
			return slug + " is now " + domain, nil
		})
	case inputCreatePHP, inputSwitchPHP:
		version := NormalizePHPVersion(val)
		if version == "" {
			return fmt.Errorf("version required")
		}
		slug := spec.slug
		label := "switching " + slug + " to php " + version
		if m.inv.FindPHP(version) == nil {
			label = "installing php " + version + " for " + slug + " — this can take minutes"
		}
		m.startBackground(label, func(cb func(string, string)) (string, error) {
			// install=true, tap allowed: someone typed this version at the
			// keyboard, and for 7.4 or 8.0 the tap is the only place it lives.
			if err := m.engine.SwitchPHPEnsure(slug, version, true, true, func(line string) { cb("brew", line) }); err != nil {
				return "", err
			}
			return "php " + version + " active on " + slug, nil
		})
	case inputSetDomain:
		if val == "" {
			return fmt.Errorf("domain required")
		}
		slug := spec.slug
		// The rename rewrites URLs across every table, so it is seconds of work,
		// not milliseconds: the UI has to stay alive through it.
		m.startBackground("changing "+slug+" to "+val, func(func(string, string)) (string, error) {
			if err := m.engine.SetDomain(slug, val); err != nil {
				return "", err
			}
			return slug + " is now " + val, nil
		})
		return nil
	case inputWorktreeBranch:
		if val == "" {
			return fmt.Errorf("branch required")
		}
		slug, branch := spec.slug, val
		m.startBackground("adding preview "+branch, func(func(string, string)) (string, error) {
			w, err := m.engine.AddWorktree(slug, branch)
			if err != nil {
				return "", err
			}
			return "preview running: " + BareDomainURL(w.Domain), nil
		})
	case inputSQL:
		if val == "" {
			return nil
		}
		if err := m.engine.EnsureDB(); err != nil {
			return err
		}
		out, err := m.engine.DB(val)
		m.setMsg(strings.TrimSpace(tail(out, 160)), err != nil)
		return err
	case inputImportSource:
		if val == "" {
			return fmt.Errorf("a LocalWP site name or a WordPress directory")
		}
		next := m.startInput(inputImportName, "name for the imported site",
			"becomes the slug, the folder name and the default domain", "")
		next.input.dir = val
		next.input.value = defaultImportName(val)
		next.input.note = importSourceNote(val)
		*m = next
		return nil
	case inputImportName:
		if val == "" {
			return fmt.Errorf("name required")
		}
		slug, err := SanitizeName(val)
		if err != nil {
			return err
		}
		next := m.startInput(inputImportDomain, "domain for "+slug, "must resolve locally — /etc/hosts is updated for you", "")
		next.input.dir, next.input.name = spec.dir, val
		next.input.value = m.store.DefaultDomain(slug)
		*m = next
		return nil
	case inputImportDomain:
		if val == "" {
			return fmt.Errorf("domain required")
		}
		src, name, domain := spec.dir, spec.name, val
		m.startBackground("importing "+src, func(cb func(string, string)) (string, error) {
			site, err := m.engine.ImportSite(ImportOpts{
				Source: src, Name: name, Domain: domain, Progress: cb,
			})
			if err != nil {
				return "", err
			}
			return "imported " + BareURL(site) + "  db " + site.DBName, nil
		})
		return nil
	case inputInstallPHP:
		version := NormalizePHPVersion(val)
		if version == "" {
			version = latestBrewPHP()
		}
		m.startBackground("installing php "+version+" — this can take minutes",
			func(cb func(string, string)) (string, error) {
				// Third-party tap allowed here: the person typing the version is
				// the one who gets to decide, and for 7.4 or 8.0 there is no
				// other source left.
				if err := InstallPHP(m.store, version, true, func(line string) { cb("brew", line) }); err != nil {
					return "", err
				}
				DiscoverInventory(m.store)
				_ = m.store.Save()
				return "php " + version + " installed", nil
			})
		return nil
	}
	return nil
}

func (m model) handleSitesKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	site := m.currentSite()
	switch k.String() {
	case "/":
		m.mode = modeSearch
		m.msg = ""
		return m, nil
	case "n":
		// Settings live in a file five processes share. A TUI left open since
		// before `agent-local sites-dir` ran would otherwise offer the old
		// directory, which is exactly how this looked like it had not applied.
		m.store.ReloadIfChanged()
		next := m.startInput(inputNewWhere, "new site in "+shortHome(m.store.SitesDir())+"?",
			"enter or y → keep them together · n → a path for this one · d → change this directory", "")
		next.input.note = m.newSiteNote()
		return next, nil
	case "i":
		next := m.startInput(inputImportSource, "import from",
			"LocalWP name or a WordPress directory · ⇥ completes", "")
		next.input.note = localWPImportNote()
		return next, nil
	case "g":
		if site == nil {
			return m, nil
		}
		slug := site.Slug
		return m.runAction("opening database for "+slug, func(func(string, string)) (string, error) {
			s := m.store.Site(slug)
			if s == nil {
				return "", fmt.Errorf("no such site")
			}
			if !m.engine.FPMRunning(slug) {
				if err := m.engine.StartSite(slug); err != nil {
					return "", err
				}
			}
			if _, err := writeAdminerBoot(s); err != nil {
				return "", err
			}
			url := AdminerURL(s.Domain)
			_ = runCmdQuiet("open", url)
			return url, nil
		})
	case "s":
		if site == nil {
			return m, nil
		}
		slug, url := site.Slug, BareURL(site)
		return m.runAction("starting "+slug, func(func(string, string)) (string, error) {
			if err := m.engine.StartSite(slug); err != nil {
				return "", err
			}
			return url, nil
		})
	case "x":
		if site == nil {
			return m, nil
		}
		if err := m.engine.StopSite(site.Slug); err != nil {
			m.setMsg(err.Error(), true)
		} else {
			m.setMsg(site.Slug+" stopped", false)
		}
		return m, nil
	case "R":
		if site == nil {
			return m, nil
		}
		slug := site.Slug
		return m.runAction("restarting "+slug, func(func(string, string)) (string, error) {
			stopErr := m.engine.StopSite(slug)
			// A failed stop used to be discarded: the following start then found
			// the pool still up, returned nil, and reported nothing at all.
			if stopErr != nil && m.engine.FPMRunning(slug) {
				return "", fmt.Errorf("restart failed: %w", stopErr)
			}
			if err := m.engine.StartSite(slug); err != nil {
				return "", err
			}
			return "restarted " + slug, nil
		})
	case "p":
		if site == nil {
			return m, nil
		}
		hint := "installed: " + strings.Join(m.inv.Runtimes(), " ")
		if len(m.inv.BrokenPHPs) > 0 {
			var b []string
			for _, rt := range m.inv.BrokenPHPs {
				b = append(b, rt.Version)
			}
			hint += "   broken: " + strings.Join(b, " ")
		}
		hint += "   any of " + strings.Join(PHPVersions, " ") + " installs on demand"
		return m.startInput(inputSwitchPHP, "php version for "+site.Slug, hint, site.Slug), nil
	case "d":
		if site == nil {
			return m, nil
		}
		return m.startInput(inputSetDomain, "new domain for "+site.Slug, "e.g. "+site.Slug+m.store.Suffix(), site.Slug), nil
	case "D":
		if site == nil {
			return m, nil
		}
		slug := site.Slug
		m.confirmLabel = "deleting " + slug
		m.confirm = "delete site " + slug + " (database + files)?"
		m.confirmA = func() error { return m.engine.DeleteSite(slug, DeleteOpts{}) }
		m.mode = modeConfirm
		return m, nil
	case "a":
		if site == nil {
			return m, nil
		}
		return m.startInput(inputWorktreeBranch, "branch to preview for "+site.Slug,
			"served on its own domain", site.Slug), nil
	case "m":
		if site == nil {
			return m, nil
		}
		hint := "empty turns it off"
		if h := m.engine.MediaFallbackHint(site.Slug); h != "" && h != site.MediaFallback {
			hint = ".htaccess says " + h + " — ⇥ accepts it"
		}
		next := m.startInput(inputMediaFallback, "missing uploads on "+site.Slug+" come from", hint, site.Slug)
		next.input.value = site.MediaFallback
		return next, nil
	case "Q":
		return m.startInput(inputSQL, "SQL on shared db", "e.g. SHOW DATABASES", ""), nil
	case "o":
		if site == nil {
			return m, nil
		}
		if !m.engine.FPMRunning(site.Slug) {
			_ = m.engine.StartSite(site.Slug)
		}
		runCmdQuiet("open", BareURL(site))
		return m, nil
	}
	return m, nil
}

func (m model) handleWorktreesKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	wt := m.currentWorktree()
	switch k.String() {
	case "a":
		// Attach to the highlighted row's site, or to the Sites selection when
		// there is nothing highlighted yet.
		site := m.currentSite()
		if wt != nil {
			site = m.store.Site(wt.Site)
		}
		if site == nil {
			m.setMsg("create a site first — Sites tab, press n", true)
			return m, nil
		}
		return m.startInput(inputWorktreeBranch, "branch to preview for "+site.Slug,
			"served on its own domain", site.Slug), nil
	case "s":
		if wt == nil {
			return m, nil
		}
		id, url := wt.ID, BareDomainURL(wt.Domain)
		return m.runAction("starting "+wt.Branch, func(func(string, string)) (string, error) {
			if err := m.engine.StartWorktree(id); err != nil {
				return "", err
			}
			return "preview running: " + url, nil
		})
	case "x":
		if wt == nil {
			return m, nil
		}
		m.engine.StopWorktree(wt.ID)
		m.setMsg("stopped "+wt.Branch, false)
		return m, nil
	case "R":
		if wt == nil {
			return m, nil
		}
		id, branch := wt.ID, wt.Branch
		return m.runAction("restarting "+branch, func(func(string, string)) (string, error) {
			m.engine.StopWorktree(id)
			if err := m.engine.StartWorktree(id); err != nil {
				return "", err
			}
			return "restarted " + branch, nil
		})
	case "o":
		if wt == nil {
			return m, nil
		}
		if !m.engine.FPMRunning(wt.ID) {
			_ = m.engine.StartWorktree(wt.ID)
		}
		runCmdQuiet("open", BareDomainURL(wt.Domain))
		return m, nil
	case "D":
		if wt == nil {
			return m, nil
		}
		id, branch := wt.ID, wt.Branch
		m.confirmLabel = "removing preview " + branch
		m.confirm = "remove preview " + branch + " (" + id + ")? the branch itself is kept"
		m.confirmA = func() error { return m.engine.RemoveWorktree(id) }
		m.mode = modeConfirm
		return m, nil
	}
	return m, nil
}

func (m model) handleRuntimesKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "i":
		return m.startInput(inputInstallPHP, "install php version", strings.Join(PHPVersions, " ")+"  (7.4 / 8.0 come from the "+phpTap+" tap)", ""), nil
	case "b", "m", "h":
		what := map[string]string{"b": "brew", "m": "mariadb", "h": "apache"}[k.String()]
		m.confirm = "install " + what + " via homebrew?"
		m.confirmA = func() error {
			var err error
			switch what {
			case "brew":
				err = InstallBrew(nil)
			case "mariadb":
				err = InstallMySQL(m.store, nil)
			case "apache":
				err = InstallApache(m.store, nil)
			}
			if err == nil {
				DiscoverInventory(m.store)
				m.store.Save()
			}
			return err
		}
		m.mode = modeConfirm
		return m, nil
	}
	return m, nil
}

// runDoctor probes off the UI loop; the report lands as an actionMsg payload
// (see Update). Rendering must never call Doctor itself: a full probe of
// every site inside View() froze the tab exactly when someone opened it —
// and on a value receiver the report was discarded, so every frame paid for
// it again.
func (m model) runDoctor(label string) (tea.Model, tea.Cmd) {
	store := m.store
	return m.runActionWith(label, func(func(string, string)) (string, any, error) {
		return "checks run", Doctor(store), nil
	})
}

func (m model) handleDoctorKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "f":
		store := m.store
		return m.runActionWith("applying fixes", func(cb func(string, string)) (string, any, error) {
			done := DoctorFix(store, false)
			for _, d := range done {
				cb("fixed", d)
			}
			// Re-check afterwards, so the panel shows the result of the fixes
			// rather than the state that prompted them.
			rep := Doctor(store)
			if len(done) == 0 {
				return "nothing auto-fixable", rep, nil
			}
			return "fixed: " + strings.Join(done, ", "), rep, nil
		})
	}
	return m, nil
}

// handleSearchKey types into the Sites filter. The list updates on every rune
// so a long rack can be cut down without a submit step. enter keeps the
// filter and goes back to browsing; esc throws it away.
func (m model) handleSearchKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.siteFilter = ""
		m.mode = modeBrowse
		m.clampSiteCur()
		return m, nil
	case "enter":
		m.mode = modeBrowse
		return m, nil
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "ctrl+u":
		m.siteFilter = ""
		m.clampSiteCur()
		return m, nil
	case "backspace":
		if m.siteFilter != "" {
			r := []rune(m.siteFilter)
			m.siteFilter = string(r[:len(r)-1])
			m.clampSiteCur()
		}
		return m, nil
	case "up", "k":
		if m.siteCur > 0 {
			m.siteCur--
		}
		return m, nil
	case "down", "j":
		if m.siteCur < len(m.visibleSites())-1 {
			m.siteCur++
		}
		return m, nil
	}
	if k.Type == tea.KeyRunes && !k.Alt {
		m.siteFilter += string(k.Runes)
		m.clampSiteCur()
	}
	return m, nil
}

// ---------- view ----------

func (m model) View() string {
	if m.quitting {
		return ""
	}
	w := m.width
	if w < 60 {
		w = 96 // before the first WindowSizeMsg, or in a very narrow window
	}
	var b strings.Builder
	b.WriteString(m.viewHeader(w) + "\n")
	b.WriteString(m.viewTabs(w) + "\n\n")

	switch m.tab {
	case tabSites:
		b.WriteString(m.viewSites())
	case tabWorktrees:
		b.WriteString(m.viewWorktrees())
	case tabRuntimes:
		b.WriteString(m.viewRuntimes())
	case tabDoctor:
		b.WriteString(m.viewDoctor())
	}

	b.WriteString("\n" + m.viewFooter(w))

	// Transient layers, in the order they interrupt: help, a question, then
	// work in flight, then the outcome of the last thing that finished.
	if m.mode == modeHelp {
		b.WriteString("\n" + m.viewHelp(w))
	}
	if m.mode == modeConfirm {
		body := stWarn.Render("?  "+m.confirm) + "\n" +
			stKey.Render("y") + stDim.Render(" yes   ") + stKey.Render("n") + stDim.Render(" no")
		b.WriteString("\n" + stModal.Width(min(w-4, 72)).Render(body))
	}
	if m.mode == modeBusy {
		// A spinner, what it is doing, and how long it has been doing it. The old
		// static line never even reached the screen, because the work ran inside
		// Update and bubbletea paints after Update returns.
		spin := spinFrames[m.spin%len(spinFrames)]
		line := stKey.Render(spin+" ") + stRow.Render(m.busy)
		if secs := int(time.Since(m.busySince).Seconds()); secs >= 2 {
			line += stDim.Render(fmt.Sprintf("   %ds", secs))
		}
		if m.busyDetail != "" {
			line += "\n" + stDim.Render("    "+trunc(m.busyDetail, w-10))
		}
		b.WriteString("\n" + stModal.Width(min(w-4, 72)).Render(line))
	}
	if m.msg != "" && m.mode != modeHelp {
		st, mark := stOK, "✓ "
		if m.msgErr {
			st, mark = stErr, "✗ "
		}
		b.WriteString("\n" + st.Render(mark+m.msg))
	}
	if m.mode == modeInput {
		inner := stKey.Render(m.input.prompt) + stDim.Render(" › ") +
			stSelRow.Render(m.input.value) + stSelBar.Render("▏")
		if m.input.note != "" {
			inner += "\n" + stOK.Render(trunc(m.input.note, w-10))
		}
		if m.input.hint != "" {
			inner += "\n" + stDim.Render(trunc(m.input.hint, w-10))
		}
		b.WriteString("\n" + stModal.Width(min(w-4, 78)).Render(inner))
	}
	return b.String()
}

// viewHeader is a title bar, not a readout. Four labelled lamps with four port
// numbers spent the same ink on "everything is fine" as on a real problem, so
// the normal state is one word — and the exception names itself.
func (m model) viewHeader(w int) string {
	left := stName.Render("AGENT-LOCAL") + stDim.Render("  "+Version)

	front := m.health.front
	if front == "" {
		front = "router"
	}
	// The capsule is the one loud status. Everything else is inventory.
	var status string
	if down := m.stackDown(); len(down) == 0 {
		status = lamp(true) + stCapsule.Render(" ready")
	} else {
		st := lipgloss.NewStyle().Foreground(cAmber)
		if len(down) > 1 {
			st = lipgloss.NewStyle().Foreground(cAlert)
		}
		status = lampFor("fail") + st.Render(" "+strings.Join(down, ", ")+" down")
	}
	right := stRail.Render("[ ") + status + stRail.Render(" ]") +
		stDim.Render("  "+front+" :"+fmt.Sprint(DefaultHTTPPort)) +
		stDim.Render(fmt.Sprintf("  %d sites", len(m.sites)))

	gap := w - lipgloss.Width(left) - lipgloss.Width(right) - 4
	if gap < 2 {
		return "  " + left + "\n  " + right
	}
	return "  " + left + strings.Repeat(" ", gap) + right + "  "
}

// stackDown names the pieces that are not answering, in the order a person
// would care about them. Empty means ready.
func (m model) stackDown() []string {
	var down []string
	if !m.health.db {
		down = append(down, "db")
	}
	if !m.health.http {
		down = append(down, "http")
	}
	if !m.health.https {
		down = append(down, "tls")
	}
	if !m.health.api {
		down = append(down, "api")
	}
	return down
}

// viewTabs is a rail: labels on one line, and a rule beneath that spans the
// whole width. The segment under the active tab is lit steel and the rest is
// barely there, so one line both marks position and closes the header off from
// the table — instead of a second divider competing with it.
func (m model) viewTabs(w int) string {
	tabs := []struct {
		name string
		t    tab
	}{{"Sites", tabSites}, {"Worktrees", tabWorktrees}, {"Runtimes", tabRuntimes}, {"Doctor", tabDoctor}}
	const indent, sep = 2, 3
	var labels []string
	activeStart, activeLen := -1, 0
	pos := indent
	for _, t := range tabs {
		if t.t == m.tab {
			labels = append(labels, stTabOn.Render(t.name))
			activeStart, activeLen = pos, lipgloss.Width(t.name)
		} else {
			labels = append(labels, stTabOff.Render(t.name))
		}
		pos += lipgloss.Width(t.name) + sep
	}
	// The marker overhangs the label by one cell either side: flush to the text
	// it reads as an accident of string length, proud of it it reads as a marker.
	start, length := activeStart-1, activeLen+2
	if start < 0 {
		start, length = 0, activeLen+1
	}
	rule := stRailFaint.Render(strings.Repeat("─", start))
	rule += stRail.Render(strings.Repeat("━", length))
	if rest := w - start - length; rest > 0 {
		rule += stRailFaint.Render(strings.Repeat("─", rest))
	}
	return strings.Repeat(" ", indent) + strings.Join(labels, strings.Repeat(" ", sep)) + "\n" + rule
}

// row renders one table line with the cursor bar and lamp gutter. Header and
// data rows share it, so columns cannot drift apart the way they did when the
// header skipped the cursor gutter.
//
// Three weights, in order of what the eye should find: the selection is bold,
// anything serving is normal ink, and a parked row recedes to dim so a long list
// reads as "these five are up" without counting lamps.
func row(selected bool, gutter, body string) string {
	if selected {
		return stSelBar.Render("▌ ") + gutter + stSelRow.Render(body)
	}
	return "  " + gutter + stRow.Render(body)
}

func rowParked(selected bool, gutter, body string) string {
	if selected {
		return stSelBar.Render("▌ ") + gutter + stSelRow.Render(body)
	}
	return "  " + gutter + stDim.Render(body)
}

// rowFor picks the weight from liveness.
func rowFor(live, selected bool, gutter, body string) string {
	if live {
		return row(selected, gutter, body)
	}
	return rowParked(selected, gutter, body)
}

// tableWidth keeps the header rule, rows and the detail panel on one grid.
const tableWidth = 66

// minListRows is the smallest table worth drawing. Below this the window is so
// short that hiding rows stops helping, so the list keeps them and the terminal
// scrolls instead.
const minListRows = 3

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// listRows is how many table rows fit in the window: the terminal height minus
// the frame the View already commits to (header, tab rail, footer, message) and
// minus whatever the tab draws around its own list. -1 means "height unknown" —
// before the first WindowSizeMsg — and the caller should draw everything.
func (m model) listRows(reserved int) int {
	if m.height <= 0 {
		return -1
	}
	w := m.width
	if w < 60 {
		w = 96
	}
	// Measured, not counted by hand: the header wraps to two lines in a narrow
	// window and the footer's key chips wrap to as many as they need.
	chrome := lineCount(m.viewHeader(w)) + lineCount(m.viewTabs(w)) + 1 // + blank under the rail
	chrome += 1 + lineCount(m.viewFooter(w))                            // blank, then the footer
	if m.msg != "" && m.mode != modeHelp {
		chrome++
	}
	n := m.height - chrome - reserved
	if n < minListRows {
		n = minListRows
	}
	return n
}

// scrollSites moves the window the least it can to keep the cursor inside it.
func (m *model) scrollSites(total, rows int) {
	if rows <= 0 || total <= rows {
		m.siteTop = 0
		return
	}
	if m.siteCur < m.siteTop {
		m.siteTop = m.siteCur
	}
	if m.siteCur >= m.siteTop+rows {
		m.siteTop = m.siteCur - rows + 1
	}
	if m.siteTop > total-rows {
		m.siteTop = total - rows
	}
	if m.siteTop < 0 {
		m.siteTop = 0
	}
}

// sitesLayout is the Sites tab's share of the window: how many table rows fit,
// and whether the detail panel still has room. On a very short terminal the
// panel goes first — a one-row table with a full panel under it is not a list.
func (m model) sitesLayout() (rows int, showPanel bool) {
	panelLines := 0
	if s := m.currentSite(); s != nil {
		panelLines = lineCount(m.sitePanel(s)) + 1 // and the blank line above it
	}
	fixed := 1 // the column header
	if m.mode == modeSearch || m.siteFilter != "" {
		fixed++
	}
	showPanel = panelLines > 0
	rows = m.listRows(fixed + panelLines)
	if showPanel && rows <= minListRows && m.listRows(fixed) > minListRows {
		showPanel, panelLines = false, 0
		rows = m.listRows(fixed)
	}
	if rows >= 0 && len(m.visibleSites()) > rows {
		// One row goes to the "n–m of N" line, which is the only thing on screen
		// saying the list continues past the edge.
		rows = m.listRows(fixed + 1 + panelLines)
	}
	return rows, showPanel
}

// layoutSites records the window the next frame will draw, so paging and the
// scroll position survive between renders.
func (m *model) layoutSites() {
	rows, _ := m.sitesLayout()
	if rows < 0 {
		m.pageRows, m.siteTop = len(m.visibleSites()), 0
		return
	}
	m.pageRows = rows
	m.scrollSites(len(m.visibleSites()), rows)
}

// layout records the windows the next frame will draw, so paging and the
// scroll positions survive between renders. View works on a value receiver,
// so anything it computed would be thrown away with the copy.
func (m *model) layout() {
	m.layoutSites()
	if m.tab == tabDoctor && m.doctor != nil {
		budget := m.doctorBudget()
		if budget < 0 {
			m.pageRows, m.docTop = len(m.doctor.Findings), 0
			return
		}
		m.docTop, _ = doctorWindow(m.doctor.Findings, m.docCur, m.docTop, budget)
		m.pageRows = budget
	}
}

// doctorBudget is the Doctor tab's share of the window: everything under the
// summary block, less the line that says the list continues.
func (m model) doctorBudget() int { return m.listRows(3) }

// doctorWindow picks the findings a budget of lines can show while keeping
// the cursor visible. A finding costs one line — two when it carries a fix
// hint — so the budget is spent rather than divided.
func doctorWindow(findings []Finding, cur, top, budget int) (newTop, end int) {
	n := len(findings)
	if n == 0 {
		return 0, 0
	}
	if budget <= 0 {
		return 0, n
	}
	cost := func(f Finding) int {
		if f.Status != "ok" && f.FixCmd != "" {
			return 2
		}
		return 1
	}
	if cur >= n {
		cur = n - 1
	}
	if cur < 0 {
		cur = 0
	}
	if top > cur {
		top = cur
	}
	if top < 0 {
		top = 0
	}
	// A stale top must not strand blank budget under the list — a re-run
	// can shrink the report.
	tail := func(from int) (lines int) {
		for i := from; i < n; i++ {
			lines += cost(findings[i])
		}
		return lines
	}
	for top > 0 && tail(top-1) <= budget {
		top--
	}
	for {
		lines, e := 0, top
		for e < n {
			c := cost(findings[e])
			if lines+c > budget {
				break
			}
			lines, e = lines+c, e+1
		}
		if cur < e || top == cur {
			return top, e
		}
		top++
	}
}

// pageStep is a screenful of list, minus a row of overlap so you keep your place.
func (m model) pageStep() int {
	n := m.pageRows - 1
	if n < 1 {
		n = 1
	}
	return n
}

func (m *model) viewSites() string {
	if len(m.sites) == 0 {
		empty := stRow.Render("No sites on this rack yet.") + "\n" +
			stDim.Render("A site is a WordPress install this machine serves.") + "\n\n" +
			stKey.Render("n") + stDim.Render("  create a new one") + "\n" +
			stKey.Render("i") + stDim.Render("  import LocalWP or an existing checkout") + "\n" +
			stKey.Render("?") + stDim.Render("  every key")
		return "  " + stPanel.Width(m.panelWidth()).Render(empty)
	}
	shown := m.visibleSites()
	var b strings.Builder
	if m.mode == modeSearch || m.siteFilter != "" {
		b.WriteString(m.viewSiteSearch(len(shown)) + "\n")
	}
	if len(shown) == 0 {
		empty := stDim.Render("no sites match ") + stRow.Render(m.siteFilter) + "\n" +
			stKey.Render("esc") + stDim.Render("  clear")
		return b.String() + "  " + stPanel.Width(m.panelWidth()).Render(empty)
	}
	rows, showPanel := m.sitesLayout()
	panel := ""
	if s := m.currentSite(); showPanel && s != nil {
		panel = "\n" + m.sitePanel(s)
	}
	top, end := 0, len(shown)
	if rows >= 0 {
		m.scrollSites(len(shown), rows)
		top = m.siteTop
		if end = top + rows; end > len(shown) {
			end = len(shown)
		}
	}

	b.WriteString("    " + stHead.Render(fmt.Sprintf("%-20s %-5s %-30s %8s", "SITE", "PHP", "DOMAIN", "PREVIEWS")) + "\n")
	for i, s := range shown[top:end] {
		i += top
		// Deliberately no size column: it costs a directory walk per row per
		// render, and an imported checkout cannot report one anyway. Size lives
		// in the panel, for the selected site only.
		gone := !fileExists(s.WPDir)
		previews := ""
		if n := len(m.store.WorktreesFor(s.Slug)); n > 0 {
			previews = fmt.Sprint(n)
		}
		body := fmt.Sprintf("%-20s %-5s %-30s %8s",
			trunc(s.Slug, 20), s.PHPVersion, trunc(s.Domain, 30), previews)
		live := m.engine.FPMRunning(s.Slug)
		gutter := lamp(live) + " "
		if gone {
			// Files removed behind our back: the pool may still be up, so a green
			// lamp would promise a site that answers nothing but 404.
			gutter = lampFor("fail") + " "
			body += stErr.Render("  docroot missing")
		}
		b.WriteString(rowFor(live, i == m.siteCur, gutter, body) + "\n")
	}
	if top > 0 || end < len(shown) {
		b.WriteString("    " + stDim.Render(fmt.Sprintf("%d–%d of %d   ↑↓ scroll", top+1, end, len(shown))) + "\n")
	}
	b.WriteString(panel)
	return b.String()
}

// viewSiteSearch is the filter line above the table. The caret only shows
// while typing so a kept filter still reads as state, not as a prompt.
func (m model) viewSiteSearch(n int) string {
	q := m.siteFilter
	if m.mode == modeSearch {
		q += stSelBar.Render("▏")
	}
	count := stDim.Render(fmt.Sprintf("%d/%d", n, len(m.sites)))
	hint := ""
	if m.mode == modeSearch {
		hint = stDim.Render("   enter keep  esc clear")
	}
	return "  " + stKey.Render("/") + " " + stSelRow.Render(q) + "  " + count + hint
}

// sitePanel holds what a table column would have had to truncate: the real
// URLs, the credentials, where it lives, how big it got.
func (m *model) sitePanel(s *Site) string {
	rows := [][2]string{
		{"open", BareURL(s)},
		{"admin", BareURL(s) + "/wp-admin"},
		{"db", s.DBName + stDim.Render("  as ") + s.DBUser + stDim.Render(fmt.Sprintf("  127.0.0.1:%d", DefaultDBPort))},
		{"gui", AdminerURL(s.Domain) + stDim.Render("  g")},
		{"files", shortHome(s.WPDir) + stDim.Render("   "+m.siteSize(s.Slug))},
	}
	// Credentials only exist for sites we installed; an adopted folder keeps
	// whatever admin it already had, and inventing a blank pair reads as data.
	if s.AdminUser != "" {
		rows[1] = [2]string{"admin", BareURL(s) + "/wp-admin" + stDim.Render("   "+s.AdminUser+" / "+s.AdminPass)}
	}
	if wts := m.store.WorktreesFor(s.Slug); len(wts) > 0 {
		names := make([]string, 0, len(wts))
		for _, w := range wts {
			names = append(names, w.Branch+stDim.Render(" → "+BareDomainURL(w.Domain)))
		}
		rows = append(rows, [2]string{"preview", strings.Join(names, stDim.Render(", "))})
	}
	// Only when set: an off switch does not need a row of its own, but a live
	// redirect to somewhere else absolutely does — otherwise an image arriving
	// from production looks like the local copy.
	if eff := EffectiveMediaFallback(s); eff != "" {
		note := ""
		if s.MediaFallback == "" {
			note = stDim.Render("  (.htaccess)")
		}
		rows = append(rows, [2]string{"media", stDim.Render("missing uploads → ") + eff + note})
	}
	// Only when something was caught: an empty inbox is the normal state and
	// does not need a row saying so.
	if n, latest := MailCount(s.Slug); n > 0 {
		rows = append(rows, [2]string{"mail", fmt.Sprintf("%d caught", n) + stDim.Render("  "+mailAge(latest)+"  ") + MailURL(s.Domain)})
	}
	var body strings.Builder
	for i, r := range rows {
		if i > 0 {
			body.WriteString("\n")
		}
		body.WriteString(stLabel.Render(r[0]) + "  " + r[1])
	}
	return stPanel.Width(m.panelWidth()).Render(body.String())
}

func (m model) panelWidth() int {
	w := m.width - 6
	if w < tableWidth {
		return tableWidth
	}
	if w > 92 {
		return 92
	}
	return w
}

func (m model) viewWorktrees() string {
	if len(m.sites) == 0 {
		return stDim.Render("  No sites yet — create one on the Sites tab first.")
	}
	wts := m.allWorktrees()
	if len(wts) == 0 {
		return stDim.Render("  No branch previews. Press ") + stKey.Render("a") +
			stDim.Render(" to serve a branch of a site on its own domain.")
	}
	var b strings.Builder
	b.WriteString("    " + stHead.Render(fmt.Sprintf("%-16s %-20s %-30s", "SITE", "BRANCH", "DOMAIN")) + "\n")
	for i, w := range wts {
		body := fmt.Sprintf("%-16s %-20s %-30s", trunc(w.Site, 16), trunc(w.Branch, 20), trunc(w.Domain, 30))
		live := m.engine.FPMRunning(w.ID)
		gutter := lamp(live) + " "
		// A row whose checkout is gone can never serve: a grey lamp would read as
		// "stopped", which is a lie the user only discovers via a dead connection.
		if !fileExists(w.Path) {
			gutter = lampFor("fail") + " "
			body += stErr.Render("  checkout missing")
		}
		b.WriteString(rowFor(live, i == m.wtCur, gutter, body) + "\n")
	}
	if wt := m.currentWorktree(); wt != nil {
		files := shortHome(wt.Path)
		if !fileExists(wt.Path) {
			files += stErr.Render("  — gone")
		}
		rows := [][2]string{
			{"open", BareDomainURL(wt.Domain)},
			{"branch", wt.Branch + stDim.Render("  of ") + wt.Site},
			{"files", files},
			{"db", stDim.Render("same database as ") + wt.Site + stDim.Render(" — writes land on the base site")},
		}
		var body strings.Builder
		for i, r := range rows {
			if i > 0 {
				body.WriteString("\n")
			}
			body.WriteString(stLabel.Render(r[0]) + "  " + r[1])
		}
		b.WriteString("\n" + stPanel.Width(m.panelWidth()).Render(body.String()))
	}
	return b.String()
}

func (m model) viewRuntimes() string {
	inv := &m.inv
	var b strings.Builder
	b.WriteString("  " + stHead.Render("PHP") + "\n")
	inUse := m.phpInUse()
	for i, rt := range inv.PHPs {
		// Pad the plain strings, then colour: %-10s counts escape bytes as
		// width, so styling before padding shifts every later column.
		note := "cli only"
		if rt.FPM != "" {
			note = "fpm"
		}
		if inUse[rt.Version] {
			note = "serving"
		}
		body := fmt.Sprintf("%-8s %-10s", rt.Version, note) + " " + stDim.Render(shortHome(rt.Bin))
		b.WriteString(rowFor(inUse[rt.Version], i == m.rtCur, lamp(inUse[rt.Version])+" ", body) + "\n")
	}
	b.WriteString("\n  " + stHead.Render("SERVICES") + "\n")
	svc := func(on bool, name, detail string) {
		b.WriteString(row(false, lamp(on)+" ", fmt.Sprintf("%-10s", name)+" "+stDim.Render(detail)) + "\n")
	}
	if inv.MySQL.Bin == "" {
		b.WriteString(row(false, lampFor("fail")+" ", fmt.Sprintf("%-10s", "database")+" "+stDim.Render("not installed — press m")) + "\n")
	} else {
		svc(m.health.db, inv.MySQL.Kind, fmt.Sprintf("%s   port %d", inv.MySQL.Version, DefaultDBPort))
	}
	front := m.health.front
	if front == "" {
		front = "router"
	}
	detail := fmt.Sprintf("built-in Go vhost proxy   ports %d/%d", DefaultHTTPPort, DefaultHTTPSPort)
	if front == "apache" {
		detail = fmt.Sprintf("apache %s   ports %d/%d", inv.HTTP.Version, DefaultHTTPPort, DefaultHTTPSPort)
	}
	svc(m.health.http, front, detail)
	svc(inv.Brew != "", "homebrew", orDash(shortHome(inv.Brew), "missing — press b"))
	return b.String()
}

func (m *model) viewDoctor() string {
	if m.doctor == nil {
		return "  " + stDim.Render("running checks…")
	}
	var b strings.Builder
	var warn, fail int
	for _, f := range m.doctor.Findings {
		switch f.Status {
		case "warn":
			warn++
		case "fail":
			fail++
		}
	}
	summary := stOK.Render(fmt.Sprintf("%d checks pass", len(m.doctor.Findings)-warn-fail))
	if warn > 0 {
		summary += stDim.Render("  ") + stWarn.Render(fmt.Sprintf("%d warn", warn))
	}
	if fail > 0 {
		summary += stDim.Render("  ") + stErr.Render(fmt.Sprintf("%d fail", fail))
	}
	b.WriteString("  " + summary + "\n\n")
	// Width the name column to the longest check rather than a guess: names run
	// from "dns" to "site:muster-import-test", and a fixed 12 shoved half the
	// details out of line.
	nameW := 8
	for _, f := range m.doctor.Findings {
		if n := lipgloss.Width(f.Check); n > nameW {
			nameW = n
		}
	}
	if nameW > 24 {
		nameW = 24
	}
	findings := m.doctor.Findings
	top, end := 0, len(findings)
	if budget := m.doctorBudget(); budget >= 0 {
		m.docTop, end = doctorWindow(findings, m.docCur, m.docTop, budget)
		top = m.docTop
	}
	for i := top; i < end; i++ {
		f := findings[i]
		detail := f.Detail
		if f.Status == "ok" {
			detail = stDim.Render(detail) // failures should be the loud ones
		}
		line := fmt.Sprintf("%-*s", nameW, trunc(f.Check, nameW)) + "  " + detail
		b.WriteString(row(i == m.docCur, lampFor(f.Status)+" ", line))
		if f.Status != "ok" && f.FixCmd != "" {
			b.WriteString("\n" + strings.Repeat(" ", 4) + stDim.Render("fix: "+f.FixCmd))
		}
		b.WriteString("\n")
	}
	if top > 0 || end < len(findings) {
		b.WriteString("    " + stDim.Render(fmt.Sprintf("%d–%d of %d   ↑↓ scroll", top+1, end, len(findings))) + "\n")
	}
	return b.String()
}

// keyChip is one binding in the legend: the key is the only loud thing.
func keyChip(k, label string) string {
	return stKey.Render(k) + stDim.Render(" "+label)
}

// packChips lays chips into lines that fit width. A chip is never split, which
// is how "D delete" used to die at the edge of the window.
func packChips(chips []string, width int) []string {
	if width < 8 {
		width = 8
	}
	var lines []string
	var cur string
	for _, c := range chips {
		if cur == "" {
			cur = c
			continue
		}
		if lipgloss.Width(cur)+2+lipgloss.Width(c) > width {
			lines = append(lines, cur)
			cur = c
			continue
		}
		cur += "  " + c
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func chipsOf(keys [][2]string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, keyChip(k[0], k[1]))
	}
	return out
}

// viewFooter is a legend strip under a hairline, not a sentence. Tab actions
// wrap as whole chips; ? / tab / quit sit on the last line to the right.
func (m model) viewFooter(w int) string {
	var keys [][2]string
	switch m.tab {
	case tabSites:
		keys = [][2]string{{"/", "search"}, {"n", "new"}, {"i", "import"}, {"s", "start"}, {"x", "stop"}, {"R", "restart"},
			{"o", "open"}, {"g", "db"}, {"a", "preview"}, {"p", "php"}, {"d", "domain"}, {"m", "media"}, {"D", "delete"}}
	case tabWorktrees:
		keys = [][2]string{{"a", "add"}, {"s", "start"}, {"x", "stop"}, {"R", "restart"}, {"o", "open"}, {"D", "remove"}}
	case tabRuntimes:
		keys = [][2]string{{"i", "php"}, {"m", "mariadb"}, {"h", "apache"}, {"b", "brew"}}
	case tabDoctor:
		keys = [][2]string{{"r", "re-run"}, {"f", "fix"}}
	}
	always := chipsOf([][2]string{{"?", "help"}, {"⇥", "tab"}, {"q", "quit"}})
	alwaysStr := strings.Join(always, "  ")
	inner := w - 4
	if inner < 20 {
		inner = 20
	}

	lines := packChips(chipsOf(keys), inner)
	if len(lines) == 0 {
		lines = []string{""}
	}
	last := lines[len(lines)-1]
	if gap := inner - lipgloss.Width(last) - lipgloss.Width(alwaysStr); gap >= 2 {
		lines[len(lines)-1] = last + strings.Repeat(" ", gap) + alwaysStr
	} else {
		pad := inner - lipgloss.Width(alwaysStr)
		if pad < 0 {
			pad = 0
		}
		lines = append(lines, strings.Repeat(" ", pad)+alwaysStr)
	}

	var b strings.Builder
	b.WriteString(stRailFaint.Render(strings.Repeat("─", max(w, 2))))
	for _, line := range lines {
		b.WriteString("\n  " + line)
	}
	return b.String()
}

func (m model) viewHelp(w int) string {
	section := func(title string, keys [][2]string, note string) string {
		body := strings.Join(packChips(chipsOf(keys), min(w-8, 72)), "\n")
		if note != "" {
			body += "\n" + stWarn.Render(note)
		}
		return stHead.Render(title) + "\n" + body
	}
	block := strings.Join([]string{
		section("SITES", [][2]string{
			{"/", "search"}, {"n", "new"}, {"i", "import"}, {"s", "start"}, {"x", "stop"}, {"R", "restart"},
			{"o", "open"}, {"g", "database"}, {"a", "preview"}, {"p", "php"}, {"d", "domain"},
			{"m", "media"}, {"D", "delete"},
		}, ""),
		section("PREVIEWS", [][2]string{
			{"a", "add"}, {"s", "start"}, {"x", "stop"}, {"R", "restart"}, {"o", "open"}, {"D", "remove"},
		}, "same database as the base site"),
		section("RUNTIMES", [][2]string{
			{"i", "install php"}, {"m", "mariadb"}, {"h", "apache"}, {"b", "homebrew"},
		}, ""),
		section("DOCTOR", [][2]string{{"r", "re-run"}, {"f", "fix what can be fixed"}}, ""),
		section("MOVING", [][2]string{
			{"↑↓", "row"}, {"pgup/pgdn", "page"}, {"home/end", "first / last"},
		}, "long lists scroll inside the window"),
		section("ALWAYS", [][2]string{
			{"⇥", "next tab"}, {"r", "refresh"}, {"q", "quit"}, {"esc", "cancel"},
		}, ""),
	}, "\n\n")
	return stModal.Width(min(w-4, 76)).Render(block)
}

// completeImportSource completes a filesystem path, or a LocalWP site name.
func completeImportSource(v string) (string, string) {
	raw := strings.TrimSpace(v)
	if raw == "" || strings.ContainsAny(raw, "/~") {
		return completeDir(v)
	}
	sites, err := ListLocalWPSites()
	if err != nil || len(sites) == 0 {
		return completeDir(v)
	}
	var names []string
	for _, s := range sites {
		if strings.HasPrefix(strings.ToLower(s.Name), strings.ToLower(raw)) {
			names = append(names, s.Name)
		}
	}
	sort.Strings(names)
	switch len(names) {
	case 0:
		return v, localWPImportNote()
	case 1:
		return names[0], "LocalWP site"
	default:
		return names[0], fmt.Sprintf("%d LocalWP: %s", len(names), strings.Join(trimList(names, 5), "  "))
	}
}

func localWPImportNote() string {
	sites, err := ListLocalWPSites()
	if err != nil || len(sites) == 0 {
		return "a directory with wp-load.php, or a LocalWP site name"
	}
	names := make([]string, 0, len(sites))
	for _, s := range sites {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return fmt.Sprintf("LocalWP: %s", strings.Join(trimList(names, 6), "  "))
}

func defaultImportName(src string) string {
	if sites, err := ListLocalWPSites(); err == nil {
		for _, s := range sites {
			if s.Name == src || s.ID == src {
				return s.Name
			}
		}
	}
	return defaultSiteName(src)
}

func importSourceNote(src string) string {
	if sites, err := ListLocalWPSites(); err == nil {
		for _, s := range sites {
			if s.Name == src || s.ID == src {
				return "LocalWP " + s.Name + " → " + s.Domain
			}
		}
	}
	if st, err := os.Stat(src); err == nil && st.IsDir() {
		return "directory " + shortHome(DocrootFor(src))
	}
	return src
}

// trunc keeps a column a column: an over-long value loses its tail to an
// ellipsis rather than shoving every column to its right.
func trunc(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	if w <= 1 {
		return "…"
	}
	return s[:w-1] + "…"
}

// shortHome trades the home prefix for ~, which is the difference between a
// path that fits on the line and one that does not.
func shortHome(p string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, h) {
		return "~" + strings.TrimPrefix(p, h)
	}
	return p
}

func orDash(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// runTUI starts the bubbletea program.
func runTUI() {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Println("tui error:", err)
	}
}

// renderFrame prints a single frame and exits. Alignment bugs in a TUI are
// invisible until something renders it at a known width, and a PTY harness
// cannot report one — so this is how the layout gets checked.
//
//	agent-local tui --frame 120 --height 24 --tab doctor
func renderFrame(args []string) error {
	// lipgloss strips colour when stdout is not a terminal, which would make a
	// piped frame useless for reviewing the palette — force a profile so the
	// escapes are there to look at.
	lipgloss.SetColorProfile(termenv.ANSI256)
	w := 100
	if v := flagValue(args, "--frame"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 20 {
			w = n
		}
	}
	h := 40
	if v := flagValue(args, "--height"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 8 {
			h = n
		}
	}
	m := initialModel()
	m.width, m.height = w, h
	switch flagValue(args, "--tab") {
	case "worktrees":
		m.tab = tabWorktrees
	case "runtimes":
		m.tab = tabRuntimes
	case "doctor":
		m.tab = tabDoctor
		// A one-shot frame has no Update loop to run the checks async, so
		// pay for them here, where blocking is the point.
		m.doctor = Doctor(m.store)
	}
	fmt.Println(m.View())
	return nil
}

// completeDir completes a directory path the way a shell does, and — unlike the
// first version of this — writes the answer back in the notation the user chose.
// Collapsing a typed "/Users/jakevarrese" to "~" is a correct path and a useless
// answer: it reads as though the input was thrown away.
func completeDir(v string) (string, string) {
	raw := strings.TrimSpace(v)
	if raw == "" {
		raw = "~/"
	}
	tilde := raw == "~" || strings.HasPrefix(raw, "~"+string(os.PathSeparator))
	// render puts a resolved path back into the user's own notation.
	render := func(p string) string {
		if tilde {
			return shortHome(p)
		}
		return p
	}
	expanded, err := ResolveDir(raw)
	if err != nil {
		return v, ""
	}
	// A trailing separator means "inside this directory", not "complete its name".
	dir, base := filepath.Dir(expanded), filepath.Base(expanded)
	if strings.HasSuffix(raw, string(os.PathSeparator)) || raw == "~" {
		dir, base = expanded, ""
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return v, "no such directory: " + shortHome(dir)
	}
	// Hidden directories are noise until they are asked for by name.
	wantHidden := strings.HasPrefix(base, ".")
	var exact, insensitive []string
	for _, e := range ents {
		name := e.Name()
		if !e.IsDir() || (!wantHidden && strings.HasPrefix(name, ".")) {
			continue
		}
		switch {
		case strings.HasPrefix(name, base):
			exact = append(exact, name)
		case strings.HasPrefix(strings.ToLower(name), strings.ToLower(base)):
			insensitive = append(insensitive, name)
		}
	}
	// Case-sensitive matches win; typing "doc" still finds "Documents" when
	// nothing matches exactly.
	names := exact
	if len(names) == 0 {
		names = insensitive
	}
	sort.Strings(names)
	sep := string(os.PathSeparator)
	switch len(names) {
	case 0:
		if base == "" {
			return render(dir) + sep, "no sub-directories here"
		}
		return v, "nothing matches " + base
	case 1:
		return render(filepath.Join(dir, names[0])) + sep, ""
	default:
		// Count first: the line gets trimmed to the window, and "how many" is the
		// part that must survive.
		list := fmt.Sprintf("%d dirs: %s", len(names), strings.Join(trimList(names, 6), "  "))
		if cp := commonPrefix(names); cp != base {
			return render(filepath.Join(dir, cp)), list
		}
		// The prefix cannot grow. If what is typed is itself a directory, step
		// into it so a second tab makes progress instead of stalling.
		for _, n := range names {
			if n == base {
				return render(filepath.Join(dir, base)) + sep, list
			}
		}
		return v, list
	}
}

// commonPrefix is the longest prefix shared by every candidate, which is how far
// a completion can go without guessing.
func commonPrefix(in []string) string {
	if len(in) == 0 {
		return ""
	}
	p := in[0]
	for _, s := range in[1:] {
		for !strings.HasPrefix(strings.ToLower(s), strings.ToLower(p)) {
			p = p[:len(p)-1]
			if p == "" {
				return ""
			}
		}
	}
	return p
}

// trimList caps a candidate list so a wide directory cannot push the layout off
// the screen.
func trimList(in []string, max int) []string {
	if len(in) <= max {
		return in
	}
	return append(in[:max:max], fmt.Sprintf("+%d more", len(in)-max))
}

// defaultSiteName reads a sensible site name out of a path, skipping the
// container directories that carry no meaning of their own.
func defaultSiteName(dir string) string {
	base := filepath.Base(dir)
	switch base {
	case "public", "web", "www", "htdocs", "public_html", "app", "wp":
		return filepath.Base(filepath.Dir(dir))
	}
	return base
}

// newSiteNote says what the shared sites directory is and what is already in it,
// so the first question can be answered without going to look.
func (m model) newSiteNote() string {
	dir := m.store.SitesDir()
	n := 0
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				n++
			}
		}
	}
	switch n {
	case 0:
		return shortHome(dir) + " — empty so far"
	case 1:
		return shortHome(dir) + " — holds 1 directory"
	default:
		return fmt.Sprintf("%s — holds %d directories", shortHome(dir), n)
	}
}
