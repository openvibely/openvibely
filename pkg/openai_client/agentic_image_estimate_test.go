package openaiclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"math"
	"strings"
	"testing"
)

func TestEstimateAgenticResponseItemModelVisibleBytes_ImageFastPathMatchesJSONSize(t *testing.T) {
	payload := strings.Repeat("a", 12000)
	for _, detail := range []string{"auto", "original"} {
		t.Run(detail, func(t *testing.T) {
			item := map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "inspect <this> & \"that\""},
					map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + payload, "detail": detail},
				},
			}

			serialized, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			gotSerializedBytes, err := marshalAgenticItemWithoutImagePayload(item)
			if err != nil {
				t.Fatal(err)
			}
			if gotSerializedBytes != len(serialized) {
				t.Fatalf("estimated serialized bytes = %d, want %d", gotSerializedBytes, len(serialized))
			}
			if got, want := estimateAgenticResponseItemModelVisibleBytes(item), legacyAgenticVisibleBytesForTest(t, item); got != want {
				t.Fatalf("visible bytes = %d, want existing estimate %d", got, want)
			}
		})
	}
}

func TestAgenticImageJSONSizeCounterMatchesEncodingJSONForSupportedValues(t *testing.T) {
	values := []any{
		float64(math.SmallestNonzeroFloat64),
		float64(1e-7),
		float64(1e20),
		float32(1e-7),
		float32(1e20),
		int64(-123456789),
		uint64(123456789),
		[]any{"<escaped>", map[string]any{"quote\"key": "line\nfeed\u2028"}},
		string([]byte{0xff, '<', '&'}),
	}
	for index, value := range values {
		item := map[string]any{
			"type": "message",
			"content": []any{map[string]any{
				"type":      "input_image",
				"image_url": "data:image/png;base64,AA==",
				"detail":    "auto",
			}},
			"metadata": value,
		}
		serialized, err := json.Marshal(item)
		if err != nil {
			t.Fatalf("case %d marshal: %v", index, err)
		}
		got, removed, _, _, optimized, ok := agenticJSONSizeReplacingImageURLs(item, 0)
		if !ok || !optimized {
			t.Fatalf("case %d did not use image-size fast path", index)
		}
		if got+removed != len(serialized) {
			t.Fatalf("case %d encoded size = %d, want %d", index, got+removed, len(serialized))
		}
	}
}

func TestEstimateAgenticResponseItemModelVisibleBytes_ValidOriginalImageDetail(t *testing.T) {
	const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC"
	for _, detail := range []string{"auto", "original"} {
		t.Run(detail, func(t *testing.T) {
			item := map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{map[string]any{
					"type":      "input_image",
					"image_url": "data:image/png;base64," + onePixelPNG,
					"detail":    detail,
				}},
			}
			serialized, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			replacementBytes := openAIResizedImageBytesEstimate
			if detail == "original" {
				replacementBytes = approxOpenAIBytesForTokens(1)
			}
			want := len(serialized) - len(onePixelPNG) + replacementBytes
			if got := estimateAgenticResponseItemModelVisibleBytes(item); got != want {
				t.Fatalf("visible bytes = %d, want %d", got, want)
			}
		})
	}
}

func TestEstimateAgenticResponseItemModelVisibleBytes_NonImageInputsMatchJSONSize(t *testing.T) {
	cases := map[string]any{
		"text only": map[string]any{
			"type":    "message",
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": "plain text"}},
		},
		"escaped non-image text": map[string]any{
			"type":    "message",
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": "<tag> & \"quote\" \\ \u2028"}},
		},
		"escaped image URL fallback": map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type":      "input_image",
				"image_url": "data:image/png;base64,abc\"def",
				"detail":    "auto",
			}},
		},
		"malformed image URL": map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type":      "input_image",
				"image_url": "data:image/png;base64,not valid base64",
				"detail":    "original",
			}},
		},
	}
	for name, item := range cases {
		t.Run(name, func(t *testing.T) {
			serialized, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			gotSerializedBytes, err := marshalAgenticItemWithoutImagePayload(item)
			if err != nil {
				t.Fatal(err)
			}
			if gotSerializedBytes != len(serialized) {
				t.Fatalf("estimated serialized bytes = %d, want %d", gotSerializedBytes, len(serialized))
			}
			if got, want := estimateAgenticResponseItemModelVisibleBytes(item), legacyAgenticVisibleBytesForTest(t, item); got != want {
				t.Fatalf("visible bytes = %d, want existing estimate %d", got, want)
			}
		})
	}
}

