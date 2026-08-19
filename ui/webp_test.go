package ui

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	nativewebp "github.com/HugoSmits86/nativewebp"
	xwebp "golang.org/x/image/webp"
)

// chunk wraps a payload in a RIFF chunk, padded to an even length.
func chunk(fourCC string, payload []byte) []byte {
	var out bytes.Buffer
	out.WriteString(fourCC)
	binary.Write(&out, binary.LittleEndian, uint32(len(payload)))
	out.Write(payload)
	if len(payload)%2 == 1 {
		out.WriteByte(0)
	}
	return out.Bytes()
}

func uint24(value int) []byte {
	return []byte{byte(value), byte(value >> 8), byte(value >> 16)}
}

// animatedWebP hand-builds the container WhatsApp sends stickers in: a VP8X
// header flagged as animated, an ANIM chunk, and one ANMF frame wrapping a
// still VP8L image.
func animatedWebP(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.RGBA{R: uint8(x * 8), G: 100, B: 220, A: 255})
		}
	}

	var still bytes.Buffer
	if err := nativewebp.Encode(&still, img, nil); err != nil {
		t.Fatalf("encode still: %v", err)
	}

	// Pull the VP8L chunk out of the still file to use as the frame payload.
	data := still.Bytes()[12:]
	var frame []byte
	for len(data) >= 8 {
		size := int(binary.LittleEndian.Uint32(data[4:8]))
		padded := size + size%2
		if string(data[0:4]) == "VP8L" {
			frame = data[:8+padded]
			break
		}
		data = data[8+padded:]
	}
	if frame == nil {
		t.Fatal("no VP8L chunk in the encoded still")
	}

	vp8x := append([]byte{0x02, 0, 0, 0}, append(uint24(width-1), uint24(height-1)...)...)
	anim := []byte{0, 0, 0, 0, 0, 0}

	var anmf bytes.Buffer
	anmf.Write(uint24(0))
	anmf.Write(uint24(0))
	anmf.Write(uint24(width - 1))
	anmf.Write(uint24(height - 1))
	anmf.Write(uint24(100))
	anmf.WriteByte(0)
	anmf.Write(frame)

	var body bytes.Buffer
	body.WriteString("WEBP")
	body.Write(chunk("VP8X", vp8x))
	body.Write(chunk("ANIM", anim))
	body.Write(chunk("ANMF", anmf.Bytes()))

	var file bytes.Buffer
	file.WriteString("RIFF")
	binary.Write(&file, binary.LittleEndian, uint32(body.Len()))
	file.Write(body.Bytes())
	return file.Bytes()
}

// Animated stickers are the ones that kept showing as [sticker]: the standard
// decoders cannot read them at all.
func TestAnimatedWebPDecodes(t *testing.T) {
	data := animatedWebP(t, 48, 48)

	if _, err := xwebp.Decode(bytes.NewReader(data)); err == nil {
		t.Skip("x/image now reads animated webp; the fallback is redundant")
	}

	path := filepath.Join(t.TempDir(), "sticker.webp")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	decoded, err := decodeImage(path)
	if err != nil {
		t.Fatalf("animated sticker still will not decode: %v", err)
	}
	if decoded.Bounds().Dx() != 48 || decoded.Bounds().Dy() != 48 {
		t.Errorf("decoded %v, want 48x48", decoded.Bounds())
	}

	// And it renders rather than falling back to a chip.
	rows, err := renderImage(path, 20, 8)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(rows) == 0 {
		t.Error("no rows drawn for the animated sticker")
	}
}

// Something that is not an image at all still fails, rather than the fallback
// chain returning nonsense.
func TestGarbageIsNotAnImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junk.webp")
	if err := os.WriteFile(path, []byte("this is not a picture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeImage(path); err == nil {
		t.Error("garbage decoded as an image")
	}
}
