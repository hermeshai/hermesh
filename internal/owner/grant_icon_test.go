package owner

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestGrantImage(t *testing.T) {
	enc := func(w, h int, f func(*bytes.Buffer, image.Image) error) []byte {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for i := range img.Pix {
			img.Pix[i] = uint8(i)
		}
		img.Set(0, 0, color.RGBA{255, 0, 0, 255})
		var b bytes.Buffer
		if err := f(&b, img); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	asPNG := func(b *bytes.Buffer, m image.Image) error { return png.Encode(b, m) }
	asJPEG := func(b *bytes.Buffer, m image.Image) error { return jpeg.Encode(b, m, nil) }

	// Only the pixels go in: bytes after the image are dropped.
	in := append(enc(128, 128, asPNG), "GPS 31.2N 121.5E"...)
	out, err := grantImage(in)
	if err != nil || bytes.Contains(out, []byte("GPS")) {
		t.Fatalf("a PNG with trailing bytes: %v", err)
	}
	if m, f, err := image.Decode(bytes.NewReader(out)); err != nil || f != "png" || m.Bounds().Dx() != 128 {
		t.Fatalf("re-encoded PNG: %v %s", err, f)
	}
	if out, err := grantImage(enc(96, 96, asJPEG)); err != nil || !bytes.HasPrefix(out, []byte{0xff, 0xd8}) {
		t.Fatalf("a JPEG stays a JPEG: %v", err)
	}
	for name, b := range map[string][]byte{
		"not square": enc(128, 100, asPNG),
		"too big":    enc(300, 300, asPNG),
		"too small":  enc(8, 8, asPNG),
		"svg":        []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`),
		"over 64 KB": make([]byte, 65<<10),
	} {
		if _, err := grantImage(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for icon, ok := range map[string]bool{"": true, "letter": true, "image": true, "app:grokbot": true, "app:": false, "app:Grok Bot": false, "grokbot": false} {
		if grantIcons.MatchString(icon) != ok {
			t.Errorf("icon %q: want ok=%v", icon, ok)
		}
	}
}
