package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// textInput is a single-line editor.
//
// charmbracelet/bubbles (which has a textinput) still targets Bubble Tea v1,
// so this is a small hand-rolled replacement. It stores runes rather than a
// string so that cursor positions are character positions, not byte offsets --
// otherwise a single emoji would take several presses to delete.
type textInput struct {
	value  []rune
	cursor int
}

// update applies a keypress and returns the new state. Every edit builds a
// fresh slice instead of reslicing in place: textInput is copied by value, and
// two copies sharing one backing array would corrupt each other.
func (t textInput) update(key tea.KeyPressMsg) textInput {
	switch key.String() {
	case "backspace":
		if t.cursor > 0 {
			t.value = removeRune(t.value, t.cursor-1)
			t.cursor--
		}
	case "delete":
		if t.cursor < len(t.value) {
			t.value = removeRune(t.value, t.cursor)
		}
	case "left":
		if t.cursor > 0 {
			t.cursor--
		}
	case "right":
		if t.cursor < len(t.value) {
			t.cursor++
		}
	case "home", "ctrl+a":
		t.cursor = 0
	case "end", "ctrl+e":
		t.cursor = len(t.value)
	case "ctrl+u":
		t.value, t.cursor = nil, 0
	case "ctrl+w":
		t.value, t.cursor = removeWordBefore(t.value, t.cursor)
	default:
		// Text is non-empty only for keys that produce printable characters,
		// which conveniently excludes every shortcut handled above.
		if key.Text != "" {
			t.value = insertRunes(t.value, t.cursor, []rune(key.Text))
			t.cursor += len([]rune(key.Text))
		}
	}
	return t
}

func (t textInput) string() string { return string(t.value) }

func (t textInput) empty() bool { return len(t.value) == 0 }

func (t textInput) clear() textInput { return textInput{} }

// render draws the value with a block cursor, scrolled so the cursor stays
// visible when the text is longer than the available width.
func (t textInput) render(width int) string {
	if width <= 0 {
		return ""
	}

	// Reserve one cell for the cursor sitting past the last character.
	visible := t.value
	start := 0
	for layoutWidth(string(visible[start:])) >= width && start < t.cursor {
		start++
	}
	visible = visible[start:]

	cursorAt := t.cursor - start
	var out strings.Builder
	out.WriteString(string(visible[:cursorAt]))
	if cursorAt < len(visible) {
		out.WriteString(reverse + string(visible[cursorAt]) + reset)
		out.WriteString(string(visible[cursorAt+1:]))
	} else {
		out.WriteString(reverse + " " + reset)
	}
	return out.String()
}

func insertRunes(value []rune, at int, extra []rune) []rune {
	next := make([]rune, 0, len(value)+len(extra))
	next = append(next, value[:at]...)
	next = append(next, extra...)
	next = append(next, value[at:]...)
	return next
}

func removeRune(value []rune, at int) []rune {
	next := make([]rune, 0, len(value)-1)
	next = append(next, value[:at]...)
	next = append(next, value[at+1:]...)
	return next
}

// removeWordBefore deletes the whitespace-delimited word ending at cursor.
func removeWordBefore(value []rune, cursor int) ([]rune, int) {
	end := cursor
	for end > 0 && value[end-1] == ' ' {
		end--
	}
	for end > 0 && value[end-1] != ' ' {
		end--
	}

	next := make([]rune, 0, len(value)-(cursor-end))
	next = append(next, value[:end]...)
	next = append(next, value[cursor:]...)
	return next, end
}
