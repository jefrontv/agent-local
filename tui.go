package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---------- styles ----------

var (
	stTitle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).MarginBottom(1)
	stTabOn   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("205")).Padding(0, 1)
	stTabOff  = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Padding(0, 1)
	stSel     = lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Background(lipgloss.Color("63"))
	stRun     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	stStop    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	stErr     = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	stOK      = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	stWarn    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	stDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	stKey     = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true)
	stStatus  = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	stDetailK = lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Width(12)
)

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
	inputNone inputTarget = iota
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
}

type model struct {
	store    *Store
	engine   *Engine
	tab      tab
	cursor   int
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
	quitting bool
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

func (m *model) refresh() {
	m.sites = m.store.Sites()
	if m.cursor >= len(m.sites) && m.cursor > 0 {
		m.cursor = len(m.sites) - 1
	}
}

func (m model) Init() tea.Cmd { return nil }

func (m *model) setMsg(s string, isErr bool) { m.msg = s; m.msgErr = isErr }

func (m model) currentSite() *Site {
	if len(m.sites) == 0 {
		return nil
	}
	return m.sites[m.cursor]
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
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j":
		m.moveDown()
		return m, nil
	case "r":
		m.refresh()
		m.store.Save()
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

func (m *model) moveDown() {
	max := len(m.sites) - 1
	switch m.tab {
	case tabRuntimes:
		max = len(m.store.Inventory().PHPs) - 1
	case tabDoctor:
		if m.doctor != nil {
			max = len(m.doctor.Findings) - 1
		}
	case tabWorktrees:
		if s := m.currentSite(); s != nil {
			max = len(m.store.WorktreesFor(s.Slug)) - 1
		} else {
			max = -1
		}
	}
	if m.cursor < max {
		m.cursor++
	}
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
	case "backspace":
		if len(m.input.value) > 0 {
			m.input.value = m.input.value[:len(m.input.value)-1]
		}
		return m, nil
	case "space":
		m.input.value += " "
		return m, nil
	default:
		if len(k.String()) == 1 {
			m.input.value += k.String()
		}
		return m, nil
	}
}

func (m *model) applyInput(spec inputSpec) error {
	val := strings.TrimSpace(spec.value)
	switch spec.target {
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
		return m.startInput(inputCreateName, "new site name", "lowercase, becomes slug"+m.store.Suffix(), ""), nil
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
		_ = m.engine.StopSite(site.Slug)
		err := m.engine.StartSite(site.Slug)
		m.busyEnd()
		if err != nil {
			m.setMsg(err.Error(), true)
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
	site := m.currentSite()
	if site == nil {
		m.setMsg("create a site first (tab Sites, press n)", true)
		return m, nil
	}
	wts := m.store.WorktreesFor(site.Slug)
	switch k.String() {
	case "a":
		return m.startInput(inputWorktreeBranch, "branch for worktree of "+site.Slug, "creates + serves on own domain", site.Slug), nil
	case "s":
		if m.cursor < len(wts) {
			if err := m.engine.StartWorktree(wts[m.cursor].ID); err != nil {
				m.setMsg(err.Error(), true)
			} else {
				m.setMsg("worktree running: http://"+wts[m.cursor].Domain+fmt.Sprintf(":%d", DefaultHTTPPort), false)
			}
		}
		return m, nil
	case "x":
		if m.cursor < len(wts) {
			m.engine.StopWorktree(wts[m.cursor].ID)
			m.setMsg("stopped", false)
		}
		return m, nil
	case "D":
		if m.cursor < len(wts) {
			id := wts[m.cursor].ID
			m.confirm = "remove worktree " + id + "?"
			m.confirmA = func() error { return m.engine.RemoveWorktree(id) }
			m.mode = modeConfirm
		}
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
	var b strings.Builder
	b.WriteString(stTitle.Render("AGENT-LOCAL — local WordPress, TUI"))
	b.WriteString("\n")
	tabs := []struct {
		name string
		t    tab
	}{{"Sites", tabSites}, {"Worktrees", tabWorktrees}, {"Runtimes", tabRuntimes}, {"Doctor", tabDoctor}}
	var line []string
	for _, t := range tabs {
		if t.t == m.tab {
			line = append(line, stTabOn.Render(t.name))
		} else {
			line = append(line, stTabOff.Render(t.name))
		}
	}
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, line...) + "\n\n")

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

	b.WriteString("\n" + m.viewStatus())
	b.WriteString("\n" + m.viewHelp())

	if m.mode == modeConfirm {
		b.WriteString("\n\n" + stWarn.Render("⚠ "+m.confirm) + "  " + stKey.Render("y") + "es / " + stKey.Render("n") + "o\n")
	}
	if m.mode == modeBusy {
		b.WriteString("\n" + stWarn.Render("⣾ "+m.busy))
	}
	if m.msg != "" {
		st := stOK
		if m.msgErr {
			st = stErr
		}
		b.WriteString("\n" + st.Render(m.msg))
	}
	if m.mode == modeInput {
		b.WriteString("\n" + stKey.Render(m.input.prompt+": ") + stSel.Render(m.input.value+"█"))
		if m.input.hint != "" {
			b.WriteString("  " + stDim.Render(m.input.hint))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m model) viewSites() string {
	if len(m.sites) == 0 {
		return stDim.Render("no sites yet — press n to create one")
	}
	var b strings.Builder
	b.WriteString(stDim.Render(fmt.Sprintf("%-22s %-6s %-26s %-9s %s", "SLUG", "PHP", "DOMAIN", "STATE", "URL")) + "\n")
	for i, s := range m.sites {
		state := stStop.Render("stopped")
		if m.engine.FPMRunning(s.Slug) {
			state = stRun.Render("running")
		}
		row := fmt.Sprintf("%-22s %-6s %-26s %s  %s", s.Slug, s.PHPVersion, s.Domain, pad(state, 9), BareURL(s))
		if i == m.cursor {
			row = stSel.Render("▸ " + row)
		} else {
			row = "  " + row
		}
		b.WriteString(row + "\n")
	}
	// detail panel for selection
	if s := m.currentSite(); s != nil {
		b.WriteString("\n" + stDim.Render("── selected ─────────────────────────────────") + "\n")
		rows := [][2]string{
			{"https", s.SURL()},
			{"admin", BareURL(s) + "/wp-admin  (" + s.AdminUser + " / " + s.AdminPass + ")"},
			{"path", s.WPDir},
			{"db", fmt.Sprintf("%s@127.0.0.1:%d/%s", s.DBUser, DefaultDBPort, s.DBName)},
			{"size", SiteDirSize(s.Slug)},
		}
		for _, r := range rows {
			b.WriteString(stDetailK.Render(r[0]) + r[1] + "\n")
		}
	}
	return b.String()
}

func pad(s string, w int) string {
	// rough width pad ignoring ansi (display width close enough)
	plain := lipgloss.Width(s)
	if plain >= w {
		return s
	}
	return s + strings.Repeat(" ", w-plain)
}

func (m model) viewWorktrees() string {
	site := m.currentSite()
	if site == nil {
		return stDim.Render("create a site first (Sites tab, press n)")
	}
	wts := m.store.WorktreesFor(site.Slug)
	var b strings.Builder
	b.WriteString(stDim.Render("worktrees of "+site.Slug+"  (a=add, s=start, x=stop, D=remove)") + "\n")
	if len(wts) == 0 {
		return b.String() + stDim.Render("none — press a to add a branch")
	}
	for i, w := range wts {
		state := stStop.Render("stopped")
		if m.engine.FPMRunning(w.ID) {
			state = stRun.Render("running")
		}
		row := fmt.Sprintf("%-24s %s  http://%s:%d  %s", w.Branch, pad(state, 9), w.Domain, DefaultHTTPPort, w.Path)
		if i == m.cursor {
			row = stSel.Render("▸ " + row)
		} else {
			row = "  " + row
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

func (m model) viewRuntimes() string {
	var b strings.Builder
	inv := m.store.Inventory()
	b.WriteString(stDim.Render("PHP toolchains (i=install new)") + "\n")
	for i, rt := range inv.PHPs {
		fpm := "no-fpm"
		if rt.FPM != "" {
			fpm = "fpm ✓"
		}
		row := fmt.Sprintf("php %-6s %-8s %s  %s", rt.Version, fpm, rt.Source, rt.Bin)
		if i == m.cursor {
			row = stSel.Render("▸ " + row)
		} else {
			row = "  " + row
		}
		b.WriteString(row + "\n")
	}
	b.WriteString("\n" + stDim.Render("database") + "\n")
	if inv.MySQL.Bin == "" {
		b.WriteString(stErr.Render("  none installed — press m to install MariaDB") + "\n")
	} else {
		b.WriteString(fmt.Sprintf("  %s %s  %s\n", inv.MySQL.Kind, inv.MySQL.Version, stateWord(m.engine.DBRunning())))
	}
	b.WriteString("\n" + stDim.Render("http front") + "\n")
	if inv.HTTP.Bin != "" {
		b.WriteString(fmt.Sprintf("  apache %s (h=reinstall)\n", inv.HTTP.Version))
	} else {
		b.WriteString("  built-in router (Go FastCGI vhost proxy) — press h to install Apache alternative\n")
	}
	if inv.Brew == "" {
		b.WriteString("\n" + stErr.Render("homebrew missing — press b to install") + "\n")
	} else {
		b.WriteString("\n" + stDim.Render("homebrew: "+inv.Brew) + "\n")
	}
	return b.String()
}

func stateWord(on bool) string {
	if on {
		return stRun.Render("running")
	}
	return stStop.Render("stopped")
}

func (m model) viewDoctor() string {
	if m.doctor == nil {
		m.doctor = Doctor(m.store)
	}
	var b strings.Builder
	b.WriteString(stDim.Render("health checks (r=re-run, f=auto-fix)") + "\n")
	for _, f := range m.doctor.Findings {
		icon := stOK.Render("✓")
		if f.Status == "warn" {
			icon = stWarn.Render("!")
		} else if f.Status == "fail" {
			icon = stErr.Render("✗")
		}
		line := fmt.Sprintf(" %s %-14s %s", icon, f.Check, f.Detail)
		if f.Status != "ok" && f.FixCmd != "" {
			line += "  " + stDim.Render("fix: "+f.FixCmd)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func (m model) viewStatus() string {
	e := m.engine
	parts := []string{
		"db " + stateWord(e.DBRunning()),
		"http:" + fmt.Sprint(DefaultHTTPPort) + " " + stateWord(portOpen(DefaultHTTPPort)),
		"https:" + fmt.Sprint(DefaultHTTPSPort) + " " + stateWord(portOpen(DefaultHTTPSPort)),
		fmt.Sprintf("%d sites", len(m.sites)),
	}
	return stStatus.Render(strings.Join(parts, "  ·  "))
}

func (m model) viewHelp() string {
	var help string
	switch m.tab {
	case tabSites:
		help = "n new  s start  x stop  R restart  p php  d domain  D delete  o open  tab switch  q quit"
	case tabWorktrees:
		help = "a add branch  s start  x stop  D remove  tab switch  q quit"
	case tabRuntimes:
		help = "i install php  m mariadb  h apache  b brew  tab switch  q quit"
	case tabDoctor:
		help = "r re-run  f auto-fix  tab switch  q quit"
	}
	return stDim.Render(help)
}

// runTUI starts the bubbletea program.
func runTUI() {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Println("tui error:", err)
	}
}