func TestEstimateAgenticImageRepeatedChecksPreserveBoundariesAndRequestJSON(t *testing.T) {
	const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC"
	for _, detail := range []string{"auto", "original"} {
		t.Run(detail, func(t *testing.T) {
			item := map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": "inspect this image"},
					map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + onePixelPNG, "detail": detail},
				},
			}
			inputItems := []any{item}
			requestBodyBefore, err := json.Marshal(map[string]any{"input": inputItems})
			if err != nil {
				t.Fatal(err)
			}
			wantBytes := legacyAgenticVisibleBytesForTest(t, item)
			wantTokens := approxOpenAITokensFromByteCount(wantBytes)
			wantRequestTokens := estimateCompactionRequestTokens(inputItems, nil, "") + 32

			for round := 1; round <= 8; round++ {
				if got := estimateAgenticResponseItemModelVisibleBytes(item); got != wantBytes {
					t.Fatalf("round %d visible bytes = %d, want %d", round, got, wantBytes)
				}
				if got := estimateInputItemsTokens(inputItems); got != wantTokens {
					t.Fatalf("round %d input tokens = %d, want %d", round, got, wantTokens)
				}
				if !shouldAutoCompactInputItems(inputItems, wantTokens) || shouldAutoCompactInputItems(inputItems, wantTokens+1) {
					t.Fatalf("round %d changed the compaction boundary at %d tokens", round, wantTokens)
				}

				fitsAtBoundary := &AgenticOptions{ContextWindow: wantRequestTokens + 100 + 1024, MaxOutputTokens: 100}
				if err := ensureOpenAIAgenticRequestFits(inputItems, nil, fitsAtBoundary); err != nil {
					t.Fatalf("round %d should fit at exact safe budget: %v", round, err)
				}
				failsBelowBoundary := &AgenticOptions{ContextWindow: wantRequestTokens + 100 + 1023, MaxOutputTokens: 100}
				if err := ensureOpenAIAgenticRequestFits(inputItems, nil, failsBelowBoundary); err == nil {
					t.Fatalf("round %d should exceed a safe budget one token below the estimate", round)
				}
			}

			requestBodyAfter, err := json.Marshal(map[string]any{"input": inputItems})
			if err != nil {
				t.Fatal(err)
			}
			if string(requestBodyAfter) != string(requestBodyBefore) {
				t.Fatal("provider request JSON changed after repeated estimates")
			}
		})
	}
}

func legacyAgenticVisibleBytesForTest(t *testing.T, item any) int {
	t.Helper()
	serialized, err := json.Marshal(item)
	if err != nil || len(serialized) == 0 {
		t.Fatalf("marshal baseline item: %v", err)
	}
	imagePayloadBytes, imageReplacementBytes := agenticImageDataURLEstimateAdjustment(item)
	audioPayloadBytes, audioReplacementBytes := agenticAudioDataURLEstimateAdjustment(item)
	encryptedPayloadBytes, encryptedReplacementBytes := agenticEncryptedFunctionOutputEstimateAdjustment(item)
	visibleBytes := len(serialized) - imagePayloadBytes + imageReplacementBytes - audioPayloadBytes + audioReplacementBytes
	visibleBytes -= encryptedPayloadBytes
	visibleBytes += encryptedReplacementBytes
	if visibleBytes < 0 {
		return 0
	}
	return visibleBytes
}

func BenchmarkEstimateAgenticImageContext(b *testing.B) {
	const originalImageSize = 10 << 20
	const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC"
	imageBytes, err := base64.StdEncoding.DecodeString(onePixelPNG)
	if err != nil {
		b.Fatal(err)
	}
	imageBytes = append(imageBytes, make([]byte, originalImageSize-len(imageBytes))...)
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes)

	for _, detail := range []string{"auto", "original"} {
		item := map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "inspect the attached image"},
				map[string]any{"type": "input_image", "image_url": dataURL, "detail": detail},
			},
		}
		for _, rounds := range []int{1, 4, 8} {
			b.Run(fmt.Sprintf("%s/rounds-%d/estimator/optimized", detail, rounds), func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64(rounds), "estimates/op")
				var total int
				for iteration := 0; iteration < b.N; iteration++ {
					for round := 0; round < rounds; round++ {
						total += estimateAgenticResponseItemModelVisibleBytes(item)
					}
				}
				benchmarkAgenticImageEstimateSink = total
			})
			b.Run(fmt.Sprintf("%s/rounds-%d/estimator/baseline", detail, rounds), func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64(rounds), "estimates/op")
				var total int
				for iteration := 0; iteration < b.N; iteration++ {
					for round := 0; round < rounds; round++ {
						total += legacyAgenticVisibleBytesForBenchmark(item)
					}
				}
				benchmarkAgenticImageEstimateSink = total
			})

			inputItems := make([]any, 0, 1+rounds*2)
			inputItems = append(inputItems, item)
			prefixes := make([][]any, rounds)
			for round := 0; round < rounds; round++ {
				inputItems = append(inputItems,
					map[string]any{"type": "function_call", "call_id": fmt.Sprintf("call-%d", round), "name": "inspect"},
					map[string]any{"type": "function_call_output", "call_id": fmt.Sprintf("call-%d", round), "output": "image inspected"},
				)
				prefixes[round] = inputItems[:len(inputItems)]
			}
			b.Run(fmt.Sprintf("%s/rounds-%d/request-fit-and-compaction/optimized", detail, rounds), func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64(rounds*2), "estimates/op")
				opts := &AgenticOptions{ContextWindow: 100_000_000, MaxOutputTokens: 1024}
				var total int
				for iteration := 0; iteration < b.N; iteration++ {
					for _, history := range prefixes {
						if err := ensureOpenAIAgenticRequestFits(history, nil, opts); err != nil {
							b.Fatal(err)
						}
						if shouldAutoCompactInputItems(history, 100_000_000) {
							total++
						}
					}
				}
				benchmarkAgenticImageEstimateSink = total
			})
			b.Run(fmt.Sprintf("%s/rounds-%d/request-fit-and-compaction/baseline", detail, rounds), func(b *testing.B) {
				b.ReportAllocs()
				b.ReportMetric(float64(rounds*2), "estimates/op")
				var total int
				for iteration := 0; iteration < b.N; iteration++ {
					for _, history := range prefixes {
						total += legacyRequestFitAndCompactionEstimates(history)
					}
				}
				benchmarkAgenticImageEstimateSink = total
			})
		}
	}
}

