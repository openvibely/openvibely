package attachment

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"runtime"
	"slices"
	"testing"

	webpcodec "github.com/gen2brain/webp"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/webp"
)

func TestModelImageDimensions(t *testing.T) {
	for _, tc := range []struct {
		profile                              models.ImageResizeProfile
		width, height, wantWidth, wantHeight int
	}{
		{models.ImageResize6000, 3456, 2234, 3456, 2234},
		{models.ImageResize6000, 6401, 100, 6000, 94},
		{models.ImageResize6000, 6000, 6000, 3200, 3200},
		{models.ImageResize2000, 3456, 2234, 2000, 1293},
		{models.ImageResize1568, 3456, 2234, 1372, 887},
		{models.ImageResize1568, 128, 256, 128, 256},
	} {
		w, h := limitsFor(tc.profile).dimensions(tc.width, tc.height)
		require.Equal(t, tc.wantWidth, w)
		require.Equal(t, tc.wantHeight, h)
	}
	for _, provider := range []models.LLMProvider{models.ProviderAnthropic, models.ProviderOpenAI} {
		for _, spec := range models.ProviderModels(provider) {
			require.Positive(t, limitsFor(spec.ImageResizeProfile).side, spec.ID)
		}
	}
}

func TestPrepareImagePreservesFittingBytesAndCompressesOverBudget(t *testing.T) {
	var src bytes.Buffer
	require.NoError(t, png.Encode(&src, image.NewNRGBA(image.Rect(0, 0, 48, 24))))
	input := InputFile{MediaType: "image/png", Data: src.Bytes()}
	fit, err := prepareImage(input, models.ImageResize1568)
	require.NoError(t, err)
	require.Equal(t, input, fit)
	// Use legal trailing bytes to exceed the byte budget independently of dimensions.
	input.Data = append(input.Data, make([]byte, 4<<20)...)
	compressed, err := prepareImage(input, models.ImageResize1568)
	require.NoError(t, err)
	require.LessOrEqual(t, len(compressed.Data), limitsFor(models.ImageResize1568).bytes)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(compressed.Data))
	require.NoError(t, err)
	require.Equal(t, 48, cfg.Width)
	require.Equal(t, 24, cfg.Height)
}

func TestImageCompressionTarget(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 120, 64))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 17)
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	for _, format := range []string{"png", "jpeg"} {
		t.Run(format, func(t *testing.T) {
			var encoded bytes.Buffer
			if format == "jpeg" {
				require.NoError(t, jpeg.Encode(&encoded, img, nil))
			} else {
				require.NoError(t, png.Encode(&encoded, img))
			}
			data := make([]byte, softImageByteLimit+1)
			copy(data, encoded.Bytes())
			input := InputFile{MediaType: "image/" + format, Data: data[:softImageByteLimit]}
			prepared, err := prepareImage(input, models.ImageResize2000)
			require.NoError(t, err)
			require.Equal(t, input, prepared)
			input.Data = data
			prepared, err = prepareImage(input, models.ImageResize6000)
			require.NoError(t, err)
			require.Equal(t, input, prepared)
			prepared, err = prepareImage(input, models.ImageResize2000)
			require.NoError(t, err)
			require.Equal(t, "image/jpeg", prepared.MediaType)
			require.LessOrEqual(t, len(prepared.Data), softImageByteLimit)
			got, err := jpeg.Decode(bytes.NewReader(prepared.Data))
			require.NoError(t, err)
			require.Equal(t, img.Bounds(), got.Bounds())
		})
	}
}

func TestJPEGKeepsColorUnderAlpha(t *testing.T) {
	for _, width := range []int{600, 2400} {
		img := image.NewNRGBA(image.Rect(0, 0, width, 600))
		_, err := rand.New(rand.NewSource(7)).Read(img.Pix)
		require.NoError(t, err)
		for y := 0; y < 600; y++ {
			for x := 0; x < width/4; x++ {
				img.SetNRGBA(x, y, color.NRGBA{R: 255})
			}
			for x := width / 4; x < width/2; x++ {
				img.SetNRGBA(x, y, color.NRGBA{G: 255, A: 100})
			}
		}
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, img))
		out, err := prepareImage(InputFile{MediaType: "image/png", Data: buf.Bytes()}, models.ImageResize2000)
		require.NoError(t, err)
		require.Equal(t, "image/jpeg", out.MediaType)
		got, err := jpeg.Decode(bytes.NewReader(out.Data))
		require.NoError(t, err)
		w := got.Bounds().Dx()
		require.Equal(t, min(2000, width), w)
		red := color.NRGBAModel.Convert(got.At(w/8, 100)).(color.NRGBA)
		green := color.NRGBAModel.Convert(got.At(3*w/8, 100)).(color.NRGBA)
		require.InDelta(t, 255, red.R, 3)
		require.InDelta(t, 255, green.G, 3)
	}
}

