package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// settingRow is one togglable preference: how to read its current value, and
// what to do when it is flipped.
type settingRow struct {
	label string
	get   func() bool
	// toggle flips the value on app -- immediately, since app is shared by
	// pointer and every page sees the change the instant this runs -- and
	// returns the command that makes it stick past a restart.
	toggle func() tea.Cmd
}

// settingsRows is rebuilt on every render rather than stored, so it always
// closes over the current app pointer and never goes stale.
func settingsRows(a *app) []settingRow {
	return []settingRow{
		{
			label: "Show photos",
			get:   func() bool { return a.showPhotos },
			toggle: func() tea.Cmd {
				a.showPhotos = !a.showPhotos
				return saveSetting(a.messages.SetShowPhotos, a.showPhotos)
			},
		},
		{
			label: "Show stickers",
			get:   func() bool { return a.showStickers },
			toggle: func() tea.Cmd {
				a.showStickers = !a.showStickers
				return saveSetting(a.messages.SetShowStickers, a.showStickers)
			},
		},
	}
}

type settingsSavedMsg struct{ err error }

// saveSetting wraps a store write as a command, so a slow disk cannot stall
// the render loop -- the same reason every other write in this app is a
// tea.Cmd rather than a direct call.
func saveSetting(set func(bool) error, value bool) tea.Cmd {
	return func() tea.Msg {
		return settingsSavedMsg{err: set(value)}
	}
}

// SettingsPage is a small, centred list of on/off preferences.
//
// It holds whichever page opened it rather than assuming that page is always
// ConversationsPage: esc hands back exactly that value, cursor, scroll, open
// chat and all, with nothing to reload and nothing to rebuild.
type SettingsPage struct {
	Page
	app      *app
	previous PageInterface

	cursor int
	status string
	failed bool
}

func openSettingsPage(a *app, previous PageInterface) SettingsPage {
	return SettingsPage{
		Page:     Page{pageTitle: "Settings"},
		app:      a,
		previous: previous,
	}
}

// render and action shadow the ones promoted from the embedded Page. That is
// required, not stylistic: Page.action returns a Page, so inheriting it would
// replace this screen with a blank one on the first keypress.
func (s SettingsPage) render() string {
	rows := settingsRows(s.app)

	lines := []string{
		accentStyle.Bold(true).Render("Settings"),
		mutedStyle.Render("A picture turned off draws as its chip instead, and decodes nothing."),
		"",
	}

	// The label column is one fixed width so every on/off lines up, whichever
	// row is longest.
	width := 0
	for _, row := range rows {
		width = max(width, len(row.label))
	}

	for i, row := range rows {
		marker := "  "
		style := titleStyle
		if i == s.cursor {
			marker = accentStyle.Render("▌ ")
			style = nameStyle
		}
		state := mutedStyle.Render("off")
		if row.get() {
			state = accentStyle.Bold(true).Render("on")
		}
		label := fmt.Sprintf("%-*s", width, row.label)
		lines = append(lines, marker+style.Render(label)+"  "+state)
	}

	lines = append(lines, "")
	if s.status != "" {
		style := mutedStyle
		if s.failed {
			style = warnStyle
		}
		lines = append(lines, style.Render(s.status), "")
	}
	lines = append(lines, mutedStyle.Render("↑↓ move · enter toggle · esc back"))

	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colourBorder).
		Padding(1, 3).
		Render(strings.Join(lines, "\n"))

	return lipgloss.Place(
		max(s.app.width, 20), max(s.app.height, 10),
		lipgloss.Center, lipgloss.Center,
		card,
	)
}

func (s SettingsPage) action(event tea.Msg) (PageInterface, tea.Cmd) {
	switch msg := event.(type) {
	case settingsSavedMsg:
		if msg.err != nil {
			s.status, s.failed = "could not save: "+msg.err.Error(), true
		}
		return s, nil

	case tea.KeyPressMsg:
		rows := settingsRows(s.app)
		switch msg.String() {
		case "esc":
			return s.previous, nil

		case "up":
			s.cursor = max(s.cursor-1, 0)
			return s, nil

		case "down":
			s.cursor = min(s.cursor+1, len(rows)-1)
			return s, nil

		case "enter", " ":
			s.status, s.failed = "", false
			return s, rows[s.cursor].toggle()
		}
	}
	return s, nil
}
