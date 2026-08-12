package ui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

type User struct {
	id int
	name string
	profilePic string
	activeUser bool
}

type Message struct {
	id int
	sender User
	time time.Time
	content string
}

type Conversation struct {
	id int
	user User
	messages []Message
}

type ConversationsPage struct {
	Page
	conversations []Conversation
	cursor int
}

func getConversations() []Conversation {

	users := []User{
		{
			id:         0,
			name:       "Hououin Kyoume",
			profilePic: "🥼",
			activeUser: true,
		},
		{
			id:         1,
			name:       "Daru",
			profilePic: "🍯",
			activeUser: false,
		},
		{
			id:         2,
			name:       "Christina",
			profilePic: "🕶️",
			activeUser: false,
		},
		{
			id:         3,
			name:       "Mayuri",
			profilePic: "🍗",
			activeUser: false,
		},
	}

	return []Conversation{
		{
			id:   0,
			user: users[1],
			messages: []Message{
				{
					id:      0,
					sender:  users[0],
					content: "El Psy Kongroo",
					time:    time.Date(2026, time.August, 12, 14, 30, 0, 0, time.UTC),
				},
				{
					id:      1,
					sender:  users[1],
					content: "lol ok Okarin, did you finish the IBN 5100 emulator or nah",
					time:    time.Date(2026, time.August, 12, 14, 31, 0, 0, time.UTC),
				},
				{
					id:      2,
					sender:  users[0],
					content: "Silence, Super Hacka. The Organization is listening.",
					time:    time.Date(2026, time.August, 12, 14, 32, 0, 0, time.UTC),
				},
				{
					id:      3,
					sender:  users[1],
					content: "bro it's just Whatsapp",
					time:    time.Date(2026, time.August, 12, 14, 33, 0, 0, time.UTC),
				},
			},
		},
		{
			id:   1,
			user: users[2],
			messages: []Message{
				{
					id:      4,
					sender:  users[2],
					content: "Don't call me Christina.",
					time:    time.Date(2026, time.August, 12, 9, 0, 0, 0, time.UTC),
				},
				{
					id:      5,
					sender:  users[0],
					content: "Christina, I require your assistance with a time-machine-related anomaly.",
					time:    time.Date(2026, time.August, 12, 9, 1, 0, 0, time.UTC),
				},
				{
					id:      6,
					sender:  users[2],
					content: "It's a microwave. It sends bananas to the past. That's it.",
					time:    time.Date(2026, time.August, 12, 9, 2, 0, 0, time.UTC),
				},
				{
					id:      7,
					sender:  users[0],
					content: "That's exactly what a time traveler from the future would say.",
					time:    time.Date(2026, time.August, 12, 9, 3, 0, 0, time.UTC),
				},
				{
					id:      8,
					sender:  users[2],
					content: "I will end you.",
					time:    time.Date(2026, time.August, 12, 9, 4, 0, 0, time.UTC),
				},
			},
		},
		{
			id:   2,
			user: users[3],
			messages: []Message{
				{
					id:      9,
					sender:  users[3],
					content: "Okarin~ tuturu! 🍡",
					time:    time.Date(2026, time.August, 12, 8, 0, 0, 0, time.UTC),
				},
				{
					id:      10,
					sender:  users[0],
					content: "Mayushii. The convergence remains stable. For now.",
					time:    time.Date(2026, time.August, 12, 8, 1, 0, 0, time.UTC),
				},
				{
					id:      11,
					sender:  users[3],
					content: "does that mean you're coming to help with the costume today",
					time:    time.Date(2026, time.August, 12, 8, 2, 0, 0, time.UTC),
				},
				{
					id:      12,
					sender:  users[0],
					content: "...Yes. I will be there.",
					time:    time.Date(2026, time.August, 12, 8, 3, 0, 0, time.UTC),
				},
			},
		},
	}
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
			fmt.Fprintf(&s, "> %s | %s \n", conversation.user.profilePic, conversation.user.name)
		} else {
			fmt.Fprintf(&s, "  %s | %s \n", conversation.user.profilePic, conversation.user.name)
		}
		fmt.Fprintf(&s, "  %s\n  ", conversation.messages[len(conversation.messages)-1].content)
		s.WriteString(strings.Repeat("_", 20))
		s.WriteByte('\n')
	}


	conversation := c.conversations[c.cursor]
	
	fmt.Fprintf(&s, "%s | %s \n", conversation.user.profilePic, conversation.user.name)
	s.WriteString(strings.Repeat("*", 20))
	s.WriteByte('\n')

	for _, message := range conversation.messages {
		if message.sender.activeUser {
			fmt.Fprintf(&s, "> %s \n", message.sender.name)
		} else {
			fmt.Fprintf(&s, "< %s \n", message.sender.name)
		}
		fmt.Fprintf(&s, "  %s | %s \n", message.content, message.time)
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