func TestFinalJPEGResizePreservesTranslucentColor(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2100, 100))
	_, err := rand.New(rand.NewSource(7)).Read(img.Pix)
	require.NoError(t, err)
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < 400; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, A: 64})
		}
	}
	var input bytes.Buffer
	require.NoError(t, png.Encode(&input, img))
	config, err := png.DecodeConfig(bytes.NewReader(input.Bytes()))
	require.NoError(t, err)
	limits := limitsFor(models.ImageResize2000)
	limits.bytes = 10_000 // Force the final resize without a multi-megabyte fixture.
	out, err := prepareImageWithConfig(InputFile{Data: input.Bytes()}, models.ImageResize2000, limits, config, "png")
	require.NoError(t, err)
	require.LessOrEqual(t, len(out.Data), limits.bytes)
	got, err := jpeg.Decode(bytes.NewReader(out.Data))
	require.NoError(t, err)
	require.Equal(t, 1000, got.Bounds().Dx())
	red := color.NRGBAModel.Convert(got.At(50, 20)).(color.NRGBA)
	require.InDelta(t, 255, red.R, 4)
}

func TestSizesGIFByFirstFrame(t *testing.T) {
	for _, tc := range []struct {
		frame       image.Rectangle
		passthrough bool
	}{
		{image.Rect(240, 120, 480, 240), true},
		{image.Rect(240, 120, 2340, 540), false},
		{image.Rect(2200, 0, 4300, 16), false},
	} {
		palette := color.Palette{color.White, color.RGBA{R: 255, A: 255}}
		frame := image.NewPaletted(tc.frame, palette)
		for i := range frame.Pix {
			frame.Pix[i] = 1
		}
		var buf bytes.Buffer
		require.NoError(t, gif.EncodeAll(&buf, &gif.GIF{Image: []*image.Paletted{frame}, Delay: []int{0}, Config: image.Config{ColorModel: palette, Width: max(2400, tc.frame.Max.X), Height: 1200}}))
		input := InputFile{MediaType: "image/gif", Data: buf.Bytes()}
		out, err := prepareImage(input, models.ImageResize2000)
		require.NoError(t, err)
		if tc.passthrough {
			require.Equal(t, input, out)
			continue
		}
		require.Equal(t, "image/png", out.MediaType)
		img, err := png.Decode(bytes.NewReader(out.Data))
		require.NoError(t, err)
		want := image.Rect(0, 0, 2000, 400)
		if tc.frame.Dy() == 16 {
			want = image.Rect(0, 0, 1969, 15)
		}
		require.Equal(t, want, img.Bounds())
		points := []image.Point{want.Max.Div(2)}
		if runtime.GOOS != "darwin" {
			points = append(points, want.Min, want.Max.Sub(image.Pt(1, 1)))
		}
		for _, point := range points {
			require.Equal(t, color.NRGBA{R: 255, A: 255}, color.NRGBAModel.Convert(img.At(point.X, point.Y)))
		}
	}
}

func TestLosslessAnimatedWebPPreservesRenderingMetadata(t *testing.T) {
	data, err := os.ReadFile("../../service/testdata/animated-2400x1200.webp")
	require.NoError(t, err)
	// Keep this animation narrow vertically so this test focuses on metadata.
	binary.LittleEndian.PutUint16(data[24:26], 6399)
	binary.LittleEndian.PutUint16(data[27:29], 31)
	data[20] |= 0x28
	icc := append([]byte("ICCP\x14\x00\x00\x00"), make([]byte, 16)...)
	icc = append(icc, "RGB "...)
	exif := []byte("EXIF\x03\x00\x00\x00abc\x00")
	data = slices.Insert(data, 30, icc...)
	data = append(data, exif...)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	out, err := prepareImage(InputFile{MediaType: "image/webp", Data: data}, models.ImageResize6000)
	require.NoError(t, err)
	require.Equal(t, "VP8X", string(out.Data[12:16]))
	require.Equal(t, byte(0x28), out.Data[20]&0x2a)
	require.True(t, bytes.HasPrefix(out.Data[30:], icc))
	require.True(t, bytes.HasSuffix(out.Data, exif))
	im, _, err := image.Decode(bytes.NewReader(out.Data))
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 6000, 30), im.Bounds())
}

