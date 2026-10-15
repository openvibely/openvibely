package attachment

import (
	"context"
	"testing"
)

func TestToolImagesAreRequestScopedAndConsumed(t *testing.T) {
	first := WithToolImages(context.Background())
	second := WithToolImages(context.Background())
	output, err := StoreToolImage(first, ToolImage{Name: "page.png", MediaType: "image/png", Data: []byte("bytes")})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := TakeToolImage(second, output); ok {
		t.Fatal("another request accessed image bytes")
	}
	image, ok := TakeToolImage(first, output)
	if !ok || string(image.Data) != "bytes" {
		t.Fatal("image bytes missing")
	}
	if _, ok := TakeToolImage(first, output); ok {
		t.Fatal("image bytes retained after consumption")
	}
	if _, err := StoreToolImage(context.Background(), image); err == nil {
		t.Fatal("missing result support must be explicit")
	}
}
