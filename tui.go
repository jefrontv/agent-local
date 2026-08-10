package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

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
	cInk   = lipgloss.AdaptiveColor{Light: "236", Dark: "252"}
	cDim   = lipgloss.AdaptiveColor{Light: "244", Dark: "245"}
	cSteel = lipgloss.AdaptiveColor{Light: "24", Dark: "67"}
	cLamp  = lipgloss.Color("78")  // serving
	cOff   = lipgloss.Color("240") // parked
	cAlert = lipgloss.Color("203") // broken
	cAmber = lipgloss.Color("179") // needs attention / in flight
	cRule  = lipgloss.AdaptiveColor{Light: "252", Dark: "238"}
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
	stPanel     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cSteel).Padding(0, 1)
	stLabel     = lipgloss.NewStyle().Foreground(cDim).Width(7).Align(lipgloss.Right)
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
)

type inputTarget int

const (
	inputNone     inputTarget = iota
	inputNewDir               // step 1: where the site lives (tab-completes)
	inputNewFresh             // step 2: install WordPress here, or just point at it
	inputNewName              // step 3: name/slug
	inputNewDomain
	inputCreateName
	inputCreateDomain
	inputCreatePHP
	inputWorktreeBranch
	inputSwitchPHP
	inputSetDomain
	inputSQL
	inputInstallPHP
)

type inputSpec struct {
	target inputTarget
	prompt string
	value  string
	pos    int
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
	busy     string
	msg      string
	msgErr   bool
	sites    []*Site
	doctor   *DoctorReport
	width    int
	height   int
	health   healthSnapshot
	sizes    map[string]string
	quitting bool
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
	DiscoverInventory(store)
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

func (m model) Init() tea.Cmd { return nil }

func (m *model) setMsg(s string, isErr bool) { m.msg = s; m.msgErr = isErr }

func (m model) currentSite() *Site {
	if len(m.sites) == 0 || m.siteCur >= len(m.sites) {
		return nil
	}
	return m.sites[m.siteCur]
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
		return len(m.store.Inventory().PHPs)
	case tabDoctor:
		if m.doctor != nil {
			return len(m.doctor.Findings)
		}
		return 0
	default:
		return len(m.sites)
	}
}