func pngTestProfile(t *testing.T, space string) []byte {
	t.Helper()
	var profile bytes.Buffer
	profile.WriteString("Test profile\x00\x00")
	zw := zlib.NewWriter(&profile)
	_, err := zw.Write(append(make([]byte, 16), space...))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	iccp := binary.BigEndian.AppendUint32(nil, uint32(profile.Len()))
	iccp = append(iccp, "iCCP"...)
	iccp = append(iccp, profile.Bytes()...)
	iccp = binary.BigEndian.AppendUint32(iccp, crc32.ChecksumIEEE(iccp[4:]))
	return iccp
}

func TestResizedPNGGrayProfile(t *testing.T) {
	var src bytes.Buffer
	require.NoError(t, png.Encode(&src, image.NewGray(image.Rect(0, 0, 6400, 16))))
	chunk := pngTestProfile(t, "GRAY")
	input := InputFile{Data: slices.Insert(src.Bytes(), 33, chunk...)}
	for _, profile := range []models.ImageResizeProfile{models.ImageResize2000, models.ImageResize6000} {
		out, err := prepareImage(input, profile)
		require.NoError(t, err)
		require.Equal(t, profile == models.ImageResize2000, bytes.Contains(out.Data, chunk))
	}
}

func TestJPEGKeepsGrayscaleProfileAcrossCompression(t *testing.T) {
	// First compress a fitting PNG to JPEG, then recompress the resulting JPEG.
	var src bytes.Buffer
	require.NoError(t, png.Encode(&src, image.NewGray(image.Rect(0, 0, 256, 128))))
	input := InputFile{Data: slices.Insert(src.Bytes(), 33, pngTestProfile(t, "GRAY")...)}
	for _, format := range []string{"png", "jpeg"} {
		input.Data = append(input.Data, make([]byte, softImageByteLimit)...)
		out, err := prepareImage(input, models.ImageResize2000)
		require.NoError(t, err)
		require.Equal(t, "image/jpeg", out.MediaType, format)
		require.True(t, bytes.Contains(jpegProfileSegments(out.Data, false), append(make([]byte, 16), "GRAY"...)), format)
		input = out
	}
}

func TestTriangleDownsamplingPreservesDetailAndRGBProfile(t *testing.T) {
	stripes := image.NewGray(image.Rect(0, 0, 24000, 16))
	for i := range stripes.Pix {
		if i%4 == 0 || i%4 == 3 {
			stripes.Pix[i] = 255
		}
	}
	var pngData bytes.Buffer
	require.NoError(t, png.Encode(&pngData, stripes))
	iccp := pngTestProfile(t, "RGB ")
	input := slices.Insert(pngData.Bytes(), 33, iccp...)
	out, err := prepareImage(InputFile{MediaType: "image/png", Data: input}, models.ImageResize6000)
	require.NoError(t, err)
	require.True(t, bytes.Contains(out.Data, iccp))
	img, err := png.Decode(bytes.NewReader(out.Data))
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 6000, 4), img.Bounds())
	require.Equal(t, color.Gray{Y: 128}, color.GrayModel.Convert(img.At(100, 2)))
	out, err = prepareImage(InputFile{Data: input}, models.ImageResize2000)
	require.NoError(t, err)
	require.True(t, bytes.Contains(out.Data, iccp))
	out.Data = append(out.Data, make([]byte, softImageByteLimit)...)
	out = applyCompressionTarget(out)
	require.Equal(t, "image/jpeg", out.MediaType)
	require.Contains(t, string(jpegProfileSegments(out.Data, false)), string(append(make([]byte, 16), "RGB "...)))
}

func TestResizePreservesStaticWebPFormat(t *testing.T) {
	var input bytes.Buffer
	require.NoError(t, webpcodec.Encode(&input, image.NewGray(image.Rect(0, 0, 6400, 96)), webpcodec.Options{Lossless: true}))
	icc := append([]byte("ICCP\x14\x00\x00\x00"), make([]byte, 16)...)
	icc = append(icc, "RGB "...)
	// Add an ICC chunk to the fixture's simple lossless WebP container.
	data := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x20\x00\x00\x00"), 0xff, 0x18, 0, 95, 0, 0)
	data = append(data, icc...)
	data = append(data, input.Bytes()[12:]...)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	out, err := prepareImage(InputFile{Data: data, MediaType: "image/webp"}, models.ImageResize2000)
	require.NoError(t, err)
	require.Equal(t, "image/webp", out.MediaType)
	gotProfile, _ := webpMetadata(out.Data)
	require.Equal(t, icc, gotProfile)
	img, err := webp.Decode(bytes.NewReader(out.Data))
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 2000, 30), img.Bounds())
	// The lossy output for this solid field is uniform, including the first pixel.
	first := img.At(0, 0)
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			if img.At(x, y) != first {
				t.Fatalf("nonuniform WebP at (%d, %d)", x, y)
			}
		}
	}
}

