package gemini

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestAttachmentsUseModelResizeProfile(t *testing.T) {
	for _, model := range []string{"gemini-3.8-flash", "gemini-4-neon"} {
		t.Run(model, func(t *testing.T) {
			for _, size := range []image.Point{{X: 3000, Y: 1500}, {X: 100, Y: 50}} {
				var encoded bytes.Buffer
				if err := png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, size.X, size.Y))); err != nil {
					t.Fatal(err)
				}
				original := append([]byte(nil), encoded.Bytes()...)
				path := filepath.Join(t.TempDir(), "image.png")
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
				got, err := convertAttachments(context.Background(), []models.Attachment{{FilePath: path, FileName: "image.png", MediaType: "image/png"}}, model)
				if err != nil {
					t.Fatal(err)
				}
				if len(got) != 1 {
					t.Fatalf("got %d attachments", len(got))
				}
				config, _, err := image.DecodeConfig(bytes.NewReader(got[0].Data))
				if err != nil {
					t.Fatal(err)
				}
				want := size
				if size.X > 2000 {
					want = image.Point{X: 2000, Y: 1000}
				}
				if config.Width != want.X || config.Height != want.Y {
					t.Fatalf("got %dx%d; want %dx%d", config.Width, config.Height, want.X, want.Y)
				}
				saved, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(saved, original) {
					t.Fatal("original attachment changed")
				}
			}
		})
	}
}
