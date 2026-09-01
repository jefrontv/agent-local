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
			// Preselect anything actionable: installed-but-unconfigured or stale.
			selected: s.Installed && (!s.Configured || s.Stale),
		}
	}
	return connectModel{rows: rows}
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
				if m.rows[i].Installed && (!m.rows[i].Configured || m.rows[i].Stale) {
					m.rows[i].selected = true
				}
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
		if !r.selected {
			continue
		}
		wrote, err := ConnectHarness(r.Harness)
		switch {
		case err != nil:
			results = append(results, fmt.Sprintf("%-16s error: %s", r.ID, err.Error()))
		case wrote:
			results = append(results, fmt.Sprintf("%-16s wrote %s", r.ID, shortHome(r.Path)))
		default:
			results = append(results, fmt.Sprintf("%-16s already configured (%s)", r.ID, shortHome(r.Path)))
		}
	}
	if len(results) == 0 {
		results = []string{"nothing selected"}
	} else {
		results = append(results, "", "restart any running harness above to pick up the new server.")
	}
	m.results = results
}

func (m connectModel) View() string {
	var b strings.Builder
	title := lipgloss.NewStyle().Bold(true).Foreground(cInk).Render("agent-local connect")
	b.WriteString(title + "\n")
	b.WriteString(stDim.Render("register the agent-local MCP server in a coding-agent harness") + "\n\n")

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
		keyChip("a", "select all"),
		keyChip("enter", "apply"),
		keyChip("q", "quit"),
	}
	b.WriteString(strings.Join(chips, "  ") + "\n")
	return b.String()
}