func TestLossyWebPResizedToLosslessPreservesColors(t *testing.T) {
	for _, tc := range []struct{ name, pixelHash string }{
		{"black", "b0a43034986fe2120262b367cf3a9a20a7e97a53139cfd5563111f1c9fc0ce10"},
		{"gradient", "1dfd64f266cad974e420a724491c19c234760a6f919951f1e0e40cec1156fed1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := image.NewNRGBA(image.Rect(0, 0, 7200, 64))
			for y := range src.Bounds().Dy() {
				for x := range src.Bounds().Dx() {
					pixel := color.NRGBA{A: 255}
					if tc.name == "gradient" {
						pixel.R, pixel.G, pixel.B = byte(x*3), byte(y*4), byte(x/8+y)
					}
					src.SetNRGBA(x, y, pixel)
				}
			}
			var input bytes.Buffer
			require.NoError(t, webpcodec.Encode(&input, src, webpcodec.Options{Quality: 90, Method: 4}))
			out, err := prepareImage(InputFile{Data: input.Bytes()}, models.ImageResize6000)
			require.NoError(t, err)
			require.Equal(t, "image/webp", out.MediaType)
			decoded, err := webp.Decode(bytes.NewReader(out.Data))
			require.NoError(t, err)
			require.Equal(t, image.Rect(0, 0, 6000, 53), decoded.Bounds())
			require.IsType(t, (*image.NRGBA)(nil), decoded)
			require.Equal(t, tc.pixelHash, fmt.Sprintf("%x", sha256.Sum256(decoded.(*image.NRGBA).Pix)))
		})
	}
}

func TestResizedWebPGradient(t *testing.T) {
	gradient := image.NewNRGBA(image.Rect(0, 0, 2400, 64))
	for y := range 64 {
		for x := range 2400 {
			gradient.SetNRGBA(x, y, color.NRGBA{R: byte(x * 3), G: byte(y * 4), B: byte(x/8 + y), A: 255})
		}
	}
	var input bytes.Buffer
	require.NoError(t, webpcodec.Encode(&input, gradient, webpcodec.Options{Lossless: true, Exact: true, Method: 4}))
	out, err := prepareImage(InputFile{Data: input.Bytes()}, models.ImageResize2000)
	require.NoError(t, err)
	require.Equal(t, "image/webp", out.MediaType)
	want := "96a84da4599a2e2f880d98f75973a3aecf700d870170a100b07ed973e661d8e9"
	if runtime.GOOS == "darwin" {
		want = "82f89f454840f5ac383b6a64cb0fb9db810d6131cb626c2a82a2b6646cae71ce"
	}
	require.Equal(t, want, fmt.Sprintf("%x", sha256.Sum256(out.Data)))
}

func TestResizeLossyWebPColorAndAlpha(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		alpha                    func(int) byte
		darwinHash, portableHash string
	}{
		{"opaque", func(x int) byte { return 255 }, "e870c4c12f30f65b1532d3f78b828707a512ffd6e7c9e07e058f9c0fe212894d", "032873827725240e569620c110ab3108d01f3005ff9abfea5249b8c6e45bae2f"},
		{"partial", func(x int) byte { return byte(40 + x%216) }, "c07d710660761aaf254ec4d5fd8f7bc6c294b634cae7a9b7849b862d47e5fa60", "6a0a00b12510e7ecb690d48a476214375ac5dd851aa53ade6c0da5b6e4135f44"},
		{"clear", func(x int) byte {
			if x%8 < 4 {
				return 0
			}
			return 255
		}, "e899263656be25c08135eccc1230451db50b9d1b484b78ab943bdec0f43ce290", "16664d35fe32583db491dcfce31d40ec647835840adb3796ec0a43594add4955"},
		{"invisible", func(x int) byte { return 0 }, "4b20ee6f4c2ff6f270a8e8a4a67aaec5cdc1ae57c310113ea37d8fd2c504e0a9", "b3ec0d29adc8b7ae779af89388e1a423032314df523141b7eacc7f2480b96226"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := image.NewNRGBA(image.Rect(0, 0, 2400, 64))
			for y := range 64 {
				for x := range 2400 {
					src.SetNRGBA(x, y, color.NRGBA{R: byte(x * 3), G: byte(y * 4), B: byte(x/8 + y), A: tc.alpha(x)})
				}
			}
			var input bytes.Buffer
			require.NoError(t, webpcodec.Encode(&input, src, webpcodec.Options{Quality: 90, Method: 4, Exact: true}))
			prepared, err := prepareImage(InputFile{Data: input.Bytes()}, models.ImageResize2000)
			require.NoError(t, err)
			want := tc.portableHash
			if runtime.GOOS == "darwin" {
				want = tc.darwinHash
			}
			require.Equal(t, "image/webp", prepared.MediaType)
			require.Equal(t, want, fmt.Sprintf("%x", sha256.Sum256(prepared.Data)))
		})
	}
}