var benchmarkAgenticImageEstimateSink int

func legacyRequestFitAndCompactionEstimates(inputItems []any) int {
	requestTokens := legacyAgenticInputItemsTokens(inputItems)
	const contextWindow = 100_000_000
	const reservedOutput = 1024
	safeBudget := contextWindow - reservedOutput - max(1024, contextWindow/50)
	if requestTokens+32 > safeBudget {
		return -1
	}
	compactionTokens := legacyAgenticInputItemsTokens(inputItems)
	if compactionTokens >= contextWindow {
		return -2
	}
	return requestTokens + compactionTokens
}

func legacyAgenticInputItemsTokens(inputItems []any) int {
	total := 0
	for _, item := range inputItems {
		total += approxOpenAITokensFromByteCount(legacyAgenticVisibleBytesForBenchmark(item))
	}
	return total
}

func legacyAgenticVisibleBytesForBenchmark(item any) int {
	itemMap, ok := item.(map[string]any)
	if ok {
		switch strings.TrimSpace(stringFromAny(itemMap["type"])) {
		case "reasoning", "context_compaction":
			if encryptedContent := stringFromAny(itemMap["encrypted_content"]); encryptedContent != "" {
				return estimateOpenAIReasoningLength(len(encryptedContent))
			}
		case "compaction":
			return estimateOpenAIReasoningLength(len(stringFromAny(itemMap["encrypted_content"])))
		}
	}
	serialized, err := json.Marshal(item)
	if err != nil || len(serialized) == 0 {
		return 0
	}
	imagePayloadBytes, imageReplacementBytes := 0, 0
	forEachAgenticContentBlock(item, func(block map[string]any) {
		if strings.TrimSpace(stringFromAny(block["type"])) != "input_image" {
			return
		}
		imageURL := firstNonEmpty(stringFromAny(block["image_url"]), stringFromAny(block["url"]))
		payload, ok := parseAgenticBase64DataURL(imageURL, "image/")
		if !ok {
			return
		}
		imagePayloadBytes += len(payload)
		if strings.EqualFold(strings.TrimSpace(stringFromAny(block["detail"])), "original") {
			imageReplacementBytes += legacyAgenticOriginalImageBytes(imageURL)
			return
		}
		imageReplacementBytes += openAIResizedImageBytesEstimate
	})
	audioPayloadBytes, audioReplacementBytes := agenticAudioDataURLEstimateAdjustment(item)
	encryptedPayloadBytes, encryptedReplacementBytes := agenticEncryptedFunctionOutputEstimateAdjustment(item)
	visibleBytes := len(serialized) - imagePayloadBytes + imageReplacementBytes - audioPayloadBytes + audioReplacementBytes
	visibleBytes -= encryptedPayloadBytes
	visibleBytes += encryptedReplacementBytes
	if visibleBytes < 0 {
		return 0
	}
	return visibleBytes
}

func legacyAgenticOriginalImageBytes(imageURL string) int {
	payload, ok := parseAgenticBase64DataURL(imageURL, "image/")
	if !ok {
		return openAIResizedImageBytesEstimate
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return openAIResizedImageBytesEstimate
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(decoded))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return openAIResizedImageBytesEstimate
	}
	patchesWide := (config.Width + openAIOriginalImagePatchSize - 1) / openAIOriginalImagePatchSize
	patchesHigh := (config.Height + openAIOriginalImagePatchSize - 1) / openAIOriginalImagePatchSize
	patchCount := patchesWide * patchesHigh
	if patchCount > openAIOriginalImageMaxPatches {
		patchCount = openAIOriginalImageMaxPatches
	}
	return approxOpenAIBytesForTokens(patchCount)
}
