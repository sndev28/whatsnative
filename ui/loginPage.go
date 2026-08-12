package ui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"	

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"

	tea "charm.land/bubbletea/v2"
)


type LoginPage struct {
	Page
	qrChan <-chan whatsmeow.QRChannelItem
}


func openLoginPage(client *whatsmeow.Client) LoginPage {

	if client.Store.ID == nil {
		// No ID stored, new login
		qrChan, err := client.GetQRChannel(context.Background())

		if err != nil {
			slog.Error(err.Error())
			panic(err)
		}
		
		err = client.Connect()
		if err != nil {
			slog.Error(err.Error())
			panic(err)
		}

		return LoginPage{
			qrChan: qrChan,
		}

	} else {
		// Already logged in, just connect
		return LoginPage{
			qrChan: nil,
		}
	}
}

func displayQRCode(code string, s *strings.Builder) {
	qrterminal.GenerateHalfBlock(code, qrterminal.L, s)
}

func displayTimeout(s *strings.Builder) {
	fmt.Fprint(s, "Log in attempt has timed out!")
}



func (l LoginPage) render() string {
	s := strings.Builder{}

	if l.qrChan != nil {
		evt := <-l.qrChan 

		switch evt.Event {
			case "code":
				displayQRCode(evt.Code, &s)
			case "timeout":
				displayTimeout(&s)
		}
	} else {
		fmt.Fprint(&s, "Logged in!")
	}

	

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