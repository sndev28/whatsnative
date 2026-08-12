package ui

import (
	"strings"
	"github.com/mdp/qrterminal/v3"

	tea "charm.land/bubbletea/v2"
)


type LoginPage struct {
	Page
	loginQR string
}


func openLoginPage() LoginPage {
	return LoginPage{
		loginQR: "El Psy Kongroo!", //placeholder
	}
}



func (l LoginPage) render() string {
	s := strings.Builder{}
	qrterminal.GenerateHalfBlock(l.loginQR, qrterminal.L, &s)

	return s.String();
}

func (l LoginPage) action(event tea.Msg) (PageInterface, tea.Cmd) {

	if event, ok := event.(tea.KeyPressMsg); ok {
		switch event.String() {
			case "ctrl+c":
				return l, tea.Quit
			default:
				return newPage(), nil
		}
	}

	return l, nil
}