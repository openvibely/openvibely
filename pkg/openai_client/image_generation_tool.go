package openaiclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/openvibely/openvibely/pkg/attachment"
	"github.com/openvibely/openvibely/pkg/filecontent"
	"github.com/openvibely/openvibely/pkg/toolschema"
)

// Use an app-owned name: image_gen.imagegen is reserved by OpenAI and requires
// the model's exact configured schema rather than our custom validation schema.
const imageGenerationToolName = "generate_image"

const imageGenerationDescription = "The `generate_image` tool enables image generation from descriptions and editing of existing images based on specific instructions. Use it when:\n" +
	"\n" +
	"- The user requests an image based on a scene description, such as a diagram, portrait, comic, meme, or any other visual.\n" +
	"- The user wants to modify an attached or previously generated image with specific changes, including adding or removing elements, altering colors, improving quality/resolution, or transforming the style (e.g., cartoon, oil painting).\n" +
	"\n" +
	"Guidelines:\n" +
	"- generate_image needs a few minutes to finish. The app waits for generation to complete and automatically publishes the PNG as a downloadable chat artifact. Once it finishes, display the image using Markdown image syntax with the returned URL. No separate publish_file call is needed.\n" +
	"- Avoid printing the full tool result or its base64 image data; show the image and only small metadata when needed.\n" +
	"- Set `transparent_background` to true when the request calls for a transparent background, including background removal or a cutout; set it to false otherwise. For edits, preserve existing transparency unless the user asks to change it.\n" +
	"- Omit both `referenced_image_paths` and `num_last_images_to_include` when generating a brand new image.\n" +
	"- For edits, use `referenced_image_paths` when every target image has a local file path.\n" +
	"- If you have not seen a local image yet, use `view_image` to inspect it before editing.\n" +
	"- Use `num_last_images_to_include` only when at least one target image has no local file path.\n" +
	"- Set `num_last_images_to_include` to the smallest number of recent conversation images that includes every target image, up to 5.\n" +
	"- Never provide both `referenced_image_paths` and `num_last_images_to_include`.\n" +
	"- If neither mechanism can include every target image, ask the user to attach the missing images again.\n" +
	"- Directly generate the image without reconfirmation or clarification unless required images must be attached again.\n" +
	"- Always use this tool for image editing unless the user explicitly requests otherwise. Do not use Python or other drawing or editing scripts unless specifically instructed."

var imageGenerationParameters = json.RawMessage(`{
 "type":"object",
 "properties":{
  "prompt":{"type":"string","description":"Describe the image to generate or the changes to make to reference images."},
  "transparent_background":{"type":"boolean","default":false,"description":"Whether the output should have a transparent background. Defaults to false."},
  "referenced_image_paths":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":5,"description":"Paths to local PNG, JPEG, or WebP reference images, relative to the working directory or absolute."},
  "num_last_images_to_include":{"type":"integer","minimum":1,"maximum":5,"description":"Use this many of the most recent images in the conversation, including previously generated images."}
 },
 "required":["prompt"],"additionalProperties":false
}`)

func imageGenerationTool() ToolDefinition {
	strict := false
	return ToolDefinition{
		Type: "function", Name: imageGenerationToolName, Strict: &strict, Parameters: imageGenerationParameters,
		Description: imageGenerationDescription,
	}
}

// Image generation inherits the request's existing permission to publish files.
// Re-check at execution time so steering or mode changes can revoke the tool.
func imageGenerationAllowed(opts *AgenticOptions) bool {
	return opts != nil && opts.PublishGeneratedImage != nil && !opts.DisableTools && !opts.SkipDefaultTools &&
		(opts.ToolFilter == nil || opts.ToolFilter("publish_file"))
}

func openAIToolAllowed(opts *AgenticOptions, name string) bool {
	if name == imageGenerationToolName {
		return imageGenerationAllowed(opts)
	}
	return opts.ToolFilter == nil || opts.ToolFilter(name)
}

