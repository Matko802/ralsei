package gamebox

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"strings"
	"sync"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	border     = 10
	padding    = 26
	boxWidth   = 760
	fontSize   = 32
	lineHeight = 42
	maxLines   = 40
	maxRunes   = 2000
	avatarSize = 112
	avatarGap  = 24
)

type Renderer struct {
	face   font.Face
	mu     sync.RWMutex
	avatar image.Image
}

func (r *Renderer) SetAvatar(src image.Image) {
	dst := image.NewRGBA(image.Rect(0, 0, avatarSize, avatarSize))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	r.mu.Lock()
	r.avatar = monochrome(dst, 2.0)
	r.mu.Unlock()
}

func monochrome(src image.Image, contrast float64) *image.Gray {
	b := src.Bounds()
	g := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r16, g16, b16, _ := src.At(x, y).RGBA()
			lum := (0.299*float64(r16) + 0.587*float64(g16) + 0.114*float64(b16)) / 257
			v := (lum-128)*contrast + 128
			if v < 0 {
				v = 0
			}
			if v > 255 {
				v = 255
			}
			g.SetGray(x-b.Min.X, y-b.Min.Y, color.Gray{Y: uint8(v)})
		}
	}
	return g
}

func Load(paths ...string) (*Renderer, error) {
	var data []byte
	tried := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		tried = append(tried, p)
		b, err := os.ReadFile(p)
		if err == nil {
			data = b
			break
		}
	}
	if data == nil {
		return nil, fmt.Errorf("font not found: %s", strings.Join(tried, ", "))
	}
	f, err := opentype.Parse(data)
	if err != nil {
		return nil, err
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    fontSize,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, err
	}
	return &Renderer{face: face}, nil
}

func (r *Renderer) measure(s string) int {
	d := font.Drawer{Face: r.face}
	return d.MeasureString(s).Ceil()
}

func (r *Renderer) wrap(text string, limit int) []string {
	remain := []rune(text)
	if len(remain) > maxRunes {
		remain = append(remain[:maxRunes], []rune("...")...)
	}
	var out []string
	lines := strings.Split(string(remain), "\n")
	for _, para := range lines {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := ""
		for _, w := range words {
			cand := w
			if line != "" {
				cand = line + " " + w
			}
			if r.measure(cand) <= limit {
				line = cand
				continue
			}
			if line != "" {
				out = append(out, line)
				line = ""
			}
			for r.measure(w) > limit {
				cut := len([]rune(w))
				for cut > 1 && r.measure(string([]rune(w)[:cut])) > limit {
					cut--
				}
				out = append(out, string([]rune(w)[:cut]))
				w = string([]rune(w)[cut:])
			}
			line = w
		}
		if line != "" {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		out = append(out, "")
	}
	if len(out) > maxLines {
		out = append(out[:maxLines-1], "...")
	}
	return out
}

func (r *Renderer) Render(text string) ([]byte, error) {
	r.mu.RLock()
	avatar := r.avatar
	r.mu.RUnlock()

	textX := border + padding
	limit := boxWidth - 2*border - 2*padding
	if avatar != nil {
		limit -= avatarSize + avatarGap
		textX += avatarSize + avatarGap
	}
	lines := r.wrap(text, limit)
	textHeight := len(lines) * lineHeight
	contentHeight := textHeight
	if avatar != nil && avatarSize > contentHeight {
		contentHeight = avatarSize
	}
	height := 2*(border+padding) + contentHeight
	img := image.NewRGBA(image.Rect(0, 0, boxWidth, height))
	white := color.RGBA{255, 255, 255, 255}
	black := color.RGBA{0, 0, 0, 255}
	draw.Draw(img, img.Bounds(), &image.Uniform{white}, image.Point{}, draw.Src)
	inner := image.Rect(border, border, boxWidth-border, height-border)
	draw.Draw(img, inner, &image.Uniform{black}, image.Point{}, draw.Src)

	if avatar != nil {
		rect := image.Rect(border+padding, border+padding, border+padding+avatarSize, border+padding+avatarSize)
		draw.Draw(img, rect, avatar, avatar.Bounds().Min, draw.Src)
	}

	d := &font.Drawer{
		Dst:  img,
		Src:  &image.Uniform{white},
		Face: r.face,
	}
	ascent := r.face.Metrics().Ascent.Ceil()
	y := border + padding + ascent
	for _, line := range lines {
		d.Dot = fixed.P(textX, y)
		d.DrawString(line)
		y += lineHeight
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
