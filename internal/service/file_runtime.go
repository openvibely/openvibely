package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/pkg/attachment"
	"github.com/openvibely/openvibely/pkg/filecontent"
)

func (s *LLMService) fileRuntime(req llmcontracts.AgentRequest) *llmcontracts.RuntimeTools {
	if !supportsRuntimeChatActionTools(req.Agent) || req.DisableTools || req.LifecycleHookCall || req.ExecID == "" || req.ProjectID == "" || s.attachmentRepo == nil || llmcontracts.RuntimeSkipDefaultTools(llmcontracts.RuntimeToolsFromContext(req.Ctx)) {
		return nil
	}
	if req.WorkDir == "" {
		req.WorkDir = "."
	}
	if req.AgentDefinition != nil && (req.AgentDefinition.ToolConfig.SkipDefaultTools || !AgentAllowsTool(req.AgentDefinition, "Read")) {
		return nil
	}
	defs := []llmcontracts.RuntimeToolDefinition{{Name: "view_image", Description: "Inspect a local PNG, JPEG, GIF, or WebP image visually. Use this after rendering document pages, slides, sheets, or other visual output with skill scripts. Returns image content to the model; does not publish the image to the user.", Access: llmcontracts.RuntimeToolAccessRead, Parameters: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"],"additionalProperties":false}`)}}
	canPublish := req.ChatMode != models.ChatModePlan && AgentAllowsTool(req.AgentDefinition, "Write")
	if canPublish {
		defs = append(defs, llmcontracts.RuntimeToolDefinition{Name: "publish_file", Description: "Publish an existing generated file as a durable downloadable chat artifact. Returns its file URL; include a Markdown link to it in the answer. Files are not automatically included in later model context. For interactive HTML use display=canvas; scripts run in an isolated preview.", Access: llmcontracts.RuntimeToolAccessWrite, Parameters: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"},"display":{"type":"string","enum":["","canvas"]}},"required":["file_path"],"additionalProperties":false}`)})
	}
	return &llmcontracts.RuntimeTools{Definitions: defs, Executor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		if name != "publish_file" && name != "view_image" {
			return "", false, false, nil
		}
		if name == "publish_file" && !canPublish {
			return "", true, true, fmt.Errorf("publishing is not permitted for this request")
		}
		var args struct {
			FilePath string `json:"file_path"`
			Display  string `json:"display"`
		}
		if err := json.Unmarshal(input, &args); err != nil {
			return "", true, true, err
		}
		if args.FilePath == "" {
			return "", true, true, fmt.Errorf("file_path is required")
		}
		path := args.FilePath
		if !filepath.IsAbs(path) {
			path = filepath.Join(req.WorkDir, path)
		}
		limit := filecontent.UploadLimit(path)
		if name == "view_image" {
			limit = 10 << 20
		}
		data, err := filecontent.ReadPath(path, limit)
		if err != nil {
			return "", true, true, err
		}
		if name == "view_image" {
			mediaType := http.DetectContentType(data)
			switch mediaType {
			case "image/png", "image/jpeg", "image/gif", "image/webp":
			default:
				return "", true, true, fmt.Errorf("view_image requires a PNG, JPEG, GIF, or WebP; render or convert this file first")
			}
			output, err := attachment.StoreToolImage(ctx, attachment.ToolImage{Name: filepath.Base(path), MediaType: mediaType, Data: data})
			return output, true, err != nil, err
		}
		if args.Display != "" && args.Display != "canvas" {
			return "", true, true, fmt.Errorf("invalid display")
		}
		ext := strings.ToLower(filepath.Ext(path))
		if args.Display == "canvas" && ext != ".html" && ext != ".htm" {
			return "", true, true, fmt.Errorf("canvas requires HTML")
		}
		root := "uploads"
		if s.taskSvc != nil && s.taskSvc.uploadsDir != "" {
			root = s.taskSvc.uploadsDir
		}
		root, err = filepath.Abs(filepath.Join(root, "generated"))
		if err != nil {
			return "", true, true, err
		}
		if err = os.MkdirAll(root, 0700); err != nil {
			return "", true, true, err
		}
		dir, err := os.MkdirTemp(root, "file-")
		if err != nil {
			return "", true, true, err
		}
		stored := filepath.Join(dir, filepath.Base(path))
		if err = os.WriteFile(stored, data, 0600); err != nil {
			os.RemoveAll(dir)
			return "", true, true, err
		}
		f := models.GeneratedFile{ProjectID: req.ProjectID, ExecutionID: req.ExecID, FileName: filepath.Base(path), FilePath: stored, MediaType: filecontent.FileMediaType(path, data), FileSize: int64(len(data)), Display: args.Display}
		if err = s.attachmentRepo.CreateGeneratedFile(ctx, &f); err != nil {
			os.RemoveAll(dir)
			return "", true, true, err
		}
		if s.broadcaster != nil && s.execRepo != nil {
			if execution, err := s.execRepo.GetByID(ctx, req.ExecID); err == nil && execution != nil {
				s.broadcaster.Publish(events.TaskEvent{Type: events.TaskFilesChanged, ProjectID: req.ProjectID, TaskID: execution.TaskID, ExecID: req.ExecID})
			}
		}
		out, _ := json.Marshal(map[string]any{"id": f.ID, "name": f.FileName, "url": "/files/" + f.ID + "?project_id=" + url.QueryEscape(f.ProjectID), "display": f.Display})
		return string(out), true, false, nil
	}}
}