func (c *Client) runImageGenerationTool(ctx context.Context, opts *AgenticOptions, inputItems []any, input json.RawMessage) (string, bool, error) {
	if !imageGenerationAllowed(opts) {
		return "", true, fmt.Errorf("image generation is not permitted for this request")
	}
	if _, err := toolschema.ValidateArguments(nil, imageGenerationParameters, input); err != nil {
		return "", true, err
	}
	var args struct {
		TransparentBackground bool     `json:"transparent_background"`
		Prompt                string   `json:"prompt"`
		Paths                 []string `json:"referenced_image_paths"`
		Recent                *int     `json:"num_last_images_to_include"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", true, fmt.Errorf("decode image arguments: %w", err)
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return "", true, fmt.Errorf("image prompt is required")
	}
	if args.Paths != nil && args.Recent != nil {
		return "", true, fmt.Errorf("provide only one image reference mode")
	}
	if len(args.Paths) > maxImageReferences || args.Paths != nil && len(args.Paths) == 0 {
		return "", true, fmt.Errorf("provide 1 to 5 reference image paths")
	}
	var references []string
	for _, path := range args.Paths {
		if strings.TrimSpace(path) == "" {
			return "", true, fmt.Errorf("reference image path is empty")
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(opts.WorkDir, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", true, fmt.Errorf("read reference image: %w", err)
		}
		if !info.Mode().IsRegular() {
			return "", true, fmt.Errorf("reference image must be a regular file")
		}
		data, err := filecontent.ReadPath(path, maxImageBytes)
		if err != nil {
			return "", true, fmt.Errorf("read reference image: %w", err)
		}
		mediaType := http.DetectContentType(data)
		if err := validateImageBytes(data, mediaType); err != nil {
			return "", true, err
		}
		references = append(references, "data:"+mediaType+";base64,"+base64.StdEncoding.EncodeToString(data))
	}
	if args.Recent != nil {
		if *args.Recent < 1 || *args.Recent > maxImageReferences {
			return "", true, fmt.Errorf("num_last_images_to_include must be between 1 and 5")
		}
		references = recentImageReferences(inputItems, *args.Recent)
		if len(references) < *args.Recent {
			return "", true, fmt.Errorf("the conversation does not contain %d reference images; supply referenced_image_paths instead", *args.Recent)
		}
	}
	data, err := c.GenerateImage(ctx, args.Prompt, references, args.TransparentBackground)
	if err != nil {
		return "", true, err
	}
	dir, err := os.MkdirTemp("", "openvibely-image-")
	if err != nil {
		return "", true, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "generated-image.png")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", true, err
	}
	published, err := opts.PublishGeneratedImage(ctx, path)
	if err != nil {
		return "", true, fmt.Errorf("publish generated image: %w", err)
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(published), &output); err != nil || output == nil {
		return "", true, fmt.Errorf("image publisher returned invalid artifact metadata")
	}
	imageRef, err := attachment.StoreToolImage(ctx, attachment.ToolImage{Name: "generated-image.png", MediaType: "image/png", Data: data})
	if err != nil {
		return "", true, err
	}
	var ref map[string]any
	if err := json.Unmarshal([]byte(imageRef), &ref); err != nil {
		return "", true, err
	}
	for key, value := range ref {
		output[key] = value
	}
	output["model"] = generatedImageModel
	encoded, err := json.Marshal(output)
	return string(encoded), err != nil, err
}

// Only inspect visible image content, never strings inside tool arguments or
// quoted JSON. Retain chronological ordering when selecting the latest images.
func recentImageReferences(items []any, count int) []string {
	var images []string
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		var content []any
		switch item["type"] {
		case "message":
			content, _ = item["content"].([]any)
		case "function_call_output":
			content, _ = item["output"].([]any)
		default:
			continue
		}
		for _, rawBlock := range content {
			block, ok := rawBlock.(map[string]any)
			if !ok || block["type"] != "input_image" {
				continue
			}
			value, _ := block["image_url"].(string)
			if value == "" {
				continue
			}
			images = append(images, value)
			if len(images) > count {
				images = images[len(images)-count:]
			}
		}
	}
	return images
}
