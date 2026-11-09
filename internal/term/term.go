package term

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	boxWidth   = 920
	padding    = 16
	headerH    = 34
	fontSize   = 16
	lineHeight = 21
	maxLines   = 26
)

var (
	bg     = color.RGBA{12, 12, 12, 255}
	header = color.RGBA{26, 26, 26, 255}
	fg     = color.RGBA{230, 230, 230, 255}
	dim    = color.RGBA{140, 140, 140, 255}
	green  = color.RGBA{126, 231, 135, 255}
)

type Renderer struct {
	face font.Face
}

func Load(path string) (*Renderer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
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
	var out []string
	for _, para := range strings.Split(text, "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		runes := []rune(para)
		for len(runes) > 0 {
			cut := len(runes)
			for cut > 0 && r.measure(string(runes[:cut])) > limit {
				cut--
			}
			if cut == 0 {
				cut = 1
			}
			out = append(out, string(runes[:cut]))
			runes = runes[cut:]
		}
	}
	return out
}

func (r *Renderer) Render(title, command, output string) ([]byte, error) {
	limit := boxWidth - 2*padding
	lines := r.wrap(output, limit)
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	height := padding*2 + headerH + lineHeight*(len(lines)+1)
	img := image.NewRGBA(image.Rect(0, 0, boxWidth, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, 0, boxWidth, headerH), &image.Uniform{header}, image.Point{}, draw.Src)

	drawText := func(x, y int, c color.RGBA, s string) {
		d := &font.Drawer{Dst: img, Src: &image.Uniform{c}, Face: r.face}
		d.Dot = fixed.P(x, y)
		d.DrawString(s)
	}

	ascent := r.face.Metrics().Ascent.Ceil()
	drawText(padding, (headerH+ascent)/2+2, dim, title)
	drawText(padding, headerH+padding+ascent, green, "ralsei@artix:~$ ")
	promptWidth := r.measure("ralsei@artix:~$ ")
	drawText(padding+promptWidth, headerH+padding+ascent, fg, command)

	y := headerH + padding + ascent + lineHeight
	for _, line := range lines {
		drawText(padding, y, fg, line)
		y += lineHeight
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
