package ui

import (
	"strings"

	"github.com/mdp/qrterminal/v3"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"whatsnative/client"
)

// LoginPage shows the QR code that links this terminal to a WhatsApp account.
//
// It holds the rendered code as a plain string rather than the channel it came
// from. Reading the channel here would mean blocking inside render, which runs
// on the render loop: the UI would freeze until a code arrived, and every
// redraw would swallow one. The session reads the channel instead and sends
// each code in as a client.QRCode message.
type LoginPage struct {
	Page
	app    *app
	qr     string
	status string
	failed bool
}

func openLoginPage(a *app) LoginPage {
	return LoginPage{
		Page:   Page{pageTitle: "Login"},
		app:    a,
		status: "Connecting to WhatsApp…",
	}
}

func displayQRCode(code string, s *strings.Builder) {
	// Half blocks keep the code square: terminal cells are twice as tall as
	// they are wide, so one character per module would come out stretched.
	qrterminal.GenerateHalfBlock(code, qrterminal.L, s)
}

// render and action shadow the ones promoted from the embedded Page. That is
// required, not stylistic: Page.action returns a Page, so inheriting it would
// replace this screen with a blank one on the first keypress.
func (l LoginPage) render() string {
	status := mutedStyle.Render(l.status)
	if l.failed {
		status = warnStyle.Render(l.status)
	}

	body := []string{
		accentStyle.Bold(true).Render("whatsnative"),
		mutedStyle.Render("Link this terminal to your WhatsApp account"),
		"",
	}
	if l.qr != "" {
		body = append(body, strings.TrimRight(l.qr, "\n"), "")
	}
	body = append(body, status)

	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colourBorder).
		Padding(1, 3).
		Align(lipgloss.Center).
		Render(strings.Join(body, "\n"))

	// Centre the card in the terminal, with the hint underneath.
	return lipgloss.Place(
		max(l.app.width, 20), max(l.app.height, 10),
		lipgloss.Center, lipgloss.Center,
		card+"\n"+mutedStyle.Render("ctrl+c  quit"),
	)
}

func (l LoginPage) action(event tea.Msg) (PageInterface, tea.Cmd) {
	switch msg := event.(type) {
	case client.QRCode:
		s := strings.Builder{}
		displayQRCode(msg.Code, &s)
		l.qr = s.String()
		l.status = "On your phone: WhatsApp › Settings › Linked devices › Link a device"
		l.failed = false
		return l, nil

	case client.Connected:
		// Paired and online: hand over to the conversations and load them.
		return openConversationsPage(l.app), loadChats(l.app)

	case client.Failure:
		l.status = "error: " + msg.Err.Error()
		l.failed = true
		return l, nil
	}

	return l, nil
}
