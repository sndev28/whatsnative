package ui

import (
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// Bubble Tea does not print our rows verbatim. It parses them into a grid of
// cells, measuring each grapheme with one of two rulers, and then clips or
// pads every row to that measurement. A row padded with a different ruler is
// therefore either cut short or left with a gap -- which is exactly how the
// right border ends up a few columns off.
//
// Which ruler it uses is decided by the terminal. Bubble Tea asks whether mode
// 2027 (Unicode core, grapheme clustering) is recognised: if so it switches to
// grapheme widths and switches the mode on, and if not it stays on wcwidth. So
// rather than trying to measure what the terminal really draws, we ask the
// same question first and then measure exactly the way Bubble Tea will.
type widthMode int32

const (
	// widthWc is Bubble Tea's default: one cell per rune, wide characters
	// counted double, combining marks free.
	widthWc widthMode = iota
	// widthGrapheme is one cell per grapheme cluster.
	widthGrapheme
)

// activeWidth is read on every measurement and written once at startup.
var activeWidth atomic.Int32

// probeBudget bounds the question: a terminal that never answers must not hold
// the app hostage at startup.
const probeBudget = 250 * time.Millisecond

// measure returns the columns a string occupies, in the same units Bubble Tea
// is using to lay out the screen.
func measure(s string) int {
	if widthMode(activeWidth.Load()) == widthGrapheme {
		return ansi.StringWidth(s)
	}
	return ansi.StringWidthWc(s)
}

// detectWidthMode works out which ruler Bubble Tea will use, before it starts.
//
// WHATSNATIVE_WIDTH=grapheme or =wc forces the answer, which is the escape
// hatch if a terminal reports one thing and draws another.
func detectWidthMode() {
	switch strings.ToLower(os.Getenv("WHATSNATIVE_WIDTH")) {
	case "grapheme", "cluster":
		activeWidth.Store(int32(widthGrapheme))
		return
	case "wc", "wcwidth", "rune":
		activeWidth.Store(int32(widthWc))
		return
	}

	// No answer means the terminal did not recognise the mode, which is also
	// what makes Bubble Tea keep its wcwidth default.
	if supportsUnicodeCore() {
		activeWidth.Store(int32(widthGrapheme))
		return
	}
	activeWidth.Store(int32(widthWc))
}

// supportsUnicodeCore asks the terminal whether it knows mode 2027, the same
// request Bubble Tea makes when it starts.
func supportsUnicodeCore() bool {
	input, output := os.Stdin, os.Stdout
	if !term.IsTerminal(input.Fd()) || !term.IsTerminal(output.Fd()) {
		return false
	}

	state, err := term.MakeRaw(input.Fd())
	if err != nil {
		return false
	}
	defer term.Restore(input.Fd(), state)

	if _, err := io.WriteString(output, "\x1b[?2027$p"); err != nil {
		return false
	}

	value, err := readModeReport(int(input.Fd()), time.Now().Add(probeBudget))
	if err != nil {
		return false
	}

	// 0 is "not recognised" and 4 is "permanently reset"; Bubble Tea only
	// switches rulers for set, reset and permanently set.
	switch value {
	case 1, 2, 3:
		return true
	default:
		return false
	}
}

// readModeReport parses a DEC report, "ESC [ ? <mode> ; <value> $ y", and
// returns the value.
//
// Bytes are read straight from the descriptor rather than through os.Stdin: Go
// cannot set a read deadline on a terminal, and a deadline is the only thing
// between us and hanging forever on a terminal that stays silent. A blocked
// read would be worse than useless -- it would still hold stdin once Bubble
// Tea started, and swallow the user's keys.
func readModeReport(fd int, deadline time.Time) (int, error) {
	// Find the start of the report.
	for {
		b, err := readByteBefore(fd, deadline)
		if err != nil {
			return 0, err
		}
		if b == 0x1b {
			break
		}
	}

	if b, err := readByteBefore(fd, deadline); err != nil || b != '[' {
		return 0, errors.New("malformed mode report")
	}
	if b, err := readByteBefore(fd, deadline); err != nil || b != '?' {
		return 0, errors.New("malformed mode report")
	}

	var mode, value []byte
	target := &mode
	for {
		b, err := readByteBefore(fd, deadline)
		if err != nil {
			return 0, err
		}
		switch {
		case b == ';':
			target = &value
		case b == '$':
			// The report ends "$y"; consume the y and report the value.
			if final, err := readByteBefore(fd, deadline); err != nil || final != 'y' {
				return 0, errors.New("malformed mode report")
			}
			return atoiBytes(value)
		case b >= '0' && b <= '9':
			*target = append(*target, b)
		default:
			return 0, errors.New("malformed mode report")
		}
	}
}

func atoiBytes(digits []byte) (int, error) {
	if len(digits) == 0 {
		return 0, errors.New("no value in mode report")
	}

	value := 0
	for _, d := range digits {
		value = value*10 + int(d-'0')
	}
	return value, nil
}
