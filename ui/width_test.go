package ui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"whatsnative/db"
)

// useWidthMode pins the ruler for the duration of a test.
func useWidthMode(t *testing.T, mode widthMode) {
	t.Helper()

	previous := activeWidth.Load()
	activeWidth.Store(int32(mode))
	t.Cleanup(func() { activeWidth.Store(previous) })
}

// measure has to agree with whichever ruler Bubble Tea is using, because Bubble
// Tea clips every row to its own measurement.
func TestMeasureFollowsTheActiveRuler(t *testing.T) {
	const malayalam = "മലയാളം ഗ്രൂപ്പ്"

	useWidthMode(t, widthGrapheme)
	if got, want := measure(malayalam), ansi.StringWidth(malayalam); got != want {
		t.Errorf("grapheme mode: measure = %d, want %d", got, want)
	}

	useWidthMode(t, widthWc)
	if got, want := measure(malayalam), ansi.StringWidthWc(malayalam); got != want {
		t.Errorf("wc mode: measure = %d, want %d", got, want)
	}

	// The two really do disagree, which is the whole reason this matters.
	if ansi.StringWidth(malayalam) == ansi.StringWidthWc(malayalam) {
		t.Skip("the two rulers agree on this sample; the test proves nothing")
	}
}

func TestReadModeReportParsesValue(t *testing.T) {
	for _, tc := range []struct {
		report string
		want   int
	}{
		{"\x1b[?2027;0$y", 0},
		{"\x1b[?2027;1$y", 1},
		{"\x1b[?2027;2$y", 2},
	} {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}

		if _, err := writer.WriteString(tc.report); err != nil {
			t.Fatalf("write report: %v", err)
		}

		value, err := readModeReport(int(reader.Fd()), time.Now().Add(2*time.Second))
		if err != nil {
			t.Fatalf("read %q: %v", tc.report, err)
		}
		if value != tc.want {
			t.Errorf("%q parsed as %d, want %d", tc.report, value, tc.want)
		}

		reader.Close()
		writer.Close()
	}
}

// A terminal that stays silent must not wedge startup.
func TestReadModeReportGivesUp(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()

	start := time.Now()
	if _, err := readModeReport(int(reader.Fd()), start.Add(80*time.Millisecond)); err == nil {
		t.Fatal("expected a timeout when nothing answers")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("gave up after %v, want promptly", elapsed)
	}
}

func TestWidthModeCanBeForced(t *testing.T) {
	previous := activeWidth.Load()
	t.Cleanup(func() { activeWidth.Store(previous) })

	t.Setenv("WHATSNATIVE_WIDTH", "grapheme")
	detectWidthMode()
	if widthMode(activeWidth.Load()) != widthGrapheme {
		t.Error("WHATSNATIVE_WIDTH=grapheme was not honoured")
	}

	t.Setenv("WHATSNATIVE_WIDTH", "wc")
	detectWidthMode()
	if widthMode(activeWidth.Load()) != widthWc {
		t.Error("WHATSNATIVE_WIDTH=wc was not honoured")
	}
}

// The invariant that actually decides whether the border lands: under the
// ruler in force, every row is exactly the terminal width. Neither short, so
// the border cannot drift left, nor long, so Bubble Tea cannot clip it.
func TestRowsAreExactUnderEitherRuler(t *testing.T) {
	name := "മലയാളം ഗ്രൂപ്പ്"
	line := "എന്താണ് വിശേഷം സുഖമാണോ"

	store := fixtureStore(t)
	if err := store.SaveChatName("900@g.us", name, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(db.Message{
		ID: "ml", ChatJID: "900@g.us", SenderJID: "900@g.us", Sender: name,
		Content: line, Timestamp: time.Now(), IsGroup: true,
	}); err != nil {
		t.Fatal(err)
	}

	chats, err := store.Chats()
	if err != nil {
		t.Fatal(err)
	}

	for _, mode := range []widthMode{widthWc, widthGrapheme} {
		useWidthMode(t, mode)

		for _, width := range []int{70, 96, 120} {
			page := openConversationsPage(&app{messages: store, width: width, height: 24})
			page.chats = chats
			page.status = ""
			for i, chat := range chats {
				if chat.JID == "900@g.us" {
					page.cursor = i
				}
			}
			messages, err := store.Messages("900@g.us", historyLimit)
			if err != nil {
				t.Fatal(err)
			}
			page.messages = messages

			for i, row := range strings.Split(page.render(), "\n") {
				if got := measure(row); got != width {
					t.Errorf("mode %d, width %d: row %d measures %d, want exactly %d",
						mode, width, i, got, width)
				}
			}
		}
	}
}
