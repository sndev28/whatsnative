package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// typeText feeds a string through the input one keypress at a time.
func typeText(input textInput, text string) textInput {
	for _, r := range text {
		input = input.update(tea.KeyPressMsg{Text: string(r), Code: r})
	}
	return input
}

func press(input textInput, code rune) textInput {
	return input.update(tea.KeyPressMsg{Code: code})
}

func TestTypingAndBackspace(t *testing.T) {
	input := typeText(textInput{}, "hello")

	if got := input.string(); got != "hello" {
		t.Fatalf("got %q, want %q", got, "hello")
	}

	input = press(input, tea.KeyBackspace)
	if got := input.string(); got != "hell" {
		t.Errorf("after backspace got %q, want %q", got, "hell")
	}
}

// The cursor counts characters, not bytes: one backspace must remove a whole
// emoji rather than half of its encoding.
func TestBackspaceDeletesWholeRune(t *testing.T) {
	input := typeText(textInput{}, "hi 🍡")

	input = press(input, tea.KeyBackspace)
	if got := input.string(); got != "hi " {
		t.Errorf("got %q, want %q", got, "hi ")
	}
}

func TestInsertAtCursor(t *testing.T) {
	input := typeText(textInput{}, "helo")
	input = press(input, tea.KeyLeft)
	input = typeText(input, "l")

	if got := input.string(); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

// Editing one copy must not disturb another: textInput is passed by value, so
// the slices behind it cannot be shared.
func TestCopiesDoNotShareBacking(t *testing.T) {
	original := typeText(textInput{}, "hello")

	edited := press(original, tea.KeyBackspace)
	edited = typeText(edited, "!")

	if got := original.string(); got != "hello" {
		t.Errorf("original changed to %q, want %q", got, "hello")
	}
	if got := edited.string(); got != "hell!" {
		t.Errorf("edited is %q, want %q", got, "hell!")
	}
}

func TestClearAndEmpty(t *testing.T) {
	input := typeText(textInput{}, "something")
	if input.empty() {
		t.Error("input with text reports empty")
	}

	if cleared := input.clear(); !cleared.empty() || cleared.string() != "" {
		t.Errorf("cleared input is %q, want empty", cleared.string())
	}
}

func TestCursorMovementBounds(t *testing.T) {
	input := typeText(textInput{}, "ab")

	for range 5 {
		input = press(input, tea.KeyLeft)
	}
	input = typeText(input, "X")
	if got := input.string(); got != "Xab" {
		t.Errorf("got %q, want %q -- cursor ran past the start", got, "Xab")
	}

	for range 10 {
		input = press(input, tea.KeyRight)
	}
	input = typeText(input, "Z")
	if got := input.string(); got != "XabZ" {
		t.Errorf("got %q, want %q -- cursor ran past the end", got, "XabZ")
	}
}
