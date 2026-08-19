package ui

import (
	"os"
	"strings"

	"github.com/rivo/uniseg"
)

// Non-Latin scripts still break the column maths, because Bubble Tea and the
// terminal disagree about how wide they are. Until that is solved, every piece
// of text that comes from WhatsApp is folded down to plain ASCII, where a
// character is reliably one column and nothing can drift.
//
// Only *content* goes through this. The borders, rules and tick marks are ours
// and known-safe, so they keep their box-drawing characters.
//
// WHATSNATIVE_ASCII=0 turns the folding off for anyone whose terminal copes.
var foldToASCII = os.Getenv("WHATSNATIVE_ASCII") != "0"

// Emoji are kept: both width rulers agree they are two cells, so unlike a
// Malayalam conjunct they do not throw the columns out. WHATSNATIVE_ASCII=strict
// folds them too, for a terminal that disagrees.
var strictASCII = os.Getenv("WHATSNATIVE_ASCII") == "strict"

// replacement stands in for a grapheme we cannot measure reliably.
const replacement = "?"

// plain folds text to ASCII, one replacement character per grapheme cluster so
// the result is exactly as wide as it looks.
func plain(s string) string {
	if !foldToASCII || isASCII(s) {
		return s
	}

	var out strings.Builder
	out.Grow(len(s))

	graphemes := uniseg.NewGraphemes(s)
	for graphemes.Next() {
		cluster := graphemes.Str()
		switch {
		case isASCII(cluster):
			out.WriteString(cluster)
		case isEmoji(cluster) && !strictASCII:
			out.WriteString(cluster)
		case emojiTokens[cluster] != "":
			// In strict mode, keep the meaning rather than losing it entirely.
			out.WriteString(emojiTokens[cluster])
		default:
			out.WriteString(replacement)
		}
	}
	return out.String()
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// isEmoji reports whether a grapheme cluster is a pictograph rather than
// letters of some script. Modifiers on their own do not make one.
func isEmoji(cluster string) bool {
	for _, r := range cluster {
		switch {
		case r >= 0xFE00 && r <= 0xFE0F, r == 0x200D:
			// Variation selectors and the zero-width joiner decorate whatever
			// they are attached to; keep looking.
			continue
		case r >= 0x1F000 && r <= 0x1FAFF,
			r >= 0x2600 && r <= 0x27BF,
			r >= 0x2B00 && r <= 0x2BFF,
			r >= 0x2190 && r <= 0x21FF,
			r == 0x203C, r == 0x2049:
			return true
		}
	}
	return false
}

// emojiTokens keeps the reactions readable once folded. Anything not listed
// becomes a plain marker, which still counts correctly.
var emojiTokens = map[string]string{
	"👍":  "+1",
	"👎":  "-1",
	"❤️": "<3",
	"❤":  "<3",
	"😂":  ":D",
	"😀":  ":)",
	"😊":  ":)",
	"😮":  ":o",
	"😢":  ":(",
	"😭":  ":'(",
	"🙏":  "thx",
	"🔥":  "fire",
	"🎉":  "party",
	"✅":  "ok",
}

// reactionLabel is how a reaction is drawn: the emoji itself when the terminal
// is trusted with it, an ASCII token when it is not.
func reactionLabel(emoji string) string {
	if !strictASCII {
		return emoji
	}
	if token := emojiTokens[emoji]; token != "" {
		return token
	}
	return "*"
}
