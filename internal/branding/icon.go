package branding

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// Icon removes the source artwork's opaque corners and adds Dock spacing.
func Icon(data []byte) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	b = b.Inset(b.Dx() / 25)
	pad := b.Dx() / 10
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx()+2*pad, b.Dy()+2*pad))
	rx, ry := float64(b.Dx())/2, float64(b.Dy())/2
	radius := math.Min(rx, ry) * 0.46
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			coverage := 0
			for sy := 0; sy < 4; sy++ {
				for sx := 0; sx < 4; sx++ {
					nx := math.Abs((float64(x) + (float64(sx)+0.5)/4 - rx) / rx)
					ny := math.Abs((float64(y) + (float64(sy)+0.5)/4 - ry) / ry)
					if nx*nx+ny*ny <= radius*radius {
						coverage++
					}
				}
			}
			c := color.NRGBAModel.Convert(src.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			c.A = uint8(int(c.A) * coverage / 16)
			dst.SetNRGBA(x+pad, y+pad, c)
		}
	}
	var out bytes.Buffer
	err = png.Encode(&out, dst)
	return out.Bytes(), err
}
