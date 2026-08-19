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
