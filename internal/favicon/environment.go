package favicon

import (
	"bytes"
	"html"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strconv"
	"strings"

	xhtml "golang.org/x/net/html"
)

// EnvironmentPNG puts the stock mark on a high-contrast environment tile.
// It is generated once by the handler, with no remote reads or font dependency.
func EnvironmentPNG(stock []byte, r, g, b uint8) ([]byte, error) {
	mark, err := png.Decode(bytes.NewReader(stock))
	if err != nil {
		return nil, err
	}
	tile := image.NewRGBA(image.Rect(0, 0, 64, 64))
	draw.Draw(tile, tile.Bounds(), image.NewUniform(color.RGBA{r, g, b, 255}), image.Point{}, draw.Src)
	// Keep a broad colored frame visible even in a 16px browser tab.
	bounds := mark.Bounds()
	inset := image.NewRGBA(image.Rect(0, 0, 48, 48))
	for y := 8; y < 56; y++ {
		for x := 8; x < 56; x++ {
			inset.Set(x-8, y-8, mark.At(bounds.Min.X+(x-8)*bounds.Dx()/48, bounds.Min.Y+(y-8)*bounds.Dy()/48))
		}
	}
	draw.Draw(tile, image.Rect(8, 8, 56, 56), inset, image.Point{}, draw.Over)
	var out bytes.Buffer
	err = png.Encode(&out, tile)
	return out.Bytes(), err
}

// PrefixTitle leaves app-authored RCDATA and attributes byte-for-byte intact.
// Environment identity is the sole opt-in exception to app title precedence.
func PrefixTitle(page []byte, prefix, fallback string) ([]byte, bool) {
	if prefix == "" {
		return page, false
	}
	if fallback == "" {
		fallback = "ShinyHub"
	}
	start, end, ok := environmentTitleBounds(page)
	if !ok {
		at := lastIndexFold(page, []byte("</head>"))
		if at < 0 {
			return page, false
		}
		snippet := []byte("<title>" + escapeEnvironmentText(prefix+fallback) + "</title>\n")
		out := make([]byte, 0, len(page)+len(snippet))
		out = append(out, page[:at]...)
		out = append(out, snippet...)
		out = append(out, page[at:]...)
		return out, true
	}
	content := strings.Join(strings.Fields(html.UnescapeString(string(page[start:end]))), " ")
	if strings.HasPrefix(content, prefix) {
		return page, false
	}
	var replacement []byte
	if content == "" || content == strings.TrimSpace(prefix) {
		replacement = []byte(escapeEnvironmentText(prefix + fallback))
	} else {
		replacement = append([]byte(escapeEnvironmentText(prefix)), page[start:end]...)
	}
	out := make([]byte, 0, len(page)+len(replacement))
	out = append(out, page[:start]...)
	out = append(out, replacement...)
	out = append(out, page[end:]...)
	return out, true
}

// Only a document-head title is browser identity; SVG/MathML titles remain app data.
func environmentTitleBounds(page []byte) (int, int, bool) {
	z := xhtml.NewTokenizer(bytes.NewReader(page))
	offset, start, foreign := 0, -1, 0
	for {
		typ := z.Next()
		at := offset
		offset += len(z.Raw())
		if typ == xhtml.ErrorToken {
			return 0, 0, false
		}
		if typ != xhtml.StartTagToken && typ != xhtml.EndTagToken && typ != xhtml.SelfClosingTagToken {
			continue
		}
		tok := z.Token()
		if typ == xhtml.EndTagToken {
			if tok.Data == "title" && start >= 0 {
				return start, at, true
			}
			if tok.Data == "head" {
				return 0, 0, false
			}
			if (tok.Data == "svg" || tok.Data == "math") && foreign > 0 {
				foreign--
			}
		} else {
			if tok.Data == "body" {
				return 0, 0, false
			}
			if tok.Data == "svg" || tok.Data == "math" {
				if typ != xhtml.SelfClosingTagToken {
					foreign++
				}
				continue
			}
			if tok.Data == "title" && foreign == 0 {
				start = offset
			}
		}
	}
}

// ReplaceIcons removes only actual rel=icon tokens, preserving touch/mask icons
// and every unrelated byte (including link-like text inside scripts/comments).
func ReplaceIcons(page []byte, href string) ([]byte, bool) {
	if href == "" || lastIndexFold(page, []byte("</head>")) < 0 {
		return page, false
	}
	z := xhtml.NewTokenizer(bytes.NewReader(page))
	offset, last := 0, 0
	var out []byte
	for {
		typ := z.Next()
		start := offset
		offset += len(z.Raw())
		if typ == xhtml.ErrorToken {
			break
		}
		if typ != xhtml.StartTagToken && typ != xhtml.SelfClosingTagToken {
			continue
		}
		tok := z.Token()
		if tok.Data != "link" {
			continue
		}
		remove := false
		for _, a := range tok.Attr {
			if a.Key == "rel" {
				for _, rel := range strings.Fields(a.Val) {
					if strings.EqualFold(rel, "icon") {
						remove = true
					}
				}
			}
		}
		if remove {
			if out == nil {
				out = make([]byte, 0, len(page)+len(href)+64)
			}
			out = append(out, page[last:start]...)
			last = offset
		}
	}
	if out != nil {
		out = append(out, page[last:]...)
		page = out
	}
	replaced, added := Ensure(page, href)
	return replaced, added || out != nil
}

func escapeEnvironmentText(text string) string {
	var out strings.Builder
	for _, r := range html.EscapeString(text) {
		if r > 127 {
			out.WriteString("&#" + strconv.Itoa(int(r)) + ";")
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}
