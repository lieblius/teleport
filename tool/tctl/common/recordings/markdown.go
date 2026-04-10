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
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var orderedListRE = regexp.MustCompile(`^\d+\.\s+`)

func renderMarkdownForTerminal(markdown string, width int, p palette) string {
	if width < 20 {
		width = 20
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(p.section)
	subtitleStyle := lipgloss.NewStyle().Bold(true).Foreground(p.accent)
	codeStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.AdaptiveColor{Light: "236", Dark: "236"}).
		Padding(0, 1)
	quoteStyle := lipgloss.NewStyle().Foreground(p.faint).Italic(true)

	var out []string
	var paragraph []string
	var codeLines []string
	inCode := false

	flushParagraph := func(prefix string) {
		if len(paragraph) == 0 {
			return
		}
		text := strings.Join(paragraph, " ")
		wrapped := wrapText(text, width)
		if prefix != "" {
			lines := strings.Split(wrapped, "\n")
			for i, line := range lines {
				if i == 0 {
					lines[i] = prefix + line
				} else {
					lines[i] = strings.Repeat(" ", len(prefix)) + line
				}
			}
			wrapped = strings.Join(lines, "\n")
		}
		out = append(out, wrapped)
		paragraph = nil
	}

	flushCode := func() {
		if len(codeLines) == 0 {
			return
		}
		blockWidth := width
		longest := 0
		for _, line := range codeLines {
			if len(line) > longest {
				longest = len(line)
			}
		}
		if longest+2 < blockWidth {
			blockWidth = longest + 2
		}
		for _, line := range codeLines {
			out = append(out, codeStyle.Width(blockWidth).Render(line))
		}
		codeLines = nil
	}

	for _, rawLine := range strings.Split(markdown, "\n") {
		line := strings.TrimRight(rawLine, " \t")
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			flushParagraph("")
			if inCode {
				flushCode()
			}
			inCode = !inCode
			continue
		}

		if inCode {
			codeLines = append(codeLines, line)
			continue
		}

		if trimmed == "" {
			flushParagraph("")
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}

		if strings.HasPrefix(trimmed, "#") {
			flushParagraph("")
			level := 0
			for level < len(trimmed) && trimmed[level] == '#' {
				level++
			}
			text := strings.TrimSpace(trimmed[level:])
			switch level {
			case 1:
				out = append(out, titleStyle.Render(text))
			default:
				out = append(out, subtitleStyle.Render(text))
			}
			continue
		}

		if strings.HasPrefix(trimmed, ">") {
			flushParagraph("")
			text := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
			out = append(out, quoteStyle.Render(wrapText(text, width-2)))
			continue
		}

		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ ") {
			flushParagraph("")
			out = append(out, formatBulletLine(trimmed[2:], width, "• "))
			continue
		}

		if orderedListRE.MatchString(trimmed) {
			flushParagraph("")
			match := orderedListRE.FindString(trimmed)
			out = append(out, formatBulletLine(strings.TrimPrefix(trimmed, match), width, match))
			continue
		}

		if strings.HasSuffix(trimmed, ":") && !strings.Contains(trimmed, " ") {
			flushParagraph("")
			out = append(out, subtitleStyle.Render(trimmed))
			continue
		}

		paragraph = append(paragraph, trimmed)
	}

	flushParagraph("")
	flushCode()

	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func formatBulletLine(text string, width int, prefix string) string {
	if width <= len(prefix) {
		return prefix + text
	}
	lines := strings.Split(wrapText(text, width-len(prefix)), "\n")
	for i, line := range lines {
		if i == 0 {
			lines[i] = prefix + line
		} else {
			lines[i] = strings.Repeat(" ", len(prefix)) + line
		}
	}
	return strings.Join(lines, "\n")
}

// wrapText breaks text into lines not exceeding maxWidth characters.
func wrapText(text string, maxWidth int) string {
	if maxWidth <= 0 {
		return text
	}
	var result strings.Builder
	lineLen := 0
	first := true
	for _, word := range strings.Fields(text) {
		if !first && lineLen+1+len(word) > maxWidth {
			result.WriteString("\n")
			lineLen = 0
			first = true
		} else if !first {
			result.WriteByte(' ')
			lineLen++
		}
		result.WriteString(word)
		lineLen += len(word)
		first = false
	}
	return result.String()
}
