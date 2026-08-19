package ui

import (
	tea "charm.land/bubbletea/v2"
)

type Page struct {
	pageTitle string
	content   string
}

type PageInterface interface {
	render() string
	action(tea.Msg) (PageInterface, tea.Cmd)
}

func newPage() Page {
	return Page{
		pageTitle: "Start",
		content:   "Empty page",
	}
}

func (p Page) render() string {
	return p.content
}

func (p Page) action(event tea.Msg) (PageInterface, tea.Cmd) {
	// fmt.Printf("\r\nevent: %s", reflect.TypeOf(event))

	if event, ok := event.(tea.KeyPressMsg); ok {
		if event.String() == "ctrl+c" {
			return p, tea.Quit
		}
	}
	return p, nil
}
