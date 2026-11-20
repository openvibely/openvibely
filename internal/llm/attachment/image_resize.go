package attachment

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"slices"
	"strings"

	webpcodec "github.com/gen2brain/webp"
	"github.com/openvibely/openvibely/internal/models"
	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

const maxPackedImageBytes = 64 << 20

type imageLimits struct{ side, block, blocks, bytes int }

func limitsFor(profile models.ImageResizeProfile) imageLimits {
	switch profile {
	case models.ImageResize6000:
		return imageLimits{side: 6000, block: 32, blocks: 10000}
	case models.ImageResize1568:
		return imageLimits{side: 1568, block: 28, blocks: 1568, bytes: 3932160}
	case models.ImageResize2000:
		return imageLimits{side: 2000, block: 28, blocks: 4784, bytes: 3932160}
	default:
		return imageLimits{}
	}
}

func (l imageLimits) fits(w, h int) bool {
	return ((w+l.block-1)/l.block)*((h+l.block-1)/l.block) <= l.blocks
}

func (l imageLimits) dimensions(width, height int) (int, int) {
	if l.block == 32 {
		scale := min(1.0, float64(l.side)/float64(max(width, height)))
		width = max(1, int(math.Round(float64(width)*scale)))
		height = max(1, int(math.Round(float64(height)*scale)))
	} else {
		// Round after each dimension is clamped.
		if width > l.side {
			height = max(1, int(math.Round(float64(height)*float64(l.side)/float64(width))))
			width = l.side
		}
		if height > l.side {
			width = max(1, int(math.Round(float64(width)*float64(l.side)/float64(height))))
			height = l.side
		}
	}
	if l.fits(width, height) {
		return width, height
	}
	if l.block == 32 {
		// Round down to a whole patch grid after scaling by area.
		w, h := float64(width), float64(height)
		patch := float64(l.block)
		scale := math.Sqrt(patch * patch * float64(l.blocks) / w / h)
		x, y := w*scale/patch, h*scale/patch
		scale *= min(math.Floor(x)/x, math.Floor(y)/y)
		return max(1, int(w*scale)), max(1, int(h*scale))
	}
	// Find the largest width that fits, rounding the height to the nearest even integer at ties.
	if height > width {
		h, w := l.dimensions(height, width)
		return w, h
	}
	ratio := float64(width) / float64(height)
	scaledHeight := func(w int) int { return max(1, int(math.RoundToEven(float64(w)/ratio))) }
	low, high := 1, width
	for low+1 < high {
		mid := (low + high) / 2
		if l.fits(mid, scaledHeight(mid)) {
			low = mid
		} else {
			high = mid
		}
	}
	return low, scaledHeight(low)
}

func prepareImage(input InputFile, profile models.ImageResizeProfile) (InputFile, error) {
	limits := limitsFor(profile)
	if limits.side == 0 {
		return input, nil
	}
	// Keep the existing static WebP decoder for normal attachment handling.
	// The animation codec also registers itself with the Go image package.
	isWebP := len(input.Data) >= 12 && string(input.Data[:4]) == "RIFF" && string(input.Data[8:12]) == "WEBP"
	var config image.Config
	var format string
	var err error
	if isWebP {
		config, err = webp.DecodeConfig(bytes.NewReader(input.Data))
		format = "webp"
	} else {
		config, format, err = image.DecodeConfig(bytes.NewReader(input.Data))
	}
	if err != nil {
		if profile == models.ImageResize1568 || profile == models.ImageResize2000 {
			input.MediaType = "text/plain"
			input.Data = []byte("[Image could not be processed: Unable to resize image — image processing is unavailable and dimensions could not be read from the file header. Please convert the image to PNG, JPEG, GIF, or WebP.]")
			return input, nil
		}
		return input, err
	}
	input.MediaType = "image/" + format
	prepared, err := prepareImageWithConfig(input, profile, limits, config, format)
	if profile == models.ImageResize6000 {
		return prepared, err
	}
	if err != nil {
		if config.Width > limits.side || config.Height > limits.side || len(input.Data) > limits.bytes {
			input.MediaType = "text/plain"
			input.Data = []byte(fmt.Sprintf("[Image could not be processed: Unable to resize image — processing failed and the original %dx%dpx image exceeds this model's send limits. Convert it to PNG or JPEG and try again.]", config.Width, config.Height))
			return input, nil
		}
		prepared = input
	}
	prepared = applyCompressionTarget(prepared)
	if len(prepared.Data) > limits.bytes {
		return InputFile{}, fmt.Errorf("image base64 size (%d bytes) exceeds the Claude send limit (%d bytes); resize the image before sending", base64.StdEncoding.EncodedLen(len(prepared.Data)), base64.StdEncoding.EncodedLen(limits.bytes))
	}
	return prepared, nil
}