func TestReencodedJPEGDropsCMYKProfile(t *testing.T) {
	var src bytes.Buffer
	require.NoError(t, jpeg.Encode(&src, image.NewGray(image.Rect(0, 0, 2400, 16)), nil))
	payload := append([]byte("ICC_PROFILE\x00\x01\x01"), make([]byte, 16)...)
	payload = append(payload, "CMYK"...)
	segment := append(binary.BigEndian.AppendUint16([]byte{0xff, 0xe2}, uint16(len(payload)+2)), payload...)
	input := InputFile{Data: slices.Insert(src.Bytes(), 2, segment...)}
	out, err := prepareImage(input, models.ImageResize2000)
	require.NoError(t, err)
	require.Equal(t, "image/jpeg", out.MediaType)
	require.False(t, bytes.Contains(out.Data, []byte("ICC_PROFILE")))
}

func TestLanczosDownsampleFineStripes(t *testing.T) {
	stripes := image.NewGray(image.Rect(0, 0, 8000, 16))
	for i := range stripes.Pix {
		if i%4 == 0 || i%4 == 3 {
			stripes.Pix[i] = 255
		}
	}
	var src bytes.Buffer
	require.NoError(t, png.Encode(&src, stripes))
	out, err := prepareImage(InputFile{Data: src.Bytes(), MediaType: "image/png"}, models.ImageResize2000)
	require.NoError(t, err)
	decoded, err := png.Decode(bytes.NewReader(out.Data))
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 2000, 4), decoded.Bounds())
	require.Equal(t, color.Gray{Y: 127}, color.GrayModel.Convert(decoded.At(1000, 2)))
	edge := color.Gray{Y: 120}
	if runtime.GOOS == "darwin" {
		edge.Y = 129
	}
	require.Equal(t, edge, color.GrayModel.Convert(decoded.At(0, 2)))
}

func TestPreparedJPEGKeepsMultipartRGBProfile(t *testing.T) {
	var src bytes.Buffer
	require.NoError(t, jpeg.Encode(&src, image.NewRGBA(image.Rect(0, 0, 6400, 96)), nil))
	marker := func(seq byte, data []byte) []byte {
		payload := append([]byte("ICC_PROFILE\x00"), seq, 2)
		payload = append(payload, data...)
		return append(binary.BigEndian.AppendUint16([]byte{0xff, 0xe2}, uint16(len(payload)+2)), payload...)
	}
	first := marker(1, make([]byte, 16))
	second := marker(2, []byte("RGB test profile"))
	input := slices.Insert(src.Bytes(), 2, append(slices.Clone(second), first...)...)
	for _, tc := range []struct {
		profile       models.ImageResizeProfile
		width, height int
	}{
		{models.ImageResize6000, 6000, 90},
		{models.ImageResize2000, 2000, 30},
	} {
		out, err := prepareImage(InputFile{Data: input, MediaType: "image/jpeg"}, tc.profile)
		require.NoError(t, err)
		expected := append(slices.Clone(first), second...)
		require.Equal(t, expected, jpegProfileSegments(out.Data, false))
		decoded, err := jpeg.Decode(bytes.NewReader(out.Data))
		require.NoError(t, err)
		require.Equal(t, image.Rect(0, 0, tc.width, tc.height), decoded.Bounds())
		if tc.profile == models.ImageResize2000 {
			out.Data = append(out.Data, make([]byte, softImageByteLimit)...)
			out = applyCompressionTarget(out)
			require.LessOrEqual(t, len(out.Data), softImageByteLimit)
			require.Equal(t, expected, jpegProfileSegments(out.Data, false))
		}
	}

}

