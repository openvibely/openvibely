package attachment

import (
	"bytes"
	"encoding/hex"
	"image"
	"image/jpeg"
	"slices"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestPrepareJPEGPreservesOrientation(t *testing.T) {
	for _, tc := range []struct {
		name, exif       string
		width, wantWidth int
		padding          int
		value            byte
	}{
		{"resize little endian", "49492a0008000000010012010300010000000600000000000000", 3200, 2000, 0, 6},
		{"recompress big endian mirrored", "4d4d002a00000008000101120003000000010007000000000000", 120, 120, 4 << 20, 7},
		{"soft compression target", "49492a0008000000010012010300010000000600000000000000", 120, 120, softImageByteLimit, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var src bytes.Buffer
			require.NoError(t, jpeg.Encode(&src, image.NewRGBA(image.Rect(0, 0, tc.width, 64)), nil))
			tiff, err := hex.DecodeString(tc.exif)
			require.NoError(t, err)
			app1 := append([]byte("\xff\xe1\x00\x22Exif\x00\x00"), tiff...)
			input := slices.Insert(src.Bytes(), 2, app1...)
			input = append(input, make([]byte, tc.padding)...)
			prepared, err := prepareImage(InputFile{MediaType: "image/jpeg", Data: input}, models.ImageResize2000)
			require.NoError(t, err)
			decoded, err := jpeg.Decode(bytes.NewReader(prepared.Data))
			require.NoError(t, err)
			require.Equal(t, tc.wantWidth, decoded.Bounds().Dx())
			require.Equal(t, "image/jpeg", prepared.MediaType)
			expected, err := hex.DecodeString("ffe100224578696600004d4d002a00000008000101120003000000010000000000000000")
			require.NoError(t, err)
			expected[29] = tc.value
			require.True(t, bytes.HasPrefix(prepared.Data[2:], expected))
			require.LessOrEqual(t, len(prepared.Data), limitsFor(models.ImageResize2000).bytes)
		})
	}
}

func TestJPEGOrientationIgnoresMalformedMetadata(t *testing.T) {
	for _, header := range []string{"ffe10000", "ffe1ffff", "ffe1001045786966000049492a00ffffffff", "ffe100124578696600004d4d002a00000008ffff"} {
		data, err := hex.DecodeString("ffd8" + header)
		require.NoError(t, err)
		require.Empty(t, jpegOrientationSegment(data))
	}
}

func TestResizedJPEGKeepsFullEXIF(t *testing.T) {
	// Little-endian TIFF with orientation 6 and an IFD DateTime field.
	app1, err := hex.DecodeString("ffe1004245786966000049492a0008000000020012010300010000000600000032010200140000002600000000000000")
	require.NoError(t, err)
	app1 = append(app1, "2026:10:05 09:30:00\x00"...)
	var src bytes.Buffer
	require.NoError(t, jpeg.Encode(&src, image.NewGray(image.Rect(0, 0, 6400, 16)), nil))
	out, err := prepareImage(InputFile{Data: slices.Insert(src.Bytes(), 2, app1...)}, models.ImageResize6000)
	require.NoError(t, err)
	require.True(t, bytes.Contains(out.Data, app1))
}