// allWorktrees lists every preview across every site, ordered site then branch.
// The tab used to show only the site highlighted on another tab, which meant the
// list changed under you and nothing on screen said why.
func (m model) allWorktrees() []*Worktree {
	out := make([]*Worktree, 0, len(m.store.Data.Worktrees))
	for _, s := range m.sites {
		for _, w := range m.store.WorktreesFor(s.Slug) {
			out = append(out, w)
		}
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

// ---------- update ----------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
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
			m.mode = modeBrowse
			action := m.confirmA
			m.confirm = ""
			if err := action(); err != nil {
				m.setMsg(err.Error(), true)
			} else {
				m.setMsg("done", false)
			}
			m.refresh()
			return m, nil
		case "n", "esc":
			m.mode = modeBrowse
			m.confirm = ""
			return m, nil
		}
		return m, nil
	case modeBusy:
		return m, nil
	}

	// browse mode
	switch k.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "tab", "right":
		m.tab = tab((int(m.tab) + 1) % 4)
		m.msg = ""
		return m, nil
	case "shift+tab", "left":
		m.tab = tab((int(m.tab) + 3) % 4)
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
	case "r":
		// Refreshing is a read. This used to Save() as well, which wrote the
		// TUI's in-memory snapshot back over the file and silently erased
		// anything the CLI, the daemon or an agent had changed since the TUI
		// opened — sites turning up "stopped" for no reason was this.
		m.refresh()
		m.setMsg("refreshed", false)
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
		m.refresh()
		return m, nil
	case "tab":
		// Completion only means something for a path. Elsewhere tab would
		// otherwise leak through to the tab bar and abandon a half-typed answer.
		if m.input.target == inputNewDir {
			v, note := completeDir(m.input.value)
			m.input.value, m.input.note = v, note
		}
		return m, nil
	case "backspace":
		if len(m.input.value) > 0 {
			m.input.value = m.input.value[:len(m.input.value)-1]
			if m.input.target == inputNewDir {
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
		next := m.startInput(inputNewDomain, "domain for "+slug, "must resolve locally — /etc/hosts is updated for you", "")
		next.input.dir, next.input.fresh, next.input.name = spec.dir, spec.fresh, val
		next.input.note = spec.note
		next.input.value = m.store.DefaultDomain(slug)
		*m = next
		return nil
	case inputNewDomain:
		if val == "" {
			return fmt.Errorf("domain required")
		}
		if spec.fresh {
			m.busyStart("installing WordPress into " + shortHome(spec.dir))
			site, err := m.engine.CreateSite(CreateOpts{
				Name:     spec.name,
				Dir:      spec.dir,
				Domain:   val,
				Progress: func(s, d string) {},
			})
			m.busyEnd()
			if err != nil {
				return err
			}
			m.setMsg("created "+BareURL(site)+"  admin="+site.AdminUser+"/"+site.AdminPass, false)
			return nil
		}
		m.busyStart("attaching " + shortHome(spec.dir))
		var config string
		site, err := m.engine.AttachSite(AttachOpts{
			Name:   spec.name,
			Dir:    spec.dir,
			Domain: val,
			Progress: func(stage, detail string) {
				if stage == "config" {
					config = detail
				}
			},
		})
		m.busyEnd()
		if err != nil {
			return err
		}
		msg := "attached " + BareURL(site) + "  db " + site.DBName + " / " + site.DBUser
		if config != "" {
			msg += "  — " + config
		}
		m.setMsg(msg, false)
		return nil
	case inputCreateName:
		if val == "" {
			return fmt.Errorf("name required")
		}
		m.busyStart("creating " + val + " (downloads + installs wordpress…)")
		site, err := m.engine.CreateSite(CreateOpts{
			Name:     val,
			Progress: func(s, d string) {},
		})
		m.busyEnd()
		if err != nil {
			return err
		}
		m.setMsg("created "+BareURL(site)+" admin="+site.AdminUser+"/"+site.AdminPass, false)
	case inputCreateDomain:
		return m.engine.SetDomain(spec.slug, val)
	case inputCreatePHP, inputSwitchPHP:
		if val == "" {
			return fmt.Errorf("version required")
		}
		m.busyStart("switching php → " + val)
		err := m.engine.SwitchPHP(spec.slug, val)
		m.busyEnd()
		if err != nil {
			return err
		}
		m.setMsg("php "+val+" active on "+spec.slug, false)
	case inputSetDomain:
		if val == "" {
			return fmt.Errorf("domain required")
		}
		return m.engine.SetDomain(spec.slug, val)
	case inputWorktreeBranch:
		if val == "" {
			return fmt.Errorf("branch required")
		}
		m.busyStart("adding worktree " + val)
		w, err := m.engine.AddWorktree(spec.slug, val)
		m.busyEnd()
		if err != nil {
			return err
		}
		m.setMsg(fmt.Sprintf("worktree: http://%s:%d", w.Domain, DefaultHTTPPort), false)
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
	case inputInstallPHP:
		if val == "" {
			val = "8.3"
		}
		m.busyStart("brew install php@" + val + " (this can take a while…)")
		err := InstallPHP(m.store, val, nil)
		m.busyEnd()
		if err != nil {
			return err
		}
		DiscoverInventory(m.store)
		m.store.Save()
		m.setMsg("php "+val+" installed", false)
	}
	return nil
}

func (m *model) busyStart(s string) { m.mode = modeBusy; m.busy = s }
func (m *model) busyEnd()           { m.mode = modeBrowse; m.busy = "" }

