package service

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	webpencoder "github.com/gen2brain/webp"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/webp"
)

func TestNewImagesResizeOnSendAcrossModelSwitch(t *testing.T) {
	ctx := compactionTestContext()
	stores := teststores.New(t).Stores
	task := &models.Task{ProjectID: "default", Title: "images", Category: models.CategoryBacklog, Status: models.StatusCompleted}
	require.NoError(t, stores.Tasks.Create(ctx, task))
	dir := t.TempDir()
	path := filepath.Join(dir, "screenshot.png")
	var original bytes.Buffer
	require.NoError(t, png.Encode(&original, image.NewNRGBA(image.Rect(0, 0, 6400, 64))))
	require.NoError(t, os.WriteFile(path, original.Bytes(), 0600))
	files := []models.Attachment{{TaskID: task.ID, FilePath: path, FileName: "screenshot.png", MediaType: "image/png", FileSize: int64(original.Len())}}
	var requests [][]int
	// Match PNG bytes in both data URLs and bare base64 strings.
	encodedPNG := regexp.MustCompile(`iVBORw0KGgo[A-Za-z0-9+/=]*`)
	server := connectedModelServer(t, "http", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var widths []int
		for _, encoded := range encodedPNG.FindAll(body, -1) {
			data, err := base64.StdEncoding.DecodeString(string(encoded))
			if err != nil {
				t.Error(err)
				return
			}
			cfg, err := png.DecodeConfig(bytes.NewReader(data))
			if err != nil {
				t.Error(err)
				return
			}
			widths = append(widths, cfg.Width)
		}
		requests = append(requests, widths)
		provider := models.ProviderOpenAI
		if r.URL.Path == "/v1/messages" {
			provider = models.ProviderAnthropic
		}
		compactionServerReply(w, provider, "ok", "")
	}))
	defer server.Close()
	oldOpenAIBaseURL, oldAnthropicHost := openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost
	openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost = server.URL+"/v1/", server.URL
	defer func() {
		openaiclient.OpenAIAPIBaseURL, anthropicclient.AnthropicAPIHost = oldOpenAIBaseURL, oldAnthropicHost
	}()
	for turn, model := range []string{"gpt-5.3-codex", "claude-opus-5-5", "claude-opus-5-5"} {
		provider := models.ProviderAnthropic
		if turn == 0 {
			provider = models.ProviderOpenAI
		}
		agent := models.LLMConfig{Name: model, Provider: provider, Model: model, AuthMethod: models.AuthMethodAPIKey, APIKey: "test", ContextWindow: 200000, DisableNativeCompaction: true}
		history, err := stores.Executions.ListByTaskChronological(ctx, task.ID)
		require.NoError(t, err)
		exec := models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "inspect"}
		require.NoError(t, stores.Executions.Create(ctx, &exec))
		if turn == 2 {
			require.NoError(t, os.Remove(path))
			files = nil
		}
		svc := NewLLMService(nil, stores.Executions, nil, nil, nil, nil)
		req := llmcontracts.AgentRequest{Ctx: ctx, Agent: agent, ExecID: exec.ID, Operation: llmcontracts.OperationStreaming, Followup: turn > 0, TransportScope: "task:" + task.ID, WorkDir: dir, Message: exec.PromptSent, ChatHistory: history, Attachments: files, DisableNativeCompaction: true}
		res, err := svc.callProviderWithCompaction(svc.providerAdapters[provider], req)
		require.NoError(t, err)
		require.NoError(t, stores.Executions.Complete(ctx, exec.ID, models.ExecCompleted, res.Output, "", 0, 0))
	}
	require.Equal(t, [][]int{{6000}, {6000, 2000}, {6000, 2000}}, requests)
}

