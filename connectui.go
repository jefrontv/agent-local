package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// connectModel is a standalone bubbletea program: a checklist of harnesses,
// apply, done. It borrows tui.go's palette and chip conventions rather than
// re-inventing a second visual language for one screen.

type connectRow struct {
	HarnessStatus
	selected bool
}

type connectModel struct {
	rows    []connectRow
	cur     int
	applied bool
	results []string // one line per harness, filled in after apply
	err     string
	width   int
}

func newConnectModel(statuses []HarnessStatus) connectModel {
	rows := make([]connectRow, len(statuses))
	for i, s := range statuses {
		rows[i] = connectRow{
			HarnessStatus: s,
			// The checkbox is the desired state: registered after apply. So it
			// starts checked wherever agent-local is (or should be) present —
			// configured, stale, or installed and waiting — and unchecking a
			// configured row is how you remove it.
			selected: s.Installed || s.Configured || s.Stale,
		}
	}
	return connectModel{rows: rows}
}

// pending names the change apply would make for a row, or "" for none.
func (r connectRow) pending() string {
	switch {
	case r.selected && r.Stale:
		return "update"
	case r.selected && !r.Configured:
		return "register"
	case !r.selected && (r.Configured || r.Stale):
		return "remove"
	}
	return ""
}

func runConnectTUI() error {
	statuses, err := DetectHarnesses()
	if err != nil {
		return err
	}
	m := newConnectModel(statuses)
	p := tea.NewProgram(m)
	final, err := p.Run()
	if err != nil {
		return err
	}
	if fm, ok := final.(connectModel); ok && fm.err != "" {
		return fmt.Errorf("%s", fm.err)
	}
	return nil
}

func (m connectModel) Init() tea.Cmd { return nil }

func (m connectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case tea.KeyMsg:
		if m.applied {
			return m, tea.Quit
		}
		switch msg.String() {
		case "up", "k":
			if m.cur > 0 {
				m.cur--
			}
		case "down", "j":
			if m.cur < len(m.rows)-1 {
				m.cur++
			}
		case " ":
			if len(m.rows) > 0 {
				m.rows[m.cur].selected = !m.rows[m.cur].selected
			}
		case "a":
			for i := range m.rows {
				if m.rows[i].Installed {
					m.rows[i].selected = true
				}
			}
		case "n":
			for i := range m.rows {
				m.rows[i].selected = false
			}
		case "enter":
			m.applyNow()
			m.applied = true
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *connectModel) applyNow() {
	var results []string
	for _, r := range m.rows {
		p := r.pending()
		if p == "" {
			continue
		}
		line, err := applyOne(r.Harness, p == "remove")
		if err != nil {
			line = "error: " + err.Error()
		}
		results = append(results, fmt.Sprintf("%-16s %s", r.ID, line))
	}
	if len(results) == 0 {
		results = []string{"no changes"}
	} else {
		results = append(results, "", "restart any running harness above to pick up the change.")
	}
	m.results = results
}

func (m connectModel) View() string {
	var b strings.Builder
	title := lipgloss.NewStyle().Bold(true).Foreground(cInk).Render("agent-local connect")
	b.WriteString(title + "\n")
	b.WriteString(stDim.Render("checked = registered after apply; uncheck a configured harness to remove it") + "\n\n")

	if m.applied {
		for _, line := range m.results {
			b.WriteString(line + "\n")
		}
		b.WriteString("\n" + stDim.Render("press any key to exit"))
		return b.String()
	}

	for i, r := range m.rows {
		gutter := "○"
		if r.selected {
			gutter = "●"
		}
		gutterStyle := stDim
		switch {
		case r.Configured:
			gutterStyle = stOK
		case r.Stale:
			gutterStyle = stWarn
		}
		label := fmt.Sprintf("%-16s %-28s %s", r.Name, statusLabel(r.HarnessStatus), stDim.Render(shortHome(r.Path)))
		switch r.pending() {
		case "register", "update":
			label += "  " + stOK.Render("→ "+r.pending())
		case "remove":
			label += "  " + stWarn.Render("→ remove")
		}
		line := gutterStyle.Render(gutter) + " " + label
		if i == m.cur {
			line = stSelRow.Render("› ") + line
		} else {
			line = "  " + line
		}
		b.WriteString(line + "\n")
	}

	b.WriteString("\n")
	chips := []string{
		keyChip("↑/↓", "move"),
		keyChip("space", "toggle"),
		keyChip("a", "all"),
		keyChip("n", "none"),
		keyChip("enter", "apply"),
		keyChip("q", "quit"),
	}
	b.WriteString(strings.Join(chips, "  ") + "\n")
	return b.String()
}