func (m model) handleSitesKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	site := m.currentSite()
	switch k.String() {
	case "n":
		return m.startInput(inputNewDir, "where should the site live?",
			"⇥ completes · ~/Sites/my-site", ""), nil
	case "s":
		if site == nil {
			return m, nil
		}
		m.busyStart("starting " + site.Slug)
		err := m.engine.StartSite(site.Slug)
		m.busyEnd()
		if err != nil {
			m.setMsg(err.Error(), true)
		} else {
			m.setMsg(BareURL(site), false)
		}
		return m, nil
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
		m.busyStart("restarting " + site.Slug)
		stopErr := m.engine.StopSite(site.Slug)
		// A failed stop used to be discarded: the following start then found the
		// pool still up, returned nil, and the restart reported nothing at all.
		if stopErr != nil && m.engine.FPMRunning(site.Slug) {
			m.busyEnd()
			m.setMsg("restart failed: "+stopErr.Error(), true)
			return m, nil
		}
		err := m.engine.StartSite(site.Slug)
		m.busyEnd()
		if err != nil {
			m.setMsg(err.Error(), true)
		} else {
			m.setMsg("restarted "+site.Slug, false)
		}
		return m, nil
	case "p":
		if site == nil {
			return m, nil
		}
		return m.startInput(inputSwitchPHP, "php version for "+site.Slug,
			"installed: "+strings.Join(m.store.Inventory().Runtimes(), " "), site.Slug), nil
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
		if err := m.engine.StartWorktree(wt.ID); err != nil {
			m.setMsg(err.Error(), true)
		} else {
			m.setMsg("preview running: "+BareDomainURL(wt.Domain), false)
		}
		return m, nil
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
		m.busyStart("restarting " + wt.Branch)
		m.engine.StopWorktree(wt.ID)
		err := m.engine.StartWorktree(wt.ID)
		m.busyEnd()
		if err != nil {
			m.setMsg(err.Error(), true)
		} else {
			m.setMsg("restarted "+wt.Branch, false)
		}
		return m, nil
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
		return m.startInput(inputInstallPHP, "install php version", "7.4 8.0 8.1 8.2 8.3 8.4", ""), nil
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

func (m model) handleDoctorKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "r":
		m.doctor = Doctor(m.store)
		return m, nil
	case "f":
		m.busyStart("applying fixes…")
		done := DoctorFix(m.store, false)
		m.busyEnd()
		m.doctor = Doctor(m.store)
		if len(done) == 0 {
			m.setMsg("nothing auto-fixable without a password prompt; use CLI: agent-local doctor --fix", false)
		} else {
			m.setMsg("fixed: "+strings.Join(done, "; "), false)
		}
		return m, nil
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

	// Transient layers, in the order they interrupt: a question, then work in
	// flight, then the outcome of the last thing that finished.
	if m.mode == modeConfirm {
		b.WriteString("\n\n" + stWarn.Render("? "+m.confirm) + "  " +
			stKey.Render("y") + stDim.Render(" yes  ") + stKey.Render("n") + stDim.Render(" no"))
	}
	if m.mode == modeBusy {
		b.WriteString("\n\n" + stWarn.Render("· "+m.busy))
	}
	if m.msg != "" {
		st, mark := stOK, "✓ "
		if m.msgErr {
			st, mark = stErr, "✗ "
		}
		b.WriteString("\n\n" + st.Render(mark+m.msg))
	}
	if m.mode == modeInput {
		b.WriteString("\n\n" + stKey.Render(m.input.prompt) + stDim.Render(" › ") +
			stSelRow.Render(m.input.value) + stSelBar.Render("▏"))
		indent := strings.Repeat(" ", lipgloss.Width(m.input.prompt)+3)
		// The note is what is actually on disk and what the answer will do with
		// it: asking about a directory without saying what is in it is asking
		// the user to guess.
		if m.input.note != "" {
			b.WriteString("\n" + indent + stOK.Render(m.input.note))
		}
		if m.input.hint != "" {
			b.WriteString("\n" + indent + stDim.Render(m.input.hint))
		}
	}
	return b.String()
}

// viewHeader is a title bar, not a readout. Four labelled lamps with four port
// numbers spent the same ink on "everything is fine" as on a real problem, so
// the normal state is one word — and the exception names itself.
func (m model) viewHeader(w int) string {
	left := stName.Render("agent-local") + stDim.Render("  "+Version)

	front := m.health.front
	if front == "" {
		front = "router"
	}
	// What is actually worth typing or knowing when all is well: the port you
	// visit, and how many sites there are.
	var right string
	if down := m.stackDown(); len(down) == 0 {
		right = lamp(true) + stRow.Render(" ready") +
			stDim.Render("   "+front+" :"+fmt.Sprint(DefaultHTTPPort)) +
			stDim.Render(fmt.Sprintf("   %d sites", len(m.sites)))
	} else {
		st := lipgloss.NewStyle().Foreground(cAmber)
		if len(down) > 1 {
			st = lipgloss.NewStyle().Foreground(cAlert)
		}
		right = lampFor("fail") + st.Render(" "+strings.Join(down, ", ")+" down") +
			stDim.Render(fmt.Sprintf("   %d sites", len(m.sites)))
	}

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

func (m *model) viewSites() string {
	if len(m.sites) == 0 {
		return stDim.Render("  No sites yet. Press ") + stKey.Render("n") + stDim.Render(" to create one.")
	}
	var b strings.Builder
	b.WriteString("    " + stHead.Render(fmt.Sprintf("%-20s %-5s %-30s %8s", "SITE", "PHP", "DOMAIN", "PREVIEWS")) + "\n")
	for i, s := range m.sites {
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
	if s := m.currentSite(); s != nil {
		b.WriteString("\n" + m.sitePanel(s))
	}
	return b.String()
}

// sitePanel holds what a table column would have had to truncate: the real
// URLs, the credentials, where it lives, how big it got.
func (m *model) sitePanel(s *Site) string {
	rows := [][2]string{
		{"open", BareURL(s) + stDim.Render("   "+s.SURL())},
		{"db", s.DBName + stDim.Render("  as ") + s.DBUser + stDim.Render(fmt.Sprintf("  127.0.0.1:%d", DefaultDBPort))},
		{"files", shortHome(s.WPDir) + stDim.Render("   "+m.siteSize(s.Slug))},
	}
	// Credentials only exist for sites we installed; an adopted folder keeps
	// whatever admin it already had, and inventing a blank pair reads as data.
	if s.AdminUser != "" {
		rows = append(rows[:1], append([][2]string{
			{"admin", BareURL(s) + "/wp-admin" + stDim.Render("   "+s.AdminUser+" / "+s.AdminPass)},
		}, rows[1:]...)...)
	}
	if wts := m.store.WorktreesFor(s.Slug); len(wts) > 0 {
		names := make([]string, 0, len(wts))
		for _, w := range wts {
			names = append(names, w.Branch+stDim.Render(" → "+BareDomainURL(w.Domain)))
		}
		rows = append(rows, [2]string{"preview", strings.Join(names, stDim.Render(", "))})
	}
	var body strings.Builder
	for i, r := range rows {
		if i > 0 {
			body.WriteString("\n")
		}
		body.WriteString(stLabel.Render(r[0]) + "  " + r[1])
	}
	return stPanel.Width(tableWidth).Render(body.String())
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
		}
		var body strings.Builder
		for i, r := range rows {
			if i > 0 {
				body.WriteString("\n")
			}
			body.WriteString(stLabel.Render(r[0]) + "  " + r[1])
		}
		b.WriteString("\n" + stPanel.Width(tableWidth).Render(body.String()))
	}
	return b.String()
}