func TestAnthropicSendsOtherContentWithOversizedAnimatedWebP(t *testing.T) {
	var pngData bytes.Buffer
	require.NoError(t, png.Encode(&pngData, image.NewGray(image.Rect(0, 0, 4, 4))))
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "still.png")
	require.NoError(t, os.WriteFile(pngPath, pngData.Bytes(), 0600))
	// Use the same two-frame animation with a 2000-square canvas. Its
	// dimensions exceed the token budget but still fit the send limits.
	animation, err := os.ReadFile("testdata/animated-2400x1200.webp")
	require.NoError(t, err)
	binary.LittleEndian.PutUint16(animation[24:26], 1999)
	binary.LittleEndian.PutUint16(animation[27:29], 1999)
	fittingPath := filepath.Join(dir, "fitting.webp")
	require.NoError(t, os.WriteFile(fittingPath, animation, 0600))
	// The logical screen exceeds the decode budget, but each frame fits.
	palette := color.Palette{color.Black, color.White}
	first := image.NewPaletted(image.Rect(0, 0, 64, 64), palette)
	second := image.NewPaletted(first.Bounds(), palette)
	for i := range second.Pix {
		second.Pix[i] = 1
	}
	var gifData bytes.Buffer
	require.NoError(t, gif.EncodeAll(&gifData, &gif.GIF{Image: []*image.Paletted{first, second}, Delay: []int{10, 10}, Config: image.Config{ColorModel: palette, Width: 10000, Height: 8000}}))
	gifPath := filepath.Join(dir, "large-canvas.gif")
	require.NoError(t, os.WriteFile(gifPath, gifData.Bytes(), 0600))
	damagedPath := filepath.Join(dir, "damaged.png")
	require.NoError(t, os.WriteFile(damagedPath, pngData.Bytes()[:16], 0600))
	attachments := []models.Attachment{
		{FileName: "damaged.png", FilePath: damagedPath, MediaType: "image/png"},
		{FileName: "large-canvas.gif", FilePath: gifPath, MediaType: "image/gif"},
		{FileName: "fitting.webp", FilePath: fittingPath, MediaType: "image/webp"},
		{FileName: "animation.webp", FilePath: "testdata/animated-2400x1200.webp", MediaType: "image/webp"},
		{FileName: "still.png", FilePath: pngPath, MediaType: "image/png"},
	}
	var body []byte
	server := connectedModelServer(t, "http", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		compactionServerReply(w, models.ProviderAnthropic, "ok", "")
	}))
	defer server.Close()
	oldHost := anthropicclient.AnthropicAPIHost
	anthropicclient.AnthropicAPIHost = server.URL
	defer func() { anthropicclient.AnthropicAPIHost = oldHost }()
	ctx := compactionTestContext()
	stores := teststores.New(t).Stores
	svc := NewLLMService(nil, stores.Executions, nil, nil, nil, nil)
	req := llmcontracts.AgentRequest{Ctx: ctx, WorkDir: dir, Agent: models.LLMConfig{Provider: models.ProviderAnthropic, Model: "claude-opus-5-5", APIKey: "test", AuthMethod: models.AuthMethodAPIKey}, Operation: llmcontracts.OperationDirect, Message: "Describe both images", Attachments: attachments}
	_, err = svc.callProviderWithCompaction(svc.providerAdapters[models.ProviderAnthropic], req)
	require.NoError(t, err)
	require.Contains(t, string(body), "Describe both images")
	require.Contains(t, string(body), base64.StdEncoding.EncodeToString(gifData.Bytes()))
	require.Contains(t, string(body), "dimensions could not be read from the file header")
	require.Contains(t, string(body), "Save the first frame as PNG or JPEG")
	require.Contains(t, string(body), base64.StdEncoding.EncodeToString(pngData.Bytes()))
	require.Equal(t, 1, bytes.Count(body, []byte(`"media_type":"image/webp"`)))
	require.Contains(t, string(body), base64.StdEncoding.EncodeToString(animation))
}

