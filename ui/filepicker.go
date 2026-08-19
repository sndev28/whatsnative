package ui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Dropping a file onto a terminal only works if the terminal turns the drop
// into a paste, and not all of them do. This is the way that always works:
// browse to the file from inside the app.
type browseEntry struct {
	name  string
	isDir bool
}

// openBrowser starts the picker in the last directory used, or home.
func (c ConversationsPage) openBrowser() (PageInterface, tea.Cmd) {
	dir := c.browseDir
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = home
		} else {
			dir = "."
		}
	}

	c.browsing = true
	c.browseCursor = 0
	c.browseDir = dir
	c.browseEntries = readDirectory(dir)
	c.note("Pick a file to send · enter opens · esc cancels")
	return c, nil
}

// readDirectory lists a directory with folders first, skipping hidden entries
// and anything unreadable.
func readDirectory(dir string) []browseEntry {
	items, err := os.ReadDir(dir)
	if err != nil {
		return []browseEntry{{name: "..", isDir: true}}
	}

	entries := []browseEntry{{name: "..", isDir: true}}
	for _, item := range items {
		if strings.HasPrefix(item.Name(), ".") {
			continue
		}
		entries = append(entries, browseEntry{name: item.Name(), isDir: item.IsDir()})
	}

	// Folders first, then files, each alphabetically -- but keep ".." on top.
	rest := entries[1:]
	sort.SliceStable(rest, func(i, j int) bool {
		if rest[i].isDir != rest[j].isDir {
			return rest[i].isDir
		}
		return strings.ToLower(rest[i].name) < strings.ToLower(rest[j].name)
	})
	return entries
}

// browseLines draws the picker in the rail, where the chat list normally is.
func (c ConversationsPage) browseLines(l layout) []string {
	width := l.railInner

	lines := []string{
		cell(" "+accentStyle.Bold(true).Render("send a file"), width),
		cell(" "+mutedStyle.Render(truncate(plain(shortenPath(c.browseDir)), width-2)), width),
		rule(width),
	}

	rows := max(l.contentRows-len(lines), 1)
	first := 0
	if c.browseCursor >= rows {
		first = c.browseCursor - rows + 1
	}

	for i := first; i < len(c.browseEntries) && len(lines) < l.contentRows; i++ {
		entry := c.browseEntries[i]

		name := plain(entry.name)
		if entry.isDir {
			name += "/"
		}

		marker, style := "  ", mutedStyle
		if entry.isDir {
			style = peerStyle
		}
		if i == c.browseCursor {
			marker, style = accentStyle.Render("▌ "), nameStyle
		}
		lines = append(lines, cell(marker+style.Render(truncate(name, max(width-3, 4))), width))
	}

	return append(lines, blanks(l.contentRows-len(lines), width)...)
}

// shortenPath replaces the home directory with ~ so the header fits.
func shortenPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if trimmed, found := strings.CutPrefix(path, home); found {
		return "~" + trimmed
	}
	return path
}

// handleBrowseKey drives the picker: arrows move, enter descends into a folder
// or attaches a file, backspace goes up, escape gives up.
func (c ConversationsPage) handleBrowseKey(key tea.KeyPressMsg) (PageInterface, tea.Cmd) {
	switch key.String() {
	case "esc", "ctrl+u":
		c.browsing = false
		c.note("")
		return c, nil

	case "up":
		c.browseCursor = max(c.browseCursor-1, 0)
		return c, nil
	case "down":
		c.browseCursor = min(c.browseCursor+1, max(len(c.browseEntries)-1, 0))
		return c, nil

	case "backspace", "left":
		return c.enterDirectory(filepath.Dir(c.browseDir))

	case "enter", "right":
		if c.browseCursor < 0 || c.browseCursor >= len(c.browseEntries) {
			return c, nil
		}
		entry := c.browseEntries[c.browseCursor]
		target := filepath.Join(c.browseDir, entry.name)

		if entry.isDir {
			return c.enterDirectory(filepath.Clean(target))
		}

		c.browsing = false
		c.pending = target
		c.note("Attached " + filepath.Base(target) + " — enter to send, esc to drop it")
		return c, nil
	}
	return c, nil
}

func (c ConversationsPage) enterDirectory(dir string) (PageInterface, tea.Cmd) {
	c.browseDir = dir
	c.browseEntries = readDirectory(dir)
	c.browseCursor = 0
	return c, nil
}
