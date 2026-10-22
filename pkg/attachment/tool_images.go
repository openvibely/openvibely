package attachment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
)

// ToolImages holds image bytes outside textual tool logs. Providers consume a
// result once, then persist ordinary image blocks in their conversation history.
type toolImagesKey struct{}
type toolImages struct {
	sync.Mutex
	images map[string]ToolImage
}
type ToolImage struct {
	Name, MediaType string
	Data            []byte
}

func WithToolImages(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(toolImagesKey{}).(*toolImages); ok {
		return ctx
	}
	return context.WithValue(ctx, toolImagesKey{}, &toolImages{images: make(map[string]ToolImage)})
}
func StoreToolImage(ctx context.Context, image ToolImage) (string, error) {
	store, _ := ctx.Value(toolImagesKey{}).(*toolImages)
	if store == nil {
		return "", fmt.Errorf("image tool results are unavailable for this request")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	key := hex.EncodeToString(id[:])
	store.Lock()
	store.images[key] = image
	store.Unlock()
	out, err := json.Marshal(struct {
		ID   string `json:"tool_image_id"`
		Name string `json:"image"`
	}{key, image.Name})
	return string(out), err
}
func TakeToolImage(ctx context.Context, output string) (ToolImage, bool) {
	var ref struct {
		ID string `json:"tool_image_id"`
	}
	if json.Unmarshal([]byte(output), &ref) != nil || ref.ID == "" || ctx == nil {
		return ToolImage{}, false
	}
	store, _ := ctx.Value(toolImagesKey{}).(*toolImages)
	if store == nil {
		return ToolImage{}, false
	}
	store.Lock()
	defer store.Unlock()
	image, ok := store.images[ref.ID]
	delete(store.images, ref.ID)
	return image, ok
}
