package ui

import (
	"fmt"
	"strings"
	
	tea "charm.land/bubbletea/v2"

)

type Conversation struct {
	id int
	name string
	lastMessage string
	profilePic string
}

type ConversationsPage struct {
	Page
	conversations []Conversation
}

func getConversations() []Conversation {
	return []Conversation{{
		id: 0,
		name: "John Titor",
		lastMessage: "I am from 2001...ig?",
		profilePic: "Barrel",
	},{
		id: 1,
		name: "Mayuri",
		lastMessage: "El Psy Kongree!",
		profilePic: "Tuturuuu",
	},{
		id: 2,
		name: "Christina",
		lastMessage: "My names not Christinaa",
		profilePic: "<3",
	}}
}

func openConversationsPage() ConversationsPage {
	return ConversationsPage{
		conversations: getConversations(),
	}
}

func (c ConversationsPage) render() string {
	s := ""

	for _, conversation := range c.conversations {
		s += fmt.Sprintf("%s | %s \n", conversation.profilePic, conversation.name)
		s += fmt.Sprintf("%s\n", conversation.lastMessage)
		s += strings.Repeat("_", 20)
		s += "\n"
	}

	return s;
}

func (c ConversationsPage) action(event tea.Msg) (PageInterface, tea.Cmd) {

	if event, ok := event.(tea.KeyPressMsg); ok {
		if event.String() == "ctrl+c" {
			return c, tea.Quit
		} else if event.String() == "enter" {
			return newPage(), nil
		}
	}

	return c, nil
}