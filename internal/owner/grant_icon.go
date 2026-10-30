package owner

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"regexp"

	"connectrpc.com/connect"
)

// grantIcons are the values a grant's client_icon takes: "" its app's (the
// default), "letter", "image", or "app:<logo id>".
var grantIcons = regexp.MustCompile(`^(|letter|image|app:[a-z0-9-]{1,32})$`)

// grantImage checks an icon picture and re-encodes it, so only its pixels
// go into the grant (no metadata, no trailing bytes): a square PNG or JPEG
// of 16 to 256 px.
func grantImage(b []byte) ([]byte, error) {
	bad := func(msg string) ([]byte, error) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(msg))
	}
	if len(b) > 64<<10 {
		return bad("the picture is over 64 KB")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || (format != "png" && format != "jpeg") {
		return bad("the picture is not a PNG or JPEG")
	}
	if cfg.Width != cfg.Height || cfg.Width < 16 || cfg.Width > 256 {
		return bad("the picture must be square, 16 to 256 px")
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return bad("the picture is not a PNG or JPEG")
	}
	var out bytes.Buffer
	if format == "png" {
		err = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, img)
	} else {
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 88})
	}
	if err != nil {
		return nil, err
	}
	if out.Len() > 64<<10 {
		return bad("the picture is over 64 KB")
	}
	return out.Bytes(), nil
}
