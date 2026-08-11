package ui

import (
	"fmt"
	"reflect"
	"strconv"

	tea "charm.land/bubbletea/v2"
)


type viewPort struct {

	listOfNumbers []int

}


func initializeModel() viewPort {
	var m viewPort
	m.listOfNumbers = []int{1, 4, 3, 6}
	return m
}

func (m viewPort) Init() tea.Cmd {
	return nil
}

func (m viewPort) View() tea.View {
	s := "\n"

	for idx, val := range m.listOfNumbers {
		if val > 3 {
			s += fmt.Sprintf("%s -> bigger %s\n", strconv.Itoa(idx), strconv.Itoa(val))
		} else {
			s += fmt.Sprintf("%s <- lesser %s\n", strconv.Itoa(idx), strconv.Itoa(val))
		}
	}

	view := tea.NewView(s)
	view.AltScreen = true
	return view
}

func (m viewPort) Update(event tea.Msg) (tea.Model, tea.Cmd) {

	if event, ok := event.(tea.KeyPressMsg); ok {
		fmt.Println(event)
		fmt.Println(reflect.TypeOf(event))

		if event.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func StartUI() {
	p := tea.NewProgram(initializeModel())

	if _, err := p.Run(); err != nil {
		panic(err)
	}
}
