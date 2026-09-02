package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// One voice for every command's terminal output, in the TUI's register: a
// title, dim right-aligned labels, one lamp per finished step, keys in steel,
// subprocess chatter in a gutter. Colour disappears on a pipe (lipgloss reads
// the terminal); the structure stays, so scripts see the same shape.
//
//	agent-local create mysite
//	  ● database    al_mysite created
//	  ● wordpress   6.6.2 installed
//
//	     url  https://mysite.test
//	   admin  https://mysite.test/wp-admin  admin / s3cret
//	    next  agent-local open mysite

var (
	stGutter = lipgloss.NewStyle().Foreground(cOff)
	stCell   = lipgloss.NewStyle().Foreground(cDim)
	stOutLbl = lipgloss.NewStyle().Foreground(cDim).Width(9).Align(lipgloss.Right)
)

// outTitle opens a command's output with what is happening.
func outTitle(words ...string) { fmt.Println(stName.Render(strings.Join(words, " "))) }

// outRow is a labelled value: right-aligned dim label, two spaces, value.
func outRow(label, value string) { fmt.Println(stOutLbl.Render(label) + "  " + value) }

// outStep is a finished stage, lamp lit.
func outStep(msg string) { fmt.Println("  " + stOK.Render("●") + " " + msg) }

// outStage is a finished stage with a short stage name in a fixed column.
func outStage(stage, detail string) {
	fmt.Println("  " + stOK.Render("●") + " " + stDim.Render(col(stage, 11)) + detail)
}

// col pads a word to a column width; a longer word keeps one space after it
// rather than being wrapped or cut.
func col(s string, w int) string {
	if len(s) >= w {
		return s + " "
	}
	return s + strings.Repeat(" ", w-len(s))
}

// outNote is neutral information under a title, no verdict attached.
func outNote(msg string) { fmt.Println("  " + stDim.Render(msg)) }

// outWarn is a soft problem; it goes to stderr like every warning.
func outWarn(msg string) { fmt.Fprintln(os.Stderr, "  "+stWarn.Render("●")+" "+msg) }

// outFail is the failure line the process ends on.
func outFail(msg string) { fmt.Fprintln(os.Stderr, "  "+stErr.Render("●")+" "+msg) }

// outHint says what to do next, the command styled as a key.
func outHint(text, cmd string) { fmt.Println(stOutLbl.Render(text) + "  " + stKey.Render(cmd)) }

// outDone is the closing line: what the command achieved, in the lamp colour.
func outDone(msg string) { fmt.Println(stOK.Render(msg)) }

func outBlank() { fmt.Println() }

// outState renders a running/stopped pair the way the TUI does: one lamp,
// lit or parked, then the word.
func outState(running bool) string {
	if running {
		return stOK.Render("●") + " running"
	}
	return stGutter.Render("●") + " " + stDim.Render("stopped")
}

// outSub routes a subprocess line (brew, git) into a dim gutter, so the
// tool's own steps stay legible above it. brew's "==>" headers are its real
// milestones and are promoted to steps; its progress bars are dropped.
func outSub(line string) {
	line = strings.TrimRight(line, "\r\n")
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return
	}
	if strings.HasPrefix(trimmed, "==> ") {
		outStep(strings.TrimPrefix(trimmed, "==> "))
		return
	}
	if strings.Trim(trimmed, "#-=> %.0123456789") == "" {
		return // curl/brew progress bars
	}
	fmt.Println("  " + stGutter.Render("│") + " " + stCell.Render(line))
}

// outTable prints aligned columns under a dim uppercase head. Cells may carry
// colour; widths are measured after styling.
func outTable(head []string, rows [][]string) {
	w := make([]int, len(head))
	for i, h := range head {
		w[i] = lipgloss.Width(h)
	}
	for _, r := range rows {
		for i := range head {
			if i < len(r) && lipgloss.Width(r[i]) > w[i] {
				w[i] = lipgloss.Width(r[i])
			}
		}
	}
	pad := func(s string, n int) string {
		return s + strings.Repeat(" ", n-lipgloss.Width(s))
	}
	var b strings.Builder
	for i, h := range head {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(pad(stCell.Render(strings.ToUpper(h)), w[i]))
	}
	fmt.Println(strings.TrimRight(b.String(), " "))
	for _, r := range rows {
		b.Reset()
		for i := range head {
			cell := ""
			if i < len(r) {
				cell = r[i]
			}
			if i > 0 {
				b.WriteString("  ")
			}
			if i == len(head)-1 {
				b.WriteString(cell)
			} else {
				b.WriteString(pad(cell, w[i]))
			}
		}
		fmt.Println(strings.TrimRight(b.String(), " "))
	}
}

// dimf is a dim fragment inside an otherwise plain line.
func dimf(s string) string { return stDim.Render(s) }

// keyf is a command fragment inside an otherwise plain line.
func keyf(s string) string { return stKey.Render(s) }

// outStateWord is outState with a word other than running/stopped.
func outStateWord(on bool, word string) string {
	if on {
		return stOK.Render("●") + " " + word
	}
	return stGutter.Render("●") + " " + stDim.Render(word)
}