func TestPaletteUsesFullImageAndPreservesPartialAlpha(t *testing.T) {
	// This grid's white dots occupy every position sampled by the old 256px
	// nearest-neighbor thumbnail. The color strip exercises the >256-color path.
	const width, height = 1536, 1024
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	for _, background := range []color.NRGBA{{R: 255, A: 255}, {G: 128, B: 220, A: 128}} {
		img := image.NewNRGBA(image.Rect(0, 0, width, height))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				p := background
				if x%6 == 3 && y%4 == 2 {
					p = white
				}
				if y == 0 {
					p = color.NRGBA{R: uint8(x), G: uint8(x >> 8), B: 200, A: 255}
				}
				img.SetNRGBA(x, y, p)
			}
		}
		var input bytes.Buffer
		enc := png.Encoder{CompressionLevel: png.NoCompression}
		require.NoError(t, enc.Encode(&input, img))
		require.Greater(t, input.Len(), limitsFor(models.ImageResize2000).bytes)
		prepared, err := prepareImage(InputFile{MediaType: "image/png", Data: input.Bytes()}, models.ImageResize2000)
		require.NoError(t, err)
		require.Equal(t, "image/png", prepared.MediaType)
		decoded, err := png.Decode(bytes.NewReader(prepared.Data))
		require.NoError(t, err)
		require.IsType(t, &image.Paletted{}, decoded)
		require.Equal(t, background, color.NRGBAModel.Convert(decoded.At(12, 12)))
		require.Equal(t, white, color.NRGBAModel.Convert(decoded.At(15, 14)))
	}
}

func TestTriangleFiltersColorUnderTransparentPixels(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 12000, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 12000; x++ {
			p := color.NRGBA{R: 255}
			if x%2 != 0 {
				p = color.NRGBA{B: 255, A: 255}
			}
			src.SetNRGBA(x, y, p)
		}
	}
	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, src))
	out, err := prepareImage(InputFile{Data: encoded.Bytes(), MediaType: "image/png"}, models.ImageResize6000)
	require.NoError(t, err)
	decoded, err := png.Decode(bytes.NewReader(out.Data))
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 6000, 24), decoded.Bounds())
	require.Equal(t, color.NRGBA{R: 128, B: 128, A: 128}, color.NRGBAModel.Convert(decoded.At(100, 5)))
}

func BenchmarkResizePortraitAttachment(b *testing.B) {
	src := image.NewNRGBA(image.Rect(0, 0, 6000, 10000))
	for _, profile := range []models.ImageResizeProfile{models.ImageResize6000, models.ImageResize2000} {
		w, h := limitsFor(profile).dimensions(6000, 10000)
		b.Run(string(profile), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if profile == models.ImageResize6000 {
					resizeTriangle(src, w, h)
				} else {
					resizeLanczos(src, w, h)
				}
			}
		})
	}
}

func TestResizePreservesHiddenRGBInIndexedAnd16BitPNG(t *testing.T) {
	bounds := image.Rect(0, 0, 12000, 48)
	indexed := image.NewPaletted(bounds, color.Palette{color.NRGBA{R: 255}, color.NRGBA{B: 255, A: 255}})
	rgba16 := image.NewNRGBA64(bounds)
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			index := uint8(x % 2)
			indexed.SetColorIndex(x, y, index)
			pixel := color.NRGBA64{R: 65535}
			if index == 1 {
				pixel = color.NRGBA64{B: 65535, A: 65535}
			}
			rgba16.SetNRGBA64(x, y, pixel)
		}
	}
	for name, src := range map[string]image.Image{"indexed": indexed, "16bit": rgba16} {
		t.Run(name, func(t *testing.T) {
			var input bytes.Buffer
			require.NoError(t, png.Encode(&input, src))
			for _, profile := range []models.ImageResizeProfile{models.ImageResize6000, models.ImageResize2000} {
				t.Run(string(profile), func(t *testing.T) {
					out, err := prepareImage(InputFile{MediaType: "image/png", Data: input.Bytes()}, profile)
					require.NoError(t, err)
					got, err := png.Decode(bytes.NewReader(out.Data))
					require.NoError(t, err)
					center := color.NRGBA{R: 128, B: 128, A: 128}
					if profile == models.ImageResize2000 {
						edge := color.NRGBA{R: 143, B: 112, A: 112}
						if runtime.GOOS != "darwin" {
							center = color.NRGBA{R: 128, B: 127, A: 127}
							edge = color.NRGBA{R: 134, B: 121, A: 121}
						}
						require.Equal(t, edge, color.NRGBAModel.Convert(got.At(0, 0)))
					}
					require.Equal(t, center, color.NRGBAModel.Convert(got.At(100, 5)))
				})
			}
		})
	}
}

func TestDecodeBudgetTracksPixelFormat(t *testing.T) {
	profile := models.ImageResize6000
	require.True(t, fitsImageDecodeBudget(image.Config{Width: 8400, Height: 8000, ColorModel: color.GrayModel}, profile))
	require.False(t, fitsImageDecodeBudget(image.Config{Width: 8400, Height: 8000, ColorModel: color.NRGBA64Model}, profile))
}

