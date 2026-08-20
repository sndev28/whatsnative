package ui

import (
	tea "charm.land/bubbletea/v2"

	"whatsnative/client"
	"whatsnative/db"
)

// app is the state every page shares.
//
// Pages are values that get copied on every update, so they hold a pointer to
// this: the terminal size and the connection have to be the same whichever
// page happens to be on screen.
type app struct {
	session  *client.Session
	messages *db.MessageStore

	width  int
	height int
}

type viewPort struct {
	page PageInterface
	app  *app
}

func (v viewPort) Init() tea.Cmd {
	// The emoji history is read once here; after that the picker refreshes
	// itself each time a reaction goes out.
	return tea.Batch(loadChats(v.app), loadPalette(v.app))
}

func (v viewPort) View() tea.View {
	view := tea.NewView(v.page.render())
	view.AltScreen = true
	// Name the window ourselves rather than leaving it as whatever the
	// terminal called itself. This reaches any terminal, however it was
	// launched, and it is what the title bar and window list read.
	view.WindowTitle = "WhatsApp"
	// Cell motion reports clicks, releases and the wheel, which is everything
	// the conversation page needs and nothing more; all-motion would flood the
	// update loop with a message per pixel of cursor movement.
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

func (v viewPort) Update(event tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := event.(type) {
	case tea.WindowSizeMsg:
		// app is shared by pointer, so every page sees the new size.
		v.app.width = msg.Width
		v.app.height = msg.Height

	case tea.KeyPressMsg:
		// Quitting is the same everywhere, so it lives here rather than in
		// every page's key handler.
		if msg.String() == "ctrl+c" {
			return v, tea.Quit
		}
	}

	var cmd tea.Cmd
	v.page, cmd = v.page.action(event)
	return v, cmd
}

func initializeViewPort(a *app) viewPort {
	// A stored login goes straight to the conversations; otherwise we need to
	// pair first.
	if a.session.HasSession() {
		return viewPort{page: openConversationsPage(a), app: a}
	}
	return viewPort{page: openLoginPage(a), app: a}
}

func StartUI(session *client.Session, messages *db.MessageStore) {
	a := &app{
		session:  session,
		messages: messages,
		// Sensible defaults until the first WindowSizeMsg arrives.
		width:  80,
		height: 24,
	}

	// Work out which ruler Bubble Tea is going to measure with, while the
	// ordinary screen is still ours to ask questions on.
	detectWidthMode()

	p := tea.NewProgram(initializeViewPort(a))

	// whatsmeow delivers events on its own goroutines, and Program.Send is the
	// only safe way into the update loop. One goroutine does nothing but
	// forward them; it ends when the session closes its channel.
	go func() {
		for event := range session.Events() {
			p.Send(event)
		}
	}()

	if _, err := p.Run(); err != nil {
		panic(err)
	}
}