func TestOpenAISendsResizedAnimatedWebPWithOtherContent(t *testing.T) {
	dir := t.TempDir()
	animation, err := os.ReadFile("testdata/animated-2400x1200.webp")
	require.NoError(t, err)
	binary.LittleEndian.PutUint16(animation[24:26], 6399)
	path := filepath.Join(dir, "animation.webp")
	require.NoError(t, os.WriteFile(path, animation, 0600))
	var pngData bytes.Buffer
	require.NoError(t, png.Encode(&pngData, image.NewGray(image.Rect(0, 0, 16, 16))))
	pngPath := filepath.Join(dir, "still.png")
	require.NoError(t, os.WriteFile(pngPath, pngData.Bytes(), 0600))
	files := []models.Attachment{{FileName: "animation.webp", FilePath: path, MediaType: "image/webp"}, {FileName: "still.png", FilePath: pngPath, MediaType: "image/png"}}
	palette := color.Palette{color.RGBA{R: 255, A: 255}, color.White}
	first := image.NewPaletted(image.Rect(8, 8, 40, 40), palette)
	second := image.NewPaletted(image.Rect(0, 0, 64, 64), palette)
	for i := range second.Pix {
		second.Pix[i] = 1
	}
	var gifData bytes.Buffer
	require.NoError(t, gif.EncodeAll(&gifData, &gif.GIF{Image: []*image.Paletted{first, second}, Delay: []int{10, 10}, Config: image.Config{ColorModel: palette, Width: 64, Height: 64}}))
	gifPath := filepath.Join(dir, "two-frames.gif")
	require.NoError(t, os.WriteFile(gifPath, gifData.Bytes(), 0600))
	files = append(files, models.Attachment{FileName: "two-frames.gif", FilePath: gifPath, MediaType: "image/gif"})
	wide := image.NewNRGBA(image.Rect(0, 0, 6400, 96))
	for y := 0; y < 96; y++ {
		for x := 3200; x < 6400; x++ {
			wide.SetNRGBA(x, y, color.NRGBA{G: 255, A: 255})
		}
	}
	var wideData bytes.Buffer
	require.NoError(t, webpencoder.Encode(&wideData, wide, webpencoder.Options{Lossless: true}))
	widePath := filepath.Join(dir, "wide.webp")
	require.NoError(t, os.WriteFile(widePath, wideData.Bytes(), 0600))
	files = append(files, models.Attachment{FileName: "wide.webp", FilePath: widePath, MediaType: "image/webp"})
	// Keep the IHDR valid and truncate the PNG pixel data.
	brokenPath := filepath.Join(dir, "broken.png")
	require.NoError(t, os.WriteFile(brokenPath, pngData.Bytes()[:41], 0600))
	files = append(files, models.Attachment{FileName: "broken.png", FilePath: brokenPath, MediaType: "image/png"})
	var body []byte
	server := connectedModelServer(t, "http", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		compactionServerReply(w, models.ProviderOpenAI, "ok", "")
	}))
	defer server.Close()
	oldHost := openaiclient.OpenAIAPIBaseURL
	openaiclient.OpenAIAPIBaseURL = server.URL + "/v1/"
	defer func() { openaiclient.OpenAIAPIBaseURL = oldHost }()
	stores := teststores.New(t).Stores
	svc := NewLLMService(nil, stores.Executions, nil, nil, nil, nil)
	req := llmcontracts.AgentRequest{Ctx: compactionTestContext(), WorkDir: dir, Agent: models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-5.5", APIKey: "test", AuthMethod: models.AuthMethodAPIKey}, Operation: llmcontracts.OperationDirect, Message: "Describe both images", Attachments: files}
	_, err = svc.callProviderWithCompaction(svc.providerAdapters[models.ProviderOpenAI], req)
	require.NoError(t, err)
	require.Contains(t, string(body), "Describe both images")
	require.Contains(t, string(body), "data:image/png;base64,"+base64.StdEncoding.EncodeToString(pngData.Bytes()))
	require.Contains(t, string(body), "image content omitted because it could not be processed")
	preparedPNGs := regexp.MustCompile(`data:image/png;base64,([A-Za-z0-9+/=]+)`).FindAllSubmatch(body, -1)
	require.Len(t, preparedPNGs, 2)
	firstFrameData, err := base64.StdEncoding.DecodeString(string(preparedPNGs[1][1]))
	require.NoError(t, err)
	firstFrame, err := png.Decode(bytes.NewReader(firstFrameData))
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 64, 64), firstFrame.Bounds())
	require.Equal(t, color.NRGBA{R: 255, A: 255}, color.NRGBAModel.Convert(firstFrame.At(10, 10)))
	require.Equal(t, color.NRGBA{}, color.NRGBAModel.Convert(firstFrame.At(0, 0)))
	require.NotContains(t, string(body), "data:image/gif;")
	webps := regexp.MustCompile(`data:image/webp;base64,([A-Za-z0-9+/=]+)`).FindAllSubmatch(body, -1)
	require.Len(t, webps, 2)
	resizedData, err := base64.StdEncoding.DecodeString(string(webps[1][1]))
	require.NoError(t, err)
	require.Equal(t, "VP8L", string(resizedData[12:16]))
	resized, err := webp.Decode(bytes.NewReader(resizedData))
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 6000, 90), resized.Bounds())
	require.Equal(t, color.NRGBA{}, color.NRGBAModel.Convert(resized.At(100, 45)))
	require.Equal(t, color.NRGBA{G: 255, A: 255}, color.NRGBAModel.Convert(resized.At(5900, 45)))
	data, err := base64.StdEncoding.DecodeString(string(webps[0][1]))
	require.NoError(t, err)
	require.Equal(t, "VP8L", string(data[12:16]))
	still, err := webp.Decode(bytes.NewReader(data))
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 6000, 1125), still.Bounds())
	// The red pixel at x=2 belongs to the second frame.
	require.Equal(t, color.NRGBA{A: 255}, color.NRGBAModel.Convert(still.At(2, 0)))
}