func TestResizedJPEGAcrossProfiles(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 6400, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 6400; x++ {
			c := color.RGBA{R: 255, A: 255}
			if x%8 >= 4 {
				c = color.RGBA{B: 255, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var src bytes.Buffer
	require.NoError(t, jpeg.Encode(&src, img, &jpeg.Options{Quality: 100}))
	prepared, err := prepareImage(InputFile{MediaType: "image/jpeg", Data: src.Bytes()}, models.ImageResize6000)
	require.NoError(t, err)
	decoded, err := jpeg.Decode(bytes.NewReader(prepared.Data))
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 6000, 30), decoded.Bounds())
	require.Equal(t, image.YCbCrSubsampleRatio420, decoded.(*image.YCbCr).SubsampleRatio)
	prepared, err = prepareImage(InputFile{MediaType: "image/jpeg", Data: src.Bytes()}, models.ImageResize2000)
	require.NoError(t, err)
	decoded, err = jpeg.Decode(bytes.NewReader(prepared.Data))
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 2000, 10), decoded.Bounds())
}

func TestResizedGrayscaleJPEG(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 6400, 96))
	for i := range img.Pix {
		img.Pix[i] = byte((i % 6400) / 4)
	}
	var input bytes.Buffer
	require.NoError(t, jpeg.Encode(&input, img, &jpeg.Options{Quality: 95}))
	out, err := prepareImage(InputFile{MediaType: "image/jpeg", Data: input.Bytes()}, models.ImageResize6000)
	require.NoError(t, err)
	decoded, err := jpeg.Decode(bytes.NewReader(out.Data))
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 6000, 90), decoded.Bounds())
	for _, x := range []int{16, 128, 512, 900} {
		want := img.GrayAt(x*6400/6000, 0).Y
		got := color.GrayModel.Convert(decoded.At(x, 0)).(color.Gray).Y
		require.InDelta(t, want, got, 2)
	}
}

func TestOversizeWebPToJPEG(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 1800, 1800))
	_, err := rand.New(rand.NewSource(7)).Read(img.Pix)
	require.NoError(t, err)
	for y := 0; y < 1800; y++ {
		for x := 0; x < 400; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 255, A: 100})
		}
	}
	var input bytes.Buffer
	require.NoError(t, webpcodec.Encode(&input, img, webpcodec.Options{Lossless: true, Exact: true, Method: 1}))
	require.Greater(t, input.Len(), limitsFor(models.ImageResize2000).bytes)
	out, err := prepareImage(InputFile{MediaType: "image/webp", Data: input.Bytes()}, models.ImageResize2000)
	require.NoError(t, err)
	require.Equal(t, "image/jpeg", out.MediaType)
	decoded, err := jpeg.Decode(bytes.NewReader(out.Data))
	require.NoError(t, err)
	require.Equal(t, img.Bounds(), decoded.Bounds())
	pixel := color.NRGBAModel.Convert(decoded.At(100, 100)).(color.NRGBA)
	require.InDelta(t, 254, pixel.R, 2)
	require.InDelta(t, 1, pixel.G, 2)
	require.InDelta(t, 0, pixel.B, 2)
}

func TestResizeFitsRoundedBox(t *testing.T) {
	for _, size := range []image.Point{image.Pt(2100, 16), image.Pt(16, 2100)} {
		var input bytes.Buffer
		require.NoError(t, png.Encode(&input, image.NewGray(image.Rectangle{Max: size})))
		out, err := prepareImage(InputFile{Data: input.Bytes()}, models.ImageResize2000)
		require.NoError(t, err)
		cfg, err := png.DecodeConfig(bytes.NewReader(out.Data))
		require.NoError(t, err)
		want := image.Pt(1969, 15)
		if size.Y > size.X {
			want = image.Pt(15, 1969)
		}
		require.Equal(t, want, image.Pt(cfg.Width, cfg.Height))
	}
}

func TestTriangle16BitPNGPrecision(t *testing.T) {
	gray := image.NewGray16(image.Rect(0, 0, 6400, 16))
	for i := 1; i < len(gray.Pix); i += 2 {
		gray.Pix[i] = 255
	}
	got := resizeTriangle(gray, 6000, 15)
	require.Equal(t, color.NRGBA{R: 1, G: 1, B: 1, A: 255}, got.NRGBAAt(0, 0))
}

func TestDecodePixelLimit(t *testing.T) {
	for _, profile := range []models.ImageResizeProfile{models.ImageResize1568, models.ImageResize2000} {
		require.True(t, fitsImageDecodeBudget(image.Config{Width: 8400, Height: 8000}, profile))
		require.True(t, fitsImageDecodeBudget(image.Config{Width: 16383, Height: 16383}, profile))
		require.False(t, fitsImageDecodeBudget(image.Config{Width: 16384, Height: 16383}, profile))
	}
}