func prepareImageWithConfig(input InputFile, profile models.ImageResizeProfile, limits imageLimits, config image.Config, format string) (InputFile, error) {
	screen := config
	isWebP := format == "webp"
	sizeByFirstFrame := format == "gif" && (profile == models.ImageResize1568 || profile == models.ImageResize2000)
	if sizeByFirstFrame {
		frame, err := gifFirstFrameBounds(input.Data, config)
		if err != nil {
			return input, err
		}
		config.Width, config.Height = frame.Dx(), frame.Dy()
	}
	w, h := limits.dimensions(config.Width, config.Height)
	// Fit the original aspect ratio inside the rounded bounding box.
	if profile != models.ImageResize6000 {
		scale := min(float64(w)/float64(config.Width), float64(h)/float64(config.Height))
		w = max(1, int(math.Round(float64(config.Width)*scale)))
		h = max(1, int(math.Round(float64(config.Height)*scale)))
	}
	resized := w != config.Width || h != config.Height
	animatedWebP := format == "webp" && len(input.Data) > 20 && string(input.Data[12:16]) == "VP8X" && input.Data[20]&0x02 != 0
	renderGIFCanvas := format == "gif" && profile == models.ImageResize6000
	if !renderGIFCanvas && !resized && (limits.bytes == 0 || len(input.Data) <= limits.bytes) {
		if profile == models.ImageResize6000 {
			// Decode the pixels to validate the file before passing through its original bytes.
			if animatedWebP {
				_, err := webpcodec.Decode(bytes.NewReader(input.Data))
				return input, err
			}
			_, err := decodeAttachmentImage(input.Data, format)
			return input, err
		}
		return input, nil
	}
	if (profile == models.ImageResize1568 || profile == models.ImageResize2000) && animatedWebP {
		// A resize requested only by the image-token budget is optional
		// when decoding fails. The original still fits the send limits.
		if config.Width <= limits.side && config.Height <= limits.side && len(input.Data) <= limits.bytes {
			return input, nil
		}
		input.MediaType = "text/plain"
		input.Data = []byte(fmt.Sprintf("The %d×%d animated WebP exceeds this model's image limits. Its first frame could not be decoded. Save the first frame as PNG or JPEG and try again.", config.Width, config.Height))
		return input, nil
	}
	// Bound decoded memory independently of the upload's compressed byte limit.
	if !fitsImageDecodeBudget(config, profile) {
		return input, fmt.Errorf("image dimensions exceed decoding limit")
	}
	if profile == models.ImageResize6000 && isWebP {
		return resizeLosslessWebP(input, w, h, animatedWebP)
	}
	src, err := decodeAttachmentImage(input.Data, format)
	if err != nil {
		return input, err
	}
	if isWebP {
		src = packedWebP(input.Data, src)
	}
	frame := src.Bounds()
	if resized && sizeByFirstFrame && frame.Min != (image.Point{}) && fitsImageDecodeBudget(screen, profile) {
		if displayed := nativeGIFFrame(input.Data, config.Width, config.Height); displayed != nil {
			src = displayed
		}
	}
	if renderGIFCanvas {
		canvas := image.NewNRGBA(image.Rect(0, 0, config.Width, config.Height))
		draw.Draw(canvas, frame, src, frame.Min, draw.Src)
		src = canvas
	}
	original := src
	if resized {
		if profile == models.ImageResize6000 {
			src = resizeTriangle(src, w, h)
			if format == "jpeg" {
				data, err := encodeJPEG(src, 85)
				if err != nil {
					return input, err
				}
				input.Data = slices.Insert(data, 2, jpegMetadataWithFullEXIF(input.Data)...)
				return input, nil
			}
		} else {
			src = resizeRaster(src, w, h)
		}
	}
	var out bytes.Buffer
	if isWebP && resized {
		if err := webpcodec.Encode(&out, src, webpcodec.Options{Quality: 80, Method: 4}); err != nil {
			return input, err
		}
		encoded := preserveWebPMetadata(input.Data, out.Bytes(), w, h, false)
		if len(encoded) <= limits.bytes {
			input.Data, input.MediaType = encoded, "image/webp"
			return input, nil
		}
	}
	if format != "jpeg" && !isWebP {
		if err := png.Encode(&out, src); err != nil {
			return input, err
		}
		encoded := out.Bytes()
		if format == "png" {
			encoded = preservePNGMetadata(input.Data, encoded, profile == models.ImageResize6000)
		}
		if format == "png" && limits.bytes > 0 && (!resized || len(encoded) > limits.bytes) {
			indexed := buildPNGPalette(src)
			var palettePNG bytes.Buffer
			enc := png.Encoder{CompressionLevel: png.BestCompression}
			if err := enc.Encode(&palettePNG, indexed); err != nil {
				return input, err
			}
			paletteData := preservePNGMetadata(input.Data, palettePNG.Bytes(), false)
			if len(paletteData) <= limits.bytes {
				prepared := InputFile{Name: input.Name, MediaType: "image/png", Data: paletteData}
				return prepared, nil
			}
		}
		prepared := input
		prepared.MediaType = "image/png"
		prepared.Data = encoded
		if limits.bytes == 0 || len(encoded) <= limits.bytes {
			return prepared, nil
		}
	}
	metadata := metadataForJPEG(input.Data, format)
	src = discardAlpha(src)
	for _, quality := range []int{80, 60, 40, 20} {
		data, err := encodeJPEG(src, quality)
		if err != nil {
			return input, err
		}
		if len(data)+len(metadata) <= limits.bytes {
			input.MediaType = "image/jpeg"
			input.Data = slices.Insert(data, 2, metadata...)
			return input, nil
		}
	}
	lastWidth := min(w, 1000)
	lastHeight := max(1, int(math.Round(float64(h)*float64(lastWidth)/float64(w))))
	jpegRetryScale := resizeLanczos
	if format == "jpeg" || isWebP {
		jpegRetryScale = resizeRaster
	}
	reduced := jpegRetryScale(original, lastWidth, lastHeight)
	data, err := encodeJPEG(discardAlpha(reduced), 20)
	if err != nil {
		return input, err
	}
	input.Data = slices.Insert(data, 2, metadata...)
	input.MediaType = "image/jpeg"
	return input, nil
}

