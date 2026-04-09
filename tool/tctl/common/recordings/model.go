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
	"image/color"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	sessionsearchv1pb "github.com/gravitational/teleport/api/gen/proto/go/teleport/sessionsearch/v1"
)

// ── Shared styles ─────────────────────────────────────────────────────────────
// Styles are rebuilt whenever a BackgroundColorMsg is received so they adapt
// to the terminal's actual background colour.

const (
	// listWidthPercent is the fraction of the terminal width devoted to the
	// session list.  The detail pane takes the remainder.
	listWidthPercent = 40
)

// palette holds the per-run resolved colours.
type palette struct {
	accent  color.Color
	section color.Color
	faint   color.Color
}

func buildPalette(isDark bool) palette {
	ld := lipgloss.LightDark(isDark)
	return palette{
		accent:  ld(lipgloss.Color("62"), lipgloss.Color("205")),
		section: ld(lipgloss.Color("62"), lipgloss.Color("205")),
		faint:   ld(lipgloss.Color("243"), lipgloss.Color("240")),
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
// Navigating the list immediately refreshes the detail pane on the right.
type model struct {
	sessions []*sessionsearchv1pb.SessionSummary

	list   list.Model
	detail viewport.Model
	help   help.Model
	keys   keyMap

	palette palette
	isDark  bool
	width   int
	height  int
}

func newModel(sessions []*sessionsearchv1pb.SessionSummary) *model {
	items := make([]list.Item, len(sessions))
	for i, s := range sessions {
		items[i] = sessionItem{s: s}
	}

	p := buildPalette(true) // assume dark until BackgroundColorMsg arrives

	delegate := buildDelegate(p)
	l := list.New(items, delegate, 0, 0)
	l.Title = "Session Recordings"
	l.Styles.Title = lipgloss.NewStyle().Bold(true).Foreground(p.accent)
	l.SetShowHelp(false)

	vp := viewport.New()

	h := help.New()
	h.Styles = help.DefaultStyles(true)

	return &model{
		sessions: sessions,
		list:     l,
		detail:   vp,
		help:     h,
		keys:     defaultKeyMap(),
		palette:  p,
		isDark:   true,
	}
}

// buildDelegate creates a list delegate styled with the current palette.
func buildDelegate(p palette) list.DefaultDelegate {
	delegate := list.NewDefaultDelegate()
	delegate.Styles = list.NewDefaultItemStyles(true)
	delegate.Styles.SelectedTitle = lipgloss.NewStyle().
		Bold(true).
		Foreground(p.accent)
	delegate.Styles.SelectedDesc = lipgloss.NewStyle().Faint(false)
	return delegate
}

func (m *model) Init() tea.Cmd {
	return tea.RequestBackgroundColor
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.isDark = msg.IsDark()
		m.palette = buildPalette(m.isDark)
		m.applyPalette()
		m.refreshDetail()
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		m.refreshDetail()
		return m, nil

	case tea.KeyPressMsg:
		if m.list.FilterState() == list.Filtering {
			break
		}
		if key.Matches(msg, m.keys.Quit) {
			return m, tea.Quit
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

func (m *model) View() tea.View {
	if m.width == 0 {
		return tea.NewView("")
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
	m.detail.SetHeight(detailHeight)

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

	// Join the two columns side-by-side.
	full := lipgloss.JoinHorizontal(lipgloss.Top, leftContent, rightContent)

	v := tea.NewView(full)
	v.AltScreen = true
	return v
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
	m.detail.SetWidth(rightW)
}

// applyPalette rebuilds palette-sensitive styles on the list and help widget.
func (m *model) applyPalette() {
	p := m.palette
	delegate := buildDelegate(p)
	m.list.SetDelegate(delegate)
	m.list.Styles.Title = lipgloss.NewStyle().Bold(true).Foreground(p.accent)
	m.help.Styles = help.DefaultStyles(m.isDark)
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
