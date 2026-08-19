package ui

import (
	"hash/fnv"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// One place for the palette, so the screen reads as a single design rather
// than a pile of escape codes scattered through the render functions.
var (
	colourAccent = lipgloss.Color("#25D366") // WhatsApp green
	colourPeer   = lipgloss.Color("#58A6FF")
	colourMuted  = lipgloss.Color("#8B949E")
	colourBorder = lipgloss.Color("#30363D")
	colourWarn   = lipgloss.Color("#F85149")
	colourReact  = lipgloss.Color("#E3B341")
)

// senderPalette gives each person in a group their own colour, so a busy chat
// can be followed by eye rather than by reading every name.
var senderPalette = []color.Color{
	lipgloss.Color("#58A6FF"), // blue
	lipgloss.Color("#D2A8FF"), // purple
	lipgloss.Color("#7EE787"), // green
	lipgloss.Color("#FFA657"), // orange
	lipgloss.Color("#F778BA"), // pink
	lipgloss.Color("#79C0FF"), // sky
	lipgloss.Color("#FFD866"), // yellow
	lipgloss.Color("#8DDB8C"), // mint
}

// senderStyle picks a stable colour for a person from their JID, so the same
// person is the same colour every time the chat is opened.
func senderStyle(jid string) lipgloss.Style {
	hash := fnv.New32a()
	hash.Write([]byte(jid))
	colour := senderPalette[int(hash.Sum32())%len(senderPalette)]
	return lipgloss.NewStyle().Foreground(colour).Bold(true)
}

var (
	titleStyle  = lipgloss.NewStyle().Bold(true)
	accentStyle = lipgloss.NewStyle().Foreground(colourAccent)
	nameStyle   = lipgloss.NewStyle().Foreground(colourAccent).Bold(true)
	peerStyle   = lipgloss.NewStyle().Foreground(colourPeer).Bold(true)
	mutedStyle  = lipgloss.NewStyle().Foreground(colourMuted)
	warnStyle   = lipgloss.NewStyle().Foreground(colourWarn)
	reactStyle  = lipgloss.NewStyle().Foreground(colourReact)
	ruleStyle   = lipgloss.NewStyle().Foreground(colourBorder)
)

// frame draws a rounded box around lines that are already exactly width cells,
// brightening the border when that pane has the keyboard.
//
// This is done by hand rather than with lipgloss's Border, because lipgloss
// re-pads a block to its own width measurement. That measurement is the
// grapheme-cluster one, so it would undo the extra columns layoutWidth
// reserves for scripts a terminal may not shape -- which is exactly the
// padding that stops a Malayalam chat name from bursting out of the panel.
// lipgloss is still used for colour, which costs no width.
func frame(lines []string, width int, focused bool) []string {
	edge := ruleStyle
	if focused {
		edge = accentStyle
	}

	side := edge.Render("│")
	out := make([]string, 0, len(lines)+2)

	out = append(out, edge.Render("╭"+strings.Repeat("─", width)+"╮"))
	for _, line := range lines {
		out = append(out, side+line+side)
	}
	return append(out, edge.Render("╰"+strings.Repeat("─", width)+"╯"))
}

// rule is a horizontal divider of exactly width cells, inset by one column at
// each end so it reads as a divider rather than as part of the border.
func rule(width int) string {
	if width <= 2 {
		return strings.Repeat(" ", max(width, 0))
	}
	return " " + ruleStyle.Render(strings.Repeat("─", width-2)) + " "
}