// Bound decoded storage and the temporary scaling row separately.
func fitsImageDecodeBudget(config image.Config, profile models.ImageResizeProfile) bool {
	if config.Width <= 0 || config.Height <= 0 {
		return false
	}
	if profile != models.ImageResize6000 {
		const maxPixels = 0x3fff * 0x3fff
		return config.Width <= maxPixels/config.Height
	}
	bytesPerPixel := 4
	switch config.ColorModel {
	case color.GrayModel:
		bytesPerPixel = 1
	case color.Gray16Model:
		bytesPerPixel = 2
	case color.RGBA64Model, color.NRGBA64Model:
		bytesPerPixel = 8
	}
	const budget = 512 << 20
	return config.Width <= budget/20 && config.Width <= budget/bytesPerPixel/config.Height
}

const softImageByteLimit = 500 * 1024

func applyCompressionTarget(prepared InputFile) InputFile {
	if len(prepared.Data) <= softImageByteLimit || prepared.MediaType == "text/plain" {
		return prepared
	}
	format := strings.TrimPrefix(prepared.MediaType, "image/")
	decoded, err := decodeAttachmentImage(prepared.Data, format)
	if err != nil {
		return prepared
	}
	if format == "webp" {
		decoded = packedWebP(prepared.Data, decoded)
	}
	return compressToJPEGTarget(prepared, decoded, format)
}

// Encode each quality trial from the same prepared image.
func compressToJPEGTarget(input InputFile, src image.Image, format string) InputFile {
	metadata := metadataForJPEG(input.Data, format)
	best := input
	src = discardAlpha(src)
	tryQuality := func(quality int) (fits bool, err error) {
		data, err := encodeJPEG(src, quality)
		if err != nil {
			return false, err
		}
		size := len(data) + len(metadata)
		fits = size <= softImageByteLimit
		if fits || size < len(best.Data) {
			best.MediaType = "image/jpeg"
			best.Data = slices.Insert(data, 2, metadata...)
		}
		return fits, nil
	}
	low, high := 1, 90
	if format != "jpeg" {
		fits, err := tryQuality(high)
		if fits || err != nil {
			return best
		}
		high--
	}
	for attempt := 0; attempt < 5 && low <= high; attempt++ {
		quality := (low + high) / 2
		fits, err := tryQuality(quality)
		if err != nil {
			break
		}
		if fits {
			low = quality + 1
		} else {
			high = quality - 1
		}
	}
	return best
}

func encodeJPEG(src image.Image, quality int) ([]byte, error) {
	var out bytes.Buffer
	if err := jpeg.Encode(&out, src, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// discardAlpha operates on a privately decoded image. Preserve the stored RGB
// channels, including colors under fully transparent pixels, for JPEG output.
func discardAlpha(src image.Image) image.Image {
	switch img := src.(type) {
	case *image.NRGBA:
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 0xff
		}
	case *image.NRGBA64:
		for i := 6; i < len(img.Pix); i += 8 {
			img.Pix[i], img.Pix[i+1] = 0xff, 0xff
		}
	case *image.Paletted:
		for i, c := range img.Palette {
			if pixel, ok := c.(color.NRGBA); ok {
				pixel.A = 0xff
				img.Palette[i] = pixel
			}
		}
	case *image.NYCbCrA:
		return &img.YCbCr
	}
	return src
}

func decodeAttachmentImage(data []byte, format string) (image.Image, error) {
	if format == "webp" {
		return webp.Decode(bytes.NewReader(data))
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}
