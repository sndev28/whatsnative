package ui

import (
	"fmt"
	neturl "net/url"
	"os/exec"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"

	"whatsnative/db"
)

// linkPattern finds a web address in message text.
//
// Only http and https are matched. WhatsApp text is written by other people,
// and handing an arbitrary scheme to xdg-open lets a message choose which
// program runs -- so the set of things that can be opened is kept to the web.
var linkPattern = regexp.MustCompile(`(?i)\b(?:https?://|www\.)[^\s<>"'` + "`" + `]+`)

// trailingJunk is punctuation that ends a sentence rather than a URL.
const trailingJunk = `.,;:!?)]}'"`

// firstLink returns the first web address in a message, ready to open.
func firstLink(text string) (string, bool) {
	match := linkPattern.FindString(text)
	if match == "" {
		return "", false
	}

	// "look at https://example.com." ends a sentence, not a path. Closing
	// brackets only come off when they are unmatched, so a Wikipedia URL with
	// real parentheses in it survives.
	for len(match) > 0 && strings.ContainsRune(trailingJunk, rune(match[len(match)-1])) {
		last := match[len(match)-1]
		if last == ')' && strings.Count(match, "(") >= strings.Count(match, ")") {
			break
		}
		match = match[:len(match)-1]
	}
	if match == "" {
		return "", false
	}

	// A bare www.example.com has no scheme, and xdg-open needs one.
	if !strings.Contains(match, "://") {
		match = "https://" + match
	}

	// Parse is only the check. The match itself is what gets returned, because
	// re-serialising a URL rewrites it -- percent-encoding brackets and the
	// like -- and the address someone typed is the one they meant.
	parsed, err := neturl.Parse(match)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return match, true
	}
	return "", false
}

// openLink hands a web address to the browser.
func openLink(url string) tea.Cmd {
	return func() tea.Msg {
		// Re-checked here rather than trusted from the caller: this is the last
		// point before a string from someone else's message becomes an argument
		// to a program.
		if _, ok := firstLink(url); !ok {
			return mediaOpenedMsg{err: fmt.Errorf("not a web address: %s", url)}
		}
		return mediaOpenedMsg{err: openExternally(url)}
	}
}

// copyableText is what ctrl+y puts on the clipboard for a message.
//
// The stored text, never the rendered text: what is on screen has been folded
// down to ASCII for the sake of the column maths, and pasting a row of question
// marks would be worse than useless.
func copyableText(message db.Message) string {
	switch {
	case message.Revoked:
		return ""
	case message.Content != "":
		return message.Content
	case message.IsPoll():
		return strings.Join(append([]string{message.Poll.Question}, message.Poll.Options...), "\n")
	case message.Media.Path != "":
		// Nothing to quote, but the file on disk is worth having.
		return message.Media.Path
	}
	return ""
}

// clipboardTools are the native helpers, best first. Each takes the text on
// standard input.
var clipboardTools = [][]string{
	{"wl-copy"},
	{"xclip", "-selection", "clipboard"},
	{"xsel", "--clipboard", "--input"},
	{"pbcopy"},
}

// copyText puts text on the system clipboard.
//
// A native helper is tried first because it always works locally. OSC52 is the
// fallback: it reaches the clipboard through the terminal itself, which is the
// only thing that works over SSH, but a terminal is free to ignore it and some
// do by default.
func copyText(text string) tea.Cmd {
	for _, tool := range clipboardTools {
		path, err := exec.LookPath(tool[0])
		if err != nil {
			continue
		}
		return func() tea.Msg {
			command := exec.Command(path, tool[1:]...)
			command.Stdin = strings.NewReader(text)
			if err := command.Run(); err != nil {
				return copiedMsg{err: fmt.Errorf("%s: %w", tool[0], err)}
			}
			return copiedMsg{}
		}
	}
	return tea.Batch(tea.SetClipboard(text), func() tea.Msg { return copiedMsg{} })
}
