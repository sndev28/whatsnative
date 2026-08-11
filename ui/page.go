package ui

import (
	"fmt"
	"reflect"

	tea "charm.land/bubbletea/v2"
)

type page struct {
	pageName string
	content string
}

type pageInterface interface {
	render() string
	action(tea.Msg) (pageInterface, tea.Cmd)
}

func newPage() page {
	return page{
		pageName: "Start",
		content: "Empty page",
	}
}

func (p page) render() (string) {
	return p.content;
}

func (p page) action(event tea.Msg) (pageInterface, tea.Cmd) {
	fmt.Printf("\r\nevent: %s", reflect.TypeOf(event))

	if event, ok := event.(tea.KeyPressMsg); ok {
		if event.String() == "ctrl+c" {
			return p, tea.Quit
		}
	}
	return p, nil
}
