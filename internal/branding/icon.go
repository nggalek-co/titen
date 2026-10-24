package branding

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// Icon crops the source margins and makes its rounded-square corners transparent.
func Icon(data []byte) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	b = b.Inset(b.Dx() / 20)
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	rx, ry := float64(b.Dx())/2, float64(b.Dy())/2
	cornerRadius := math.Min(rx, ry) * 0.48
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			coverage := 0
			for sy := 0; sy < 4; sy++ {
				for sx := 0; sx < 4; sx++ {
					px := float64(x) + (float64(sx)+0.5)/4
					py := float64(y) + (float64(sy)+0.5)/4
					qx := math.Max(math.Abs(px-rx)-(rx-cornerRadius), 0)
					qy := math.Max(math.Abs(py-ry)-(ry-cornerRadius), 0)
					if qx*qx+qy*qy <= cornerRadius*cornerRadius {
						coverage++
					}
				}
			}
			c := color.NRGBAModel.Convert(src.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			c.A = uint8(int(c.A) * coverage / 16)
			dst.SetNRGBA(x, y, c)
		}
	}
	var out bytes.Buffer
	err = png.Encode(&out, dst)
	return out.Bytes(), err
}
