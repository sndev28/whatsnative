package ui

import (
	tea "charm.land/bubbletea/v2"
)



type viewPort struct {
	page PageInterface
}


func (v viewPort) Init() tea.Cmd {
	return nil
}

func (v viewPort) View() tea.View {
	view := tea.NewView(v.page.render())
	view.AltScreen = true
	return view
}

func (v viewPort) Update(event tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	v.page, cmd = v.page.action(event)
	return v, cmd
}

func initializeViewPort() viewPort {
	v := viewPort{page: newPage()}
	return v
}

func StartUI() {
	p := tea.NewProgram(initializeViewPort())

	if _, err := p.Run(); err != nil {
		panic(err)
	}
}
