package ui

import (
	"bytes"
	"fmt"
	"image"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	// Registering decoders for the formats WhatsApp actually sends.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	nativewebp "github.com/HugoSmits86/nativewebp"
	animatedWebp "github.com/gen2brain/webp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// alphaFloor is the alpha below which a pixel counts as transparent. Stickers
// are mostly transparent, so getting this wrong makes them look like blocks.
const alphaFloor = 0x2000

// imageCache keeps rendered images keyed by file and size.
//
// render runs on every frame, and scaling a photo takes milliseconds, so
// without this the UI would visibly stutter whenever a picture was on screen.
var imageCache = struct {
	sync.Mutex
	rows map[string][]string
}{rows: map[string][]string{}}

// renderImage draws an image file as terminal rows that fit inside maxWidth
// columns and maxHeight rows.
//
// Each cell carries two pixels: the upper half block ▀ takes the top pixel as
// its foreground and the bottom pixel as its background. That doubles the
// vertical resolution and needs nothing but truecolor.
//
// A finer per-cell search over quadrant glyphs did look better, but at sixteen
// candidate evaluations per cell it made scrolling a picture-heavy chat
// stutter, so this stays deliberately cheap.
func renderImage(path string, maxWidth, maxHeight int) ([]string, error) {
	if maxWidth < 2 || maxHeight < 1 {
		return nil, fmt.Errorf("no room to draw")
	}

	key := fmt.Sprintf("%s|%dx%d", path, maxWidth, maxHeight)

	imageCache.Lock()
	cached, ok := imageCache.rows[key]
	imageCache.Unlock()
	if ok {
		return cached, nil
	}

	rows, err := drawImage(path, maxWidth, maxHeight)
	if err != nil {
		return nil, err
	}

	imageCache.Lock()
	imageCache.rows[key] = rows
	imageCache.Unlock()
	return rows, nil
}

func drawImage(path string, maxWidth, maxHeight int) ([]string, error) {
	source, err := decodeImage(path)
	if err != nil {
		return nil, err
	}

	columns, rows := fitCells(source.Bounds().Dx(), source.Bounds().Dy(), maxWidth, maxHeight)
	if columns < 1 || rows < 1 {
		return nil, fmt.Errorf("image too small to draw")
	}

	// One cell is one pixel wide and two pixels tall.
	scaled := image.NewRGBA(image.Rect(0, 0, columns, rows*2))
	draw.CatmullRom.Scale(scaled, scaled.Bounds(), source, source.Bounds(), draw.Over, nil)

	lines := make([]string, 0, rows)
	for row := range rows {
		var line strings.Builder
		for column := range columns {
			line.WriteString(halfBlock(scaled, column, row*2))
		}
		line.WriteString(reset)
		lines = append(lines, line.String())
	}
	return lines, nil
}

// halfBlock renders one cell from the two pixels stacked in it, keeping
// transparency intact so stickers do not come out on a coloured slab.
func halfBlock(img *image.RGBA, x, y int) string {
	topR, topG, topB, topA := img.At(x, y).RGBA()
	bottomR, bottomG, bottomB, bottomA := img.At(x, y+1).RGBA()

	topVisible := topA > alphaFloor
	bottomVisible := bottomA > alphaFloor

	switch {
	case !topVisible && !bottomVisible:
		return reset + " "
	case topVisible && !bottomVisible:
		// Only the top pixel shows: draw the upper half block, no background.
		return reset + foreground(topR, topG, topB) + "▀"
	case !topVisible && bottomVisible:
		// Only the bottom pixel shows: flip to the lower half block.
		return reset + foreground(bottomR, bottomG, bottomB) + "▄"
	default:
		return foreground(topR, topG, topB) + background(bottomR, bottomG, bottomB) + "▀"
	}
}

// RGBA returns 16-bit channels; terminals want 8-bit, hence the shift.
func foreground(r, g, b uint32) string {
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r>>8, g>>8, b>>8)
}

func background(r, g, b uint32) string {
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r>>8, g>>8, b>>8)
}

