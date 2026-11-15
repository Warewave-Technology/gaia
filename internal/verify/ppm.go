package verify

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"strconv"
)

// ppmToPNG converts the binary PPM (P6, maxval 255) that qemu's screendump produces.
func ppmToPNG(ppm []byte) ([]byte, error) {
	r := bufio.NewReader(bytes.NewReader(ppm))
	var hdr [4]int // magic checked separately, then width, height, maxval
	magic, err := ppmToken(r)
	if err != nil || magic != "P6" {
		return nil, fmt.Errorf("not a P6 PPM (%q, %v)", magic, err)
	}
	for i := 1; i < 4; i++ {
		tok, err := ppmToken(r)
		if err != nil {
			return nil, err
		}
		if hdr[i], err = strconv.Atoi(tok); err != nil || hdr[i] <= 0 {
			return nil, fmt.Errorf("bad PPM header field %q", tok)
		}
	}
	w, h, maxval := hdr[1], hdr[2], hdr[3]
	if maxval != 255 || w > 16384 || h > 16384 {
		return nil, fmt.Errorf("unsupported PPM %dx%d maxval %d", w, h, maxval)
	}
	pix := make([]byte, w*h*3)
	if _, err := io.ReadFull(r, pix); err != nil {
		return nil, fmt.Errorf("PPM pixels: %w", err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range w * h {
		img.Set(i%w, i/w, color.NRGBA{pix[3*i], pix[3*i+1], pix[3*i+2], 255})
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// ppmToken reads one whitespace-separated header token, skipping # comments.
// The single whitespace byte after the last token (maxval) is consumed with it.
func ppmToken(r *bufio.Reader) (string, error) {
	var tok []byte
	for {
		c, err := r.ReadByte()
		if err != nil {
			if len(tok) > 0 && errors.Is(err, io.EOF) {
				return string(tok), nil
			}
			return "", err
		}
		switch {
		case c == '#' && len(tok) == 0:
			if _, err := r.ReadString('\n'); err != nil {
				return "", err
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if len(tok) > 0 {
				return string(tok), nil
			}
		default:
			tok = append(tok, c)
		}
	}
}
