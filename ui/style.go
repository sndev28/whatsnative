package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// Raw escapes for the two places lipgloss is the wrong tool: the image
// renderer sets a different colour on every single cell, and the text cursor
// inverts exactly one character.
const (
	reset   = "\x1b[0m"
	reverse = "\x1b[7m"
)

// displayWidth is the width Bubble Tea's renderer itself would measure: one
// cell per grapheme cluster, escape sequences ignored.
func displayWidth(s string) int {
	return ansi.StringWidth(s)
}

// layoutWidth is how many columns a string occupies, in the same units Bubble
// Tea uses to lay out the screen. See width.go for why that distinction is the
// whole ballgame.
func layoutWidth(s string) int {
	return measure(s)
}

// truncate clips plain text to at most width cells, appending an ellipsis when
// it cuts.
//
// It steps a grapheme cluster at a time so a conjunct or an emoji is never
// split down the middle, which would leave a combining mark to attach itself
// to whatever character followed -- in a panel, the border.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if layoutWidth(s) <= width {
		return s
	}

	// Styled input: hand it to x/ansi so the escapes survive and the colour is
	// closed, shrinking the target until the pessimistic measure fits. Callers
	// normally fit plain text and style it afterwards, so this is the rare path.
	if strings.ContainsRune(s, 0x1b) {
		cut := s
		for target := displayWidth(s) - 1; target >= 0 && layoutWidth(cut) > width; target-- {
			cut = ansi.Truncate(s, target, "…")
		}
		return cut
	}

	var out strings.Builder
	used := 0

	graphemes := uniseg.NewGraphemes(s)
	for graphemes.Next() {
		cluster := graphemes.Str()
		clusterWidth := layoutWidth(cluster)
		// Leave a cell for the ellipsis.
		if used+clusterWidth > width-1 {
			break
		}
		out.WriteString(cluster)
		used += clusterWidth
	}

	out.WriteString("…")
	return out.String()
}

// cell fits a styled string to exactly width cells, clipping or padding. Every
// line handed to a panel goes through this, which is what keeps the borders
// straight whatever alphabet the text is in.
func cell(s string, width int) string {
	if width <= 0 {
		return ""
	}

	// Callers fit their plain text first, so this clip is a backstop. It works
	// on the styled string, shrinking a cell at a time until the pessimistic
	// measure fits.
	for layoutWidth(s) > width {
		shorter := ansi.Truncate(s, max(displayWidth(s)-1, 0), "")
		if shorter == s {
			break
		}
		s = shorter
	}

	if gap := width - layoutWidth(s); gap > 0 {
		s += strings.Repeat(" ", gap)
	}
	return s
}

// blanks builds n empty lines of the given width.
func blanks(n, width int) []string {
	lines := make([]string, 0, max(n, 0))
	for range max(n, 0) {
		lines = append(lines, cell("", width))
	}
	return lines
}

// oneLine flattens a multi-line string so it can sit in a list row.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
}

// wrap splits plain text into lines of at most width cells, breaking on spaces
// where possible and mid-word when a single word is too long to fit.
func wrap(s string, width int) []string {
	if width <= 0 {
		return nil
	}

	var lines []string
	for _, paragraph := range strings.Split(s, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}

		line := ""
		for _, word := range words {
			switch {
			case line == "":
				line = word
			case layoutWidth(line)+1+layoutWidth(word) <= width:
				line += " " + word
			default:
				lines = append(lines, line)
				line = word
			}

			// A single word can still be wider than the whole column. Split
			// it on grapheme boundaries, using the same pessimistic measure.
			for layoutWidth(line) > width {
				head := truncateHard(line, width)
				if head == "" {
					// Column too narrow for even one grapheme; stop rather
					// than spin forever.
					break
				}
				lines = append(lines, head)
				line = strings.TrimPrefix(line, head)
			}
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// truncateHard clips plain text to width cells with no ellipsis, on grapheme
// boundaries. Used when a single unbroken word has to be split across lines.
func truncateHard(s string, width int) string {
	var out strings.Builder
	used := 0

	graphemes := uniseg.NewGraphemes(s)
	for graphemes.Next() {
		cluster := graphemes.Str()
		clusterWidth := layoutWidth(cluster)
		if used+clusterWidth > width {
			break
		}
		out.WriteString(cluster)
		used += clusterWidth
	}
	return out.String()
}
