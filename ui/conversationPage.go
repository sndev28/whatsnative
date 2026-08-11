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
	cursor int
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
		cursor: 0,
	}
}

func (c ConversationsPage) render() string {
	s := strings.Builder{}

	for _, conversation := range c.conversations {
		if c.cursor == conversation.id {
			fmt.Fprintf(&s, "> %s | %s \n", conversation.profilePic, conversation.name)
		} else {
			fmt.Fprintf(&s, "  %s | %s \n", conversation.profilePic, conversation.name)
		}
		fmt.Fprintf(&s, "  %s\n  ", conversation.lastMessage)
		s.WriteString(strings.Repeat("_", 20))
		s.WriteByte('\n')
	}

	return s.String();
}

func (c ConversationsPage) action(event tea.Msg) (PageInterface, tea.Cmd) {

	if event, ok := event.(tea.KeyPressMsg); ok {
		switch event.String() {
			case "ctrl+c":
				return c, tea.Quit
			case "down":
				if c.cursor < len(c.conversations) - 1 {c.cursor += 1}
			case "up": 
				if c.cursor > 0 {c.cursor -= 1}
			default:
				return newPage(), nil
		}
	}

	return c, nil
}