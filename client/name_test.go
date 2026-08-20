package client

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"whatsnative/db"
)

// The profile name is not an address-book entry. Treating it as one was why a
// contact saved as "Dad" showed up under whatever he had set as his own name,
// and without the tilde that marks an unsaved number.
func TestContactNameIgnoresProfileName(t *testing.T) {
	for _, tc := range []struct {
		name string
		info types.ContactInfo
		want string
	}{
		{
			name: "saved name wins",
			info: types.ContactInfo{FullName: "Dad", PushName: "Rintaro | Mad Scientist"},
			want: "Dad",
		},
		{
			name: "profile name alone is not a saved name",
			info: types.ContactInfo{PushName: "Rintaro | Mad Scientist"},
			want: "",
		},
		{
			name: "first name is a saved name",
			info: types.ContactInfo{FirstName: "Dad", PushName: "Rintaro"},
			want: "Dad",
		},
		{
			name: "business name counts",
			info: types.ContactInfo{BusinessName: "Braun Tube Works", PushName: "Mr Braun"},
			want: "Braun Tube Works",
		},
		{
			name: "nothing at all",
			info: types.ContactInfo{},
			want: "",
		},
	} {
		if got := contactName(tc.info); got != tc.want {
			t.Errorf("%s: contactName = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A sticker's webp is usually animated and undecodable, so the still WhatsApp
// embeds alongside it is the only thing that can be drawn.
func TestStickerThumbnailIsCaptured(t *testing.T) {
	thumbnail := []byte("pretend png")

	described := describe(&waE2E.Message{
		StickerMessage: &waE2E.StickerMessage{
			Mimetype:     proto.String("image/webp"),
			FileLength:   proto.Uint64(23000),
			PngThumbnail: thumbnail,
		},
	})

	if described.Media.Kind != db.MediaSticker {
		t.Fatalf("kind is %q, want sticker", described.Media.Kind)
	}
	if string(described.Media.Thumbnail) != string(thumbnail) {
		t.Errorf("thumbnail is %q, want the embedded still", described.Media.Thumbnail)
	}
}

func TestImageThumbnailIsCaptured(t *testing.T) {
	described := describe(&waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{
			Mimetype:      proto.String("image/jpeg"),
			Caption:       proto.String("look"),
			JPEGThumbnail: []byte("pretend jpeg"),
		},
	})

	if len(described.Media.Thumbnail) == 0 {
		t.Error("a photo should keep its embedded still too")
	}
	if described.Text != "look" {
		t.Errorf("caption is %q", described.Text)
	}
}

// The regression that stopped every message arriving: ProtocolMessage_REVOKE
// is zero, and GetType on a nil ProtocolMessage returns zero, so a plain text
// message looked exactly like a delete-for-everyone and was thrown away.
func TestOrdinaryMessagesAreNotMistakenForDeletions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message *waE2E.Message
		want    bool
	}{
		{
			name:    "plain text",
			message: &waE2E.Message{Conversation: proto.String("hello")},
			want:    false,
		},
		{
			name: "a photo",
			message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
				Mimetype: proto.String("image/jpeg"),
			}},
			want: false,
		},
		{
			name:    "nothing at all",
			message: &waE2E.Message{},
			want:    false,
		},
		{
			name: "an actual revoke",
			message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
				Type: waE2E.ProtocolMessage_REVOKE.Enum(),
			}},
			want: true,
		},
		{
			name: "some other protocol message",
			message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
				Type: waE2E.ProtocolMessage_EPHEMERAL_SETTING.Enum(),
			}},
			want: false,
		},
	} {
		if _, got := revokedBy(tc.message); got != tc.want {
			t.Errorf("%s: revokedBy = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// And the message still describes as something worth storing.
func TestPlainMessageSurvivesTheRevokeCheck(t *testing.T) {
	message := &waE2E.Message{Conversation: proto.String("El Psy Kongroo")}

	if _, revoked := revokedBy(message); revoked {
		t.Fatal("a plain message was taken for a deletion")
	}
	if described := describe(message); described.empty() || described.Text != "El Psy Kongroo" {
		t.Errorf("described as %+v", described)
	}
}

// A view-once photo arrives inside an envelope: nothing is set at the top
// level, so before it was unwrapped it described as empty and never reached
// the chat at all.
func TestViewOnceMediaIsUnwrapped(t *testing.T) {
	photo := &waE2E.ImageMessage{
		Mimetype:      proto.String("image/jpeg"),
		Caption:       proto.String("only once"),
		FileLength:    proto.Uint64(4096),
		JPEGThumbnail: []byte("pretend jpeg"),
	}
	clip := &waE2E.VideoMessage{
		Mimetype:   proto.String("video/mp4"),
		FileLength: proto.Uint64(8192),
	}

	for _, tc := range []struct {
		name         string
		message      *waE2E.Message
		wantKind     string
		wantText     string
		wantViewOnce bool
	}{
		{
			name:         "view once v1",
			message:      &waE2E.Message{ViewOnceMessage: wrapped(&waE2E.Message{ImageMessage: photo})},
			wantKind:     db.MediaImage,
			wantText:     "only once",
			wantViewOnce: true,
		},
		{
			name:         "view once v2",
			message:      &waE2E.Message{ViewOnceMessageV2: wrapped(&waE2E.Message{ImageMessage: photo})},
			wantKind:     db.MediaImage,
			wantText:     "only once",
			wantViewOnce: true,
		},
		{
			name:         "view once v2 extension",
			message:      &waE2E.Message{ViewOnceMessageV2Extension: wrapped(&waE2E.Message{VideoMessage: clip})},
			wantKind:     db.MediaVideo,
			wantViewOnce: true,
		},
		{
			// Disappearing is not view-once, and must not be labelled as it.
			name:     "disappearing",
			message:  &waE2E.Message{EphemeralMessage: wrapped(&waE2E.Message{VideoMessage: clip})},
			wantKind: db.MediaVideo,
		},
		{
			// Disappearing and view-once at once, which is a real combination.
			name: "view once inside a disappearing chat",
			message: &waE2E.Message{EphemeralMessage: wrapped(&waE2E.Message{
				ViewOnceMessageV2: wrapped(&waE2E.Message{ImageMessage: photo}),
			})},
			wantKind:     db.MediaImage,
			wantText:     "only once",
			wantViewOnce: true,
		},
	} {
		described := describe(tc.message)
		if described.empty() {
			t.Errorf("%s: described as empty, so it would never be stored", tc.name)
			continue
		}
		if described.Media.Kind != tc.wantKind {
			t.Errorf("%s: kind is %q, want %q", tc.name, described.Media.Kind, tc.wantKind)
		}
		if described.Text != tc.wantText {
			t.Errorf("%s: text is %q, want %q", tc.name, described.Text, tc.wantText)
		}
		if described.Media.ViewOnce != tc.wantViewOnce {
			t.Errorf("%s: view once is %v, want %v", tc.name, described.Media.ViewOnce, tc.wantViewOnce)
		}

		// DownloadAny only looks at a message's top level, so what we store has
		// to be the unwrapped one or the attachment could never be fetched.
		var stored waE2E.Message
		if err := proto.Unmarshal(described.Media.Proto, &stored); err != nil {
			t.Fatalf("%s: stored proto does not parse: %v", tc.name, err)
		}
		if stored.GetImageMessage() == nil && stored.GetVideoMessage() == nil {
			t.Errorf("%s: stored proto is still wrapped, so it cannot be downloaded", tc.name)
		}
	}
}

// Unwrapping must not turn an ordinary message into something else, and must
// not loop on an envelope holding nothing.
func TestUnwrapLeavesOrdinaryMessagesAlone(t *testing.T) {
	plain := &waE2E.Message{Conversation: proto.String("El Psy Kongroo")}
	if got, viewOnce := unwrap(plain); got != plain || viewOnce {
		t.Error("a plain message should come back untouched and unmarked")
	}
	if got, viewOnce := unwrap(nil); got != nil || viewOnce {
		t.Error("nil should stay nil")
	}
	empty := &waE2E.Message{ViewOnceMessageV2: &waE2E.FutureProofMessage{}}
	if got, _ := unwrap(empty); got != empty {
		t.Error("an empty envelope has nothing inside, so it is its own payload")
	}

	// A disappearing message is not a view-once one; only the reader who has
	// both straight can trust either label.
	clip := &waE2E.Message{VideoMessage: &waE2E.VideoMessage{Mimetype: proto.String("video/mp4")}}
	if _, viewOnce := unwrap(&waE2E.Message{EphemeralMessage: wrapped(clip)}); viewOnce {
		t.Error("a disappearing message was marked view once")
	}
}

func wrapped(m *waE2E.Message) *waE2E.FutureProofMessage {
	return &waE2E.FutureProofMessage{Message: m}
}