func TestFinalJPEGSizeIncludesProfile(t *testing.T) {
	// Adjust the ICC payload so the quality-1 image is exactly at the limit,
	// then increase it until even the last resize cannot fit.
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	var original bytes.Buffer
	require.NoError(t, jpeg.Encode(&original, img, &jpeg.Options{Quality: 1}))
	limit := limitsFor(models.ImageResize2000).bytes
	const chunkSize = 65519
	segments := (limit - original.Len() + chunkSize + 17) / (chunkSize + 18)
	for _, extra := range []int{0, 1} {
		profile := make([]byte, limit-original.Len()-18*segments+extra)
		copy(profile[16:], "RGB ")
		var metadata bytes.Buffer
		for seq := 1; len(profile) > 0; seq++ {
			n := min(chunkSize, len(profile))
			metadata.Write([]byte{0xff, 0xe2})
			metadata.Write(binary.BigEndian.AppendUint16(nil, uint16(n+16)))
			metadata.WriteString("ICC_PROFILE\x00")
			metadata.Write([]byte{byte(seq), byte(segments)})
			metadata.Write(profile[:n])
			profile = profile[n:]
		}
		data := slices.Insert(slices.Clone(original.Bytes()), 2, metadata.Bytes()...)
		require.Equal(t, limit+extra, len(data))
		out, err := prepareImage(InputFile{Data: data}, models.ImageResize2000)
		if extra == 0 {
			require.NoError(t, err)
			require.Equal(t, "image/jpeg", out.MediaType)
			require.LessOrEqual(t, len(out.Data), limit)
		} else {
			require.ErrorContains(t, err, "exceeds the Claude send limit")
			require.Empty(t, out.Data)
		}
	}
}

func TestPortableLanczosPixels(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		width, height, dstWidth, dstHeight int
		hash                               string
	}{
		{"color gradient", 2400, 64, 201, 37, "1e33452c0014f082b5dbbb1d0d7f4dd025caaffdc69e127553b1927e2f029ab0"},
		{"narrow column", 1, 67, 1, 3, "b856f2900fc234c2e0fd5b06dba3b0461ba1d56b3cd03d71ddd14b6179f07d2e"},
		{"extreme downscale", 18000, 17, 35, 7, "0fd46e11b30f57729e3c1cd6071e5813e2f1add244895b19a8fa379902886e69"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := image.NewNRGBA(image.Rect(0, 0, tc.width, tc.height))
			for y := range tc.height {
				for x := range tc.width {
					img.SetNRGBA(x, y, color.NRGBA{R: byte(x*3 + y), G: byte(y*7 + x/9), B: byte(x*x + y), A: byte(127 + x*17)})
				}
			}
			resized := resizeLanczosPixels(img, tc.dstWidth, tc.dstHeight, true)
			require.Equal(t, tc.hash, fmt.Sprintf("%x", sha256.Sum256(resized.Pix)))
		})
	}
}

func TestResizeTranslucentAnimatedWebP(t *testing.T) {
	for _, tc := range []struct{ name, digest, blended string }{
		{"translucent-frame", "f0e97ff2bd130a32ba2c4f0fcda0ec45ae31dc75db5a5e0372aaa851ee7a1f6b", "cd416d4bd613b30eeba6b45c2ddb540b4aa9d5f437fcf3c96e05f643d31e1c58"},
		{"translucent-offset-frame", "34ddedb5a0f5a08ed37b73ef586b39dfa08927f8888137f212b80289c7611ee9", "7aacf846f2d6c5c0d217efd29a0df38ea74e93b87b272486ba27560fd5cc0e61"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile("testdata/" + tc.name + ".webp")
			require.NoError(t, err)
			for _, mode := range []struct{ name, digest string }{{"replace", tc.digest}, {"blend", tc.blended}} {
				t.Run(mode.name, func(t *testing.T) {
					if mode.name == "blend" {
						frame := bytes.Index(data, []byte("ANMF"))
						require.NotEqual(t, -1, frame)
						data[frame+23] &^= 2
					}
					out, err := prepareImage(InputFile{Data: data}, models.ImageResize6000)
					require.NoError(t, err)
					im, err := webp.Decode(bytes.NewReader(out.Data))
					require.NoError(t, err)
					require.IsType(t, (*image.NRGBA)(nil), im)
					require.Equal(t, mode.digest, fmt.Sprintf("%x", sha256.Sum256(im.(*image.NRGBA).Pix)))
				})
			}
		})
	}
}
