/*
 * Teleport
 * Copyright (C) 2026  Gravitational, Inc.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

package recordings

import (
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	sessionsearchv1pb "github.com/gravitational/teleport/api/gen/proto/go/teleport/sessionsearch/v1"
)

// ── Shared styles ─────────────────────────────────────────────────────────────

const (
	// listWidthPercent is the fraction of the terminal width devoted to the
	// session list.  The detail pane takes the remainder.
	listWidthPercent = 40
)

// palette holds the per-run resolved colours.
type palette struct {
	accent  lipgloss.TerminalColor
	section lipgloss.TerminalColor
	faint   lipgloss.TerminalColor
}

func buildPalette() palette {
	return palette{
		accent:  lipgloss.AdaptiveColor{Light: "62", Dark: "205"},
		section: lipgloss.AdaptiveColor{Light: "62", Dark: "205"},
		faint:   lipgloss.AdaptiveColor{Light: "243", Dark: "240"},
	}
}

// ── Model ─────────────────────────────────────────────────────────────────────

// model is the bubbletea model for the session search TUI.
//
// Layout: two fixed columns that fill the terminal:
//
//	┌─────────────── 40% ────────────┬──────────────── 60% ──────────────────┐
//	│  [DB] prod-db  2025-01-01 ...  │  ● Session                            │
//	│  [SSH] bastion 2025-01-01 ...  │    ID:   abc-123                      │
//	│  …                             │    Kind: db                            │
//	│                                │  ● User                                │
//	│                                │    Username: alice                     │
//	└────────────────────────────────┴───────────────────────────────────────┘
//
// Pressing Enter on a session opens a summary popup for the selected session.
type model struct {
	sessions []*sessionsearchv1pb.SessionSummary

	list   list.Model
	detail viewport.Model
	help   help.Model
	keys   keyMap

	// popup is non-nil while the summary popup is active.
	popup *summaryPopupModel

	// summaryGetter is forwarded to the popup on Enter.
	summaryGetter SummaryGetter

	palette palette
	width   int
	height  int
}

func newModel(
	sessions []*sessionsearchv1pb.SessionSummary,
	summaryGetter SummaryGetter,
) *model {
	items := make([]list.Item, len(sessions))
	for i, s := range sessions {
		items[i] = sessionItem{s: s}
	}

	p := buildPalette()

	delegate := buildDelegate(p)
	l := list.New(items, delegate, 0, 0)
	l.Title = "Session Recordings"
	l.Styles.Title = lipgloss.NewStyle().Bold(true).Foreground(p.accent)
	l.SetShowHelp(false)

	vp := viewport.New(0, 0)

	return &model{
		sessions:      sessions,
		list:          l,
		detail:        vp,
		help:          help.New(),
		keys:          defaultKeyMap(),
		summaryGetter: summaryGetter,
		palette:       p,
	}
}

// buildDelegate creates a list delegate styled with the current palette.
func buildDelegate(p palette) list.DefaultDelegate {
	delegate := list.NewDefaultDelegate()
	delegate.Styles = list.NewDefaultItemStyles()
	delegate.Styles.SelectedTitle = lipgloss.NewStyle().
		Bold(true).
		Foreground(p.accent)
	delegate.Styles.SelectedDesc = lipgloss.NewStyle().Faint(false)
	return delegate
}

func (m *model) Init() tea.Cmd {
	return nil
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// ── Popup mode ────────────────────────────────────────────────────────────
	if m.popup != nil {
		switch msg.(type) {
		case closeSummaryMsg:
			m.popup = nil
			return m, nil
		}
		updated, cmd := m.popup.Update(msg)
		m.popup = updated.(*summaryPopupModel)
		return m, cmd
	}

	// ── List mode ─────────────────────────────────────────────────────────────
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		m.refreshDetail()
		return m, nil

	case tea.KeyMsg:
		if m.list.FilterState() == list.Filtering {
			break
		}
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Enter):
			if item, ok := m.list.SelectedItem().(sessionItem); ok {
				popup := newSummaryPopupModel(item.s, m.summaryGetter, m.palette, m.width, m.height)
				m.popup = popup
				return m, popup.Init()
			}
		}
	}

	// Let the list consume the message; afterwards refresh the detail pane so
	// it always tracks the currently selected item.
	prevIdx := m.list.Index()
	var listCmd tea.Cmd
	m.list, listCmd = m.list.Update(msg)
	cmds = append(cmds, listCmd)

	if m.list.Index() != prevIdx || prevIdx == 0 {
		m.refreshDetail()
	}

	return m, tea.Batch(cmds...)
}

func (m *model) View() string {
	if m.width == 0 {
		return ""
	}

	leftW, rightW := m.splitWidths()

	// Left column: list.
	leftContent := lipgloss.NewStyle().
		Width(leftW).
		MaxWidth(leftW).
		Render(m.list.View())

	// Right column: vertical rule + detail viewport + help bar.
	helpBar := m.help.View(m.keys)
	detailHeight := m.height - lipgloss.Height(helpBar) - 1 // -1 for separator
	m.detail.Height = detailHeight

	sep := lipgloss.NewStyle().
		Faint(true).
		Render(strings.Repeat("─", rightW))

	rightContent := lipgloss.JoinVertical(lipgloss.Left,
		sep,
		m.detail.View(),
		helpBar,
	)
	rightContent = lipgloss.NewStyle().
		Width(rightW).
		MaxWidth(rightW).
		Render(rightContent)

	base := lipgloss.JoinHorizontal(lipgloss.Top, leftContent, rightContent)
	if m.popup == nil {
		return base
	}
	return m.popup.View()
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// splitWidths returns the pixel widths of the left (list) and right (detail)
// panes.
func (m *model) splitWidths() (left, right int) {
	left = m.width * listWidthPercent / 100
	right = m.width - left
	return
}

// resize propagates the current terminal size to child components.
func (m *model) resize() {
	leftW, rightW := m.splitWidths()
	m.list.SetSize(leftW, m.height)
	m.detail.Width = rightW
}

// refreshDetail re-renders the detail viewport for the currently selected
// item.
func (m *model) refreshDetail() {
	item, ok := m.list.SelectedItem().(sessionItem)
	if !ok {
		m.detail.SetContent("No session selected.")
		return
	}
	m.detail.SetContent(renderDetail(item.s, m.palette))
}