// fitCells picks a cell size that preserves the image's aspect ratio, given
// that each cell holds two vertically stacked pixels.
func fitCells(pixelWidth, pixelHeight, maxWidth, maxHeight int) (columns, rows int) {
	if pixelWidth <= 0 || pixelHeight <= 0 {
		return 0, 0
	}

	columns = maxWidth
	rows = (columns * pixelHeight) / (pixelWidth * 2)

	if rows > maxHeight {
		rows = maxHeight
		columns = (rows * 2 * pixelWidth) / pixelHeight
	}

	return max(columns, 1), max(rows, 1)
}

// openExternally hands a file to the desktop's default application. Terminals
// cannot play video, so this is how video and documents get viewed.
func openExternally(path string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", path)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		command = exec.Command("xdg-open", path)
	}

	// Start rather than Run: the viewer keeps running after we return, and
	// waiting for it would freeze the UI until the user closed it.
	if err := command.Start(); err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	return nil
}

// humanSize formats a byte count for a media chip.
func humanSize(size int64) string {
	switch {
	case size <= 0:
		return ""
	case size < 1024:
		return fmt.Sprintf("%d B", size)
	case size < 1024*1024:
		return fmt.Sprintf("%.0f KB", float64(size)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(size)/(1024*1024))
	}
}

// decodeImage opens a picture from disk.
//
// The decoders are tried in order of cost, because most pictures are ordinary
// and the last one is not cheap:
//
//   - x/image handles jpeg, png and plain webp, but has no animation support
//     and refuses a VP8L frame that declares alpha through the extended header
//   - nativewebp covers that alpha case
//   - gen2brain runs libwebp itself, which is what finally reads the animated
//     stickers WhatsApp mostly sends. Only its first frame is drawn; a
//     terminal cannot animate them anyway.
func decodeImage(path string) (image.Image, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("open image: %w", err)
	}

	source, _, err := image.Decode(bytes.NewReader(data))
	if err == nil {
		return source, nil
	}
	firstErr := err

	if source, err := nativewebp.Decode(bytes.NewReader(data)); err == nil {
		return source, nil
	}
	if source, err := nativewebp.DecodeIgnoreAlphaFlag(bytes.NewReader(data)); err == nil {
		return source, nil
	}

	if animation, err := animatedWebp.DecodeAll(bytes.NewReader(data)); err == nil && len(animation.Image) > 0 {
		return animation.Image[0], nil
	}

	// Worth recording: a picture silently turning into a chip is the sort of
	// thing that otherwise takes a screenshot and a round trip to notice.
	slog.Warn("could not decode image", "path", path, "error", firstErr)
	return nil, fmt.Errorf("decode image: %w", firstErr)
}

// thumbCache is where embedded thumbnails are spilled to disk, once each, so
// the renderer only ever has to deal with files.
var thumbCache = struct {
	sync.Mutex
	paths map[string]string
}{paths: map[string]string{}}

// renderThumbnail draws the still picture WhatsApp embeds in a message.
//
// This is what makes animated stickers appear at all: their webp cannot be
// decoded, but every sticker carries a PNG thumbnail, and it needs no
// download, so it is on screen the moment the message arrives.
func renderThumbnail(id string, data []byte, maxWidth, maxHeight int) ([]string, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("no thumbnail")
	}

	path, err := thumbnailFile(id, data)
	if err != nil {
		return nil, err
	}
	return renderImage(path, maxWidth, maxHeight)
}

func thumbnailFile(id string, data []byte) (string, error) {
	thumbCache.Lock()
	defer thumbCache.Unlock()

	if path, ok := thumbCache.paths[id]; ok {
		return path, nil
	}

	dir := filepath.Join(os.TempDir(), "whatsnative-thumbs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create thumbnail dir: %w", err)
	}

	path := filepath.Join(dir, sanitiseID(id))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write thumbnail: %w", err)
	}

	thumbCache.paths[id] = path
	return path, nil
}

// sanitiseID keeps a message ID usable as a filename.
func sanitiseID(id string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 {
			return '_'
		}
		return r
	}, id)
	return safe + ".thumb"
}