func (m model) viewRuntimes() string {
	inv := m.store.Inventory()
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

func (m model) viewDoctor() string {
	if m.doctor == nil {
		m.doctor = Doctor(m.store)
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
	for _, f := range m.doctor.Findings {
		detail := f.Detail
		if f.Status == "ok" {
			detail = stDim.Render(detail) // failures should be the loud ones
		}
		line := fmt.Sprintf("%-*s", nameW, trunc(f.Check, nameW)) + "  " + detail
		b.WriteString(row(false, lampFor(f.Status)+" ", line))
		if f.Status != "ok" && f.FixCmd != "" {
			b.WriteString("\n" + strings.Repeat(" ", 4) + stDim.Render("fix: "+f.FixCmd))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// viewFooter lists only the keys that do something on this tab, with the
// universal ones parked on the right where they stop competing for attention.
func (m model) viewFooter(w int) string {
	var keys [][2]string
	switch m.tab {
	case tabSites:
		keys = [][2]string{{"n", "new"}, {"s", "start"}, {"x", "stop"}, {"R", "restart"},
			{"o", "open"}, {"a", "preview"}, {"p", "php"}, {"d", "domain"}, {"D", "delete"}}
	case tabWorktrees:
		keys = [][2]string{{"a", "add preview"}, {"s", "start"}, {"x", "stop"}, {"R", "restart"}, {"o", "open"}, {"D", "remove"}}
	case tabRuntimes:
		keys = [][2]string{{"i", "install php"}, {"m", "mariadb"}, {"h", "apache"}, {"b", "homebrew"}}
	case tabDoctor:
		keys = [][2]string{{"r", "re-run"}, {"f", "fix what can be fixed"}}
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, stKey.Render(k[0])+" "+stDim.Render(k[1]))
	}
	left := "  " + strings.Join(parts, stDim.Render(" · "))
	right := stKey.Render("⇥") + stDim.Render(" tab") + stDim.Render(" · ") + stKey.Render("q") + stDim.Render(" quit")
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 2 {
		return left + "\n  " + right
	}
	return left + strings.Repeat(" ", gap) + right
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
//	agent-local tui --frame 120 --tab doctor
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
	m := initialModel()
	m.width, m.height = w, 40
	switch flagValue(args, "--tab") {
	case "worktrees":
		m.tab = tabWorktrees
	case "runtimes":
		m.tab = tabRuntimes
	case "doctor":
		m.tab = tabDoctor
	}
	fmt.Println(m.View())
	return nil
}

// completeDir completes a directory path the way a shell would: unique match
// completes and opens, several matches complete the shared prefix and list what
// is left. Directories only — a site lives in one.
func completeDir(v string) (string, string) {
	raw := strings.TrimSpace(v)
	if raw == "" {
		raw = "~/"
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
	var names []string
	for _, e := range ents {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(e.Name()), strings.ToLower(base)) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	switch len(names) {
	case 0:
		if base == "" {
			return shortHome(dir) + string(os.PathSeparator), "empty — nothing to complete"
		}
		return v, "nothing matches " + base
	case 1:
		return shortHome(filepath.Join(dir, names[0])) + string(os.PathSeparator), ""
	default:
		completed := commonPrefix(names)
		out := shortHome(filepath.Join(dir, completed))
		if completed == base {
			// Already at the branch point: showing the options is the only
			// useful thing left to do.
			return out, strings.Join(trimList(names, 6), "  ")
		}
		return out, strings.Join(trimList(names, 6), "  ")
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
