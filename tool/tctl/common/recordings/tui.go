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

// Package recordings provides an interactive TUI for browsing session
// recording search results returned by tctl recordings search.
package recordings

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/gravitational/trace"

	sessionsearchv1pb "github.com/gravitational/teleport/api/gen/proto/go/teleport/sessionsearch/v1"
)

// RunSearchTUI launches the full-screen interactive TUI for browsing session
// search results. It blocks until the user quits.
//
// summaryGetter may be nil; the TUI degrades gracefully when summaries are not
// available.
func RunSearchTUI(
	sessions []*sessionsearchv1pb.SessionSummary,
	summaryGetter SummaryGetter,
) error {
	p := tea.NewProgram(newModel(sessions, summaryGetter), tea.WithAltScreen())
	_, err := p.Run()
	return trace.Wrap(err)
}
