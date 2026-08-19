package client

import (
	"bytes"
	"image"

	// Registering the decoders is the whole point of these imports: they add
	// themselves to image.DecodeConfig's format list on init.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"
)

// imageSize reports an image's pixel dimensions, or zeroes for a format we
// cannot decode (animated webp among them). WhatsApp treats these as a display
// hint, so zero is survivable.
func imageSize(data []byte) (width, height int) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return config.Width, config.Height
}
