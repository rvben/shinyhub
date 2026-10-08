package favicon

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func TestEnvironmentPNG(t *testing.T) {
	var src bytes.Buffer
	mark := image.NewRGBA(image.Rect(0, 0, 64, 64))
	mark.Set(32, 32, color.White)
	if err := png.Encode(&src, mark); err != nil {
		t.Fatal(err)
	}
	data, err := EnvironmentPNG(src.Bytes(), 245, 179, 1)
	if err != nil {
		t.Fatal(err)
	}
	icon, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, a := icon.At(0, 0).RGBA()
	if r != 245*257 || g != 179*257 || b != 257 || a != 65535 {
		t.Fatal("wrong tile color")
	}
	_, _, _, a = icon.At(20, 20).RGBA()
	if a != 65535 {
		t.Fatal("transparent mark cleared tile")
	}
}
func TestEnvironmentIdentityPreservesAuthoredMarkup(t *testing.T) {
	page := []byte(`<html><head><title data-x="x">A &amp; B</title><!-- <link rel="icon" href="fake"> --><link REL="shortcut ICON" href="old"><link rel="apple-touch-icon" href="touch"></head><body><script>const x='<link rel="icon" href="fake">';</script></body></html>`)
	out, _ := PrefixTitle(page, "[A & B] ", "fallback")
	if !bytes.Contains(out, []byte(`<title data-x="x">[A &amp; B] A &amp; B</title>`)) {
		t.Fatal(string(out))
	}
	twice, changed := PrefixTitle(out, "[A & B] ", "fallback")
	if changed || !bytes.Equal(twice, out) {
		t.Fatal("prefix not idempotent")
	}
	out, _ = ReplaceIcons(out, "/environment.png")
	if bytes.Contains(out, []byte(`href="old"`)) || !bytes.Contains(out, []byte(`href="touch"`)) || strings.Count(string(out), `href="fake"`) != 2 {
		t.Fatal(string(out))
	}
}

func TestEnvironmentEmptyAndForeignTitles(t *testing.T) {
	for _, title := range []string{"", "  ", "[Acceptance]"} {
		page := []byte("<html><head><title>" + title + "</title></head><body></body></html>")
		out, _ := PrefixTitle(page, "[Acceptance] ", "FinOps")
		if !bytes.Contains(out, []byte("<title>[Acceptance] FinOps</title>")) {
			t.Fatal(string(out))
		}
		twice, changed := PrefixTitle(out, "[Acceptance] ", "FinOps")
		if changed || !bytes.Equal(twice, out) {
			t.Fatal("empty title fallback not idempotent")
		}
	}
	page := []byte("<html><head></head><body><svg><title>Chart tooltip</title></svg></body></html>")
	out, _ := PrefixTitle(page, "[Acceptance] ", "FinOps")
	if !bytes.Contains(out, []byte("<title>[Acceptance] FinOps</title>")) || !bytes.Contains(out, []byte("<svg><title>Chart tooltip</title></svg>")) {
		t.Fatal(string(out))
	}
	incomplete := []byte(`<head><link rel="icon" href="original">`)
	out, changed := ReplaceIcons(incomplete, "/environment.png")
	if changed || !bytes.Equal(incomplete, out) {
		t.Fatal("removed icon without an insertion point")
	}
}
