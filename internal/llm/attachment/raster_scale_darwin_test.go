package attachment

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/png"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestResizedRasterPixels(t *testing.T) {
	check := func(t *testing.T, input []byte, want string) {
		t.Helper()
		out, err := prepareImage(InputFile{Data: input}, models.ImageResize2000)
		require.NoError(t, err)
		img, err := png.Decode(bytes.NewReader(out.Data))
		require.NoError(t, err)
		pixels := image.NewNRGBA(img.Bounds())
		draw.Draw(pixels, pixels.Bounds(), img, img.Bounds().Min, draw.Src)
		require.Equal(t, want, fmt.Sprintf("%x", sha256.Sum256(pixels.Pix)))
	}
	t.Run("16-bit gray ramp", func(t *testing.T) {
		img := image.NewGray16(image.Rect(0, 0, 6400, 16))
		for y := range img.Bounds().Dy() {
			for x := range img.Bounds().Dx() {
				img.SetGray16(x, y, color.Gray16{Y: uint16(x * 237)})
			}
		}
		var data bytes.Buffer
		require.NoError(t, png.Encode(&data, img))
		check(t, data.Bytes(), "3594d2f1d81f7f78a5bfdb0b553e01980217447ce7797bdf561967d0792b1faf")
	})
	t.Run("color gradient", func(t *testing.T) {
		img := image.NewNRGBA(image.Rect(0, 0, 2400, 64))
		for y := range img.Bounds().Dy() {
			for x := range img.Bounds().Dx() {
				img.SetNRGBA(x, y, color.NRGBA{R: byte(x * 3), G: byte(y * 4), B: byte(x/8 + y), A: 255})
			}
		}
		var data bytes.Buffer
		require.NoError(t, png.Encode(&data, img))
		check(t, data.Bytes(), "9cbf8948069a197b95bd95df84bf84e655eb6d60d1c11dca19997b4124eea300")
	})
	for _, tc := range []struct {
		name                     string
		left, top, width, height int
		want                     string
	}{
		{"opaque margins", 2200, 0, 4300, 16, "9f87ae44f27abbe7cfc88b3470423e6756f53cbf168ae66aaa78680bd5f2f742"},
		{"transparent margins", 150, 2, 4500, 48, "bac65b3578bcbdf345201f1926743d6521caa07fee25cdb5dc7460714cf9cfb9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			palette := color.Palette{color.White, color.RGBA{R: 255, A: 255}}
			frame := image.NewPaletted(image.Rect(tc.left, tc.top, tc.left+2100, tc.top+16), palette)
			for i := range frame.Pix {
				frame.Pix[i] = 1
			}
			var data bytes.Buffer
			require.NoError(t, gif.EncodeAll(&data, &gif.GIF{Image: []*image.Paletted{frame}, Delay: []int{0}, Config: image.Config{ColorModel: palette, Width: tc.width, Height: tc.height}}))
			check(t, data.Bytes(), tc.want)
		})
	}
}

func TestPlanarColorModelsUsePackedRasterScaler(t *testing.T) {
	bounds := image.Rect(0, 0, 320, 32)
	ycbcr := image.NewYCbCr(bounds, image.YCbCrSubsampleRatio420)
	translucent := image.NewNYCbCrA(bounds, image.YCbCrSubsampleRatio420)
	cmyk := image.NewCMYK(bounds)
	for y := range bounds.Dy() {
		for x := range bounds.Dx() {
			ycbcr.Y[ycbcr.YOffset(x, y)] = byte(x*3 + y)
			i := ycbcr.COffset(x, y)
			ycbcr.Cb[i], ycbcr.Cr[i] = byte(x+y*3), byte(y-x)
			translucent.A[translucent.AOffset(x, y)] = byte(x + y)
			cmyk.SetCMYK(x, y, color.CMYK{C: byte(x * 2), M: byte(y * 3), Y: byte(x + y), K: byte(y)})
		}
	}
	translucent.YCbCr = *ycbcr
	for name, src := range map[string]image.Image{"YCbCr": ycbcr, "NYCbCrA": translucent, "CMYK": cmyk} {
		t.Run(name, func(t *testing.T) {
			packed := image.NewNRGBA(bounds)
			draw.Draw(packed, bounds, src, bounds.Min, draw.Src)
			want := resizeRaster(packed, 137, 14)
			got := resizeRaster(src, 137, 14)
			require.Equal(t, want.Pix, got.Pix)
		})
	}
}
