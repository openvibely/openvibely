package handler

import (
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

const (
	apiMaxFileSize    = 10 << 20 // 10 MB per file
	apiMaxFilesPerReq = 10       // Max 10 files per API request
)

// allowedFileExtensions maps allowed extensions to their MIME types.
// Includes images, documents, and common programming language files.
var allowedFileExtensions = map[string]string{
	// Images
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	// Documents
	".pdf": "application/pdf",
	".txt": "text/plain",
	".md":  "text/markdown",
	".csv": "text/csv",
	// Code files
	".go":    "text/x-go",
	".py":    "text/x-python",
	".js":    "text/javascript",
	".ts":    "text/typescript",
	".jsx":   "text/javascript",
	".tsx":   "text/typescript",
	".rs":    "text/x-rust",
	".java":  "text/x-java",
	".c":     "text/x-c",
	".cpp":   "text/x-c++",
	".h":     "text/x-c",
	".rb":    "text/x-ruby",
	".php":   "text/x-php",
	".swift": "text/x-swift",
	".kt":    "text/x-kotlin",
	".sh":    "text/x-shellscript",
	".bash":  "text/x-shellscript",
	".sql":   "text/x-sql",
	".html":  "text/html",
	".css":   "text/css",
	".scss":  "text/x-scss",
	".xml":   "text/xml",
	".json":  "application/json",
	".yaml":  "text/x-yaml",
	".yml":   "text/x-yaml",
	".toml":  "text/x-toml",
	".ini":   "text/plain",
	".cfg":   "text/plain",
	".conf":  "text/plain",
	".log":   "text/plain",
	".diff":  "text/x-diff",
	".patch": "text/x-diff",
}

// isAllowedFileType checks if a filename has an allowed extension
func isAllowedFileType(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	_, ok := allowedFileExtensions[ext]
	return ok
}

// ChatMessageAcceptedResponse represents the immediate response when a chat message is accepted for async processing
type ChatMessageAcceptedResponse struct {
	MessageID      string   `json:"message_id" example:"exec123"`
	Status         string   `json:"status" example:"processing"`
	StatusURL      string   `json:"status_url" example:"/api/chat/message/exec123"`
	AttachmentURLs []string `json:"attachment_urls,omitempty" example:"/chat/attachments/abc/download"`
}

// ChatMessageStatusResponse represents the status/result of an async chat message
type ChatMessageStatusResponse struct {
	MessageID  string   `json:"message_id" example:"exec123"`
	Status     string   `json:"status" example:"completed"`
	Response   string   `json:"response,omitempty" example:"Here's the information you requested..."`
	Error      string   `json:"error,omitempty" example:""`
	TaskIDs    []string `json:"task_ids,omitempty" example:"task1,task2"`
	TokensUsed int      `json:"tokens_used,omitempty" example:"150"`
	DurationMs int64    `json:"duration_ms,omitempty" example:"2500"`
}

// APIChatMessage godoc
// @Summary Send a chat message with optional file attachments (async)
// @Description Send a chat message to the AI agent with optional file attachments.
// @Description Returns 201 immediately with a message ID. The AI processes the message asynchronously.
// @Description Poll GET /api/chat/message/{id} to check status and retrieve the response.
// @Description Supported file types: Images (JPG, PNG, GIF, WebP), Documents (PDF, TXT, MD, CSV), Code (Go, Python, JS, TS, Rust, Java, C/C++, Ruby, PHP, Swift, Kotlin, Shell, SQL, HTML, CSS, SCSS, XML, JSON, YAML, TOML, INI, diff/patch)
// @Description Maximum file size: 10 MB per file
// @Description Maximum files per request: 10
// @Tags chat
// @Accept multipart/form-data
// @Produce json
// @Param message formData string true "Chat message text to send to the AI"
// @Param project_id formData string true "Project ID to associate with this chat message"
// @Param attachments formData file false "File attachments (screenshots, images, PDFs, text files)"
// @Success 201 {object} ChatMessageAcceptedResponse "Message accepted for async processing"
// @Failure 400 {object} ErrorResponse "Bad request - missing required fields or invalid input"
// @Failure 404 {object} ErrorResponse "Project not found"
// @Failure 413 {object} ErrorResponse "File too large"
// @Failure 415 {object} ErrorResponse "Unsupported file type"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/chat/message [post]
func (h *Handler) APIChatMessage(c echo.Context) error {
	// Parse multipart form (limit total request body to prevent abuse)
	if err := c.Request().ParseMultipartForm(apiMaxFileSize * int64(apiMaxFilesPerReq)); err != nil {
		// If it's not multipart, try regular form
		if c.Request().Header.Get("Content-Type") == "" || strings.HasPrefix(c.Request().Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			// That's OK, no attachments
		} else {
			log.Printf("[handler] APIChatMessage error parsing form: %v", err)
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "failed to parse request form"})
		}
	}

	message := c.FormValue("message")
	projectID := c.FormValue("project_id")

	// Validate required fields
	if message == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "message is required"})
	}
	if projectID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "project_id is required"})
	}

	log.Printf("[handler] APIChatMessage message=%q project_id=%s", message, projectID)

	// Validate project exists
	project, err := h.projectSvc.GetByID(c.Request().Context(), projectID)
	if err != nil || project == nil {
		log.Printf("[handler] APIChatMessage project not found: %s err=%v", projectID, err)
		return c.JSON(http.StatusNotFound, map[string]string{"error": "project not found"})
	}

	// Auto-select an agent
	agents, err := h.llmConfigRepo.List(c.Request().Context())
	if err != nil || len(agents) == 0 {
		log.Printf("[handler] APIChatMessage no agents available: %v", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "no agents available"})
	}
	complexity := service.AnalyzeComplexity(message)
	result := service.SelectLLM(complexity, agents)
	var agent *models.LLMConfig
	if result != nil {
		agent = result.LLMConfig
	} else {
		agent = &agents[0]
	}

	// Note: Interactive chat intentionally bypasses task worker capacity checks.
	// Task worker limits (per-project/per-model) only gate task execution, not chat.
	// This ensures the chat orchestrator remains responsive even when all task workers are busy.

	// Process file attachments if present
	var savedFiles []apiSavedFile
	if c.Request().MultipartForm != nil && c.Request().MultipartForm.File != nil {
		files := c.Request().MultipartForm.File["attachments"]
		if len(files) > apiMaxFilesPerReq {
			return c.JSON(http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("maximum %d files per request", apiMaxFilesPerReq),
			})
		}

		for _, file := range files {
			// Validate file size
			if file.Size > apiMaxFileSize {
				return c.JSON(http.StatusRequestEntityTooLarge, map[string]string{
					"error": fmt.Sprintf("file %q exceeds %dMB size limit", file.Filename, apiMaxFileSize>>20),
				})
			}

			// Validate file type
			if !isAllowedFileType(file.Filename) {
				return c.JSON(http.StatusUnsupportedMediaType, map[string]string{
					"error": fmt.Sprintf("file type %q not allowed; allowed types: images (jpg, png, gif, webp), documents (pdf, txt, md, csv), code files (go, py, js, ts, etc.)", filepath.Ext(file.Filename)),
				})
			}

			savedFiles = append(savedFiles, apiSavedFile{
				header: file,
			})
		}
	}

	// Create a task record for the chat message
	chatTitle := fmt.Sprintf("Chat %s: %s", time.Now().Format("15:04:05.000"), message[:min(50, len(message))])
	agentID := agent.ID
	task := &models.Task{
		ProjectID: projectID,
		Title:     chatTitle,
		Prompt:    message,
		Status:    models.StatusPending,
		Category:  models.CategoryChat,
		AgentID:   &agentID,
	}
	if err := h.taskRepo.Create(c.Request().Context(), task); err != nil {
		log.Printf("[handler] APIChatMessage error creating task: %v", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to create chat task"})
	}

	// Create execution record
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    message,
	}
	if err := h.execRepo.Create(c.Request().Context(), exec); err != nil {
		log.Printf("[handler] APIChatMessage error creating execution: %v", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to create execution"})
	}

	log.Printf("[handler] APIChatMessage created exec=%s task=%s", exec.ID, task.ID)

	// Broadcast new message event
	if h.chatBroadcaster != nil {
		h.chatBroadcaster.Publish(events.ChatEvent{
			Type:      events.ChatNewMessage,
			ProjectID: projectID,
			ExecID:    exec.ID,
			TaskID:    task.ID,
			Message:   message,
			Source:    "api",
			AgentName: agent.Name,
		})
	}

	// Save attachments to disk and database
	var attachmentURLs []string
	var attachmentContext string
	var imageAttachments []models.Attachment

	if len(savedFiles) > 0 {
		execDir := filepath.Join(uploadsDir, "chat", exec.ID)
		if err := os.MkdirAll(execDir, 0755); err != nil {
			log.Printf("[handler] APIChatMessage error creating exec dir: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to create upload directory"})
		}

		var textContents []string
		for _, sf := range savedFiles {
			filename := filepath.Base(sf.header.Filename)
			// Generate unique filename to avoid collisions
			uniqueName := fmt.Sprintf("%s_%s", generateShortID(), filename)
			destPath := filepath.Join(execDir, uniqueName)

			// Open uploaded file
			src, err := sf.header.Open()
			if err != nil {
				log.Printf("[handler] APIChatMessage error opening file %s: %v", filename, err)
				continue
			}

			// Save to disk
			dst, err := os.Create(destPath)
			if err != nil {
				src.Close()
				log.Printf("[handler] APIChatMessage error creating file %s: %v", filename, err)
				continue
			}
			if _, err := io.Copy(dst, src); err != nil {
				dst.Close()
				src.Close()
				os.Remove(destPath)
				log.Printf("[handler] APIChatMessage error saving file %s: %v", filename, err)
				continue
			}
			dst.Close()
			src.Close()

			// Determine media type from extension
			mediaType := mediaTypeFromExtension(filename)

			// Create database record
			chatAtt := &models.ChatAttachment{
				ExecutionID: exec.ID,
				FileName:    filename,
				FilePath:    destPath,
				MediaType:   mediaType,
				FileSize:    sf.header.Size,
			}
			if err := h.chatAttachmentRepo.Create(c.Request().Context(), chatAtt); err != nil {
				log.Printf("[handler] APIChatMessage error creating attachment record: %v", err)
				continue
			}

			attachmentURLs = append(attachmentURLs, fmt.Sprintf("/chat/attachments/%s/download", chatAtt.ID))

			// Categorize for AI processing
			if isImageFile(filename) {
				imageAttachments = append(imageAttachments, models.Attachment{
					FileName:  filename,
					FilePath:  destPath,
					MediaType: mediaType,
					FileSize:  sf.header.Size,
				})
			} else {
				info, _ := os.Stat(destPath)
				if info != nil && info.Size() <= maxTextAttachmentSize {
					content, readErr := os.ReadFile(destPath)
					if readErr == nil {
						textContents = append(textContents, fmt.Sprintf("\nFile: %s\n```\n%s\n```\n", filename, string(content)))
					}
				} else {
					textContents = append(textContents, fmt.Sprintf("\nFile: %s (attached, %d bytes - too large to include inline)\n", filename, sf.header.Size))
				}
			}

			log.Printf("[handler] APIChatMessage saved attachment file=%s size=%d", filename, sf.header.Size)
		}

		if len(textContents) > 0 {
			attachmentContext = "\n\n--- Attached Files ---\n" + strings.Join(textContents, "")
		}
	}

	// Load chat history for conversation context
	chatHistory, err := h.execRepo.ListChatHistory(c.Request().Context(), projectID, 50)
	if err != nil {
		log.Printf("[handler] APIChatMessage error loading chat history: %v", err)
		chatHistory = []models.Execution{}
	}
	priorHistory := make([]models.Execution, 0, len(chatHistory))
	for _, ch := range chatHistory {
		if ch.ID == exec.ID {
			continue
		}
		if ch.Status == models.ExecRunning {
			continue
		}
		priorHistory = append(priorHistory, ch)
	}

	// Build task context using shared function (same as /chat and Telegram)
	taskContext := h.buildChatContext(c.Request().Context(), projectID, agents)

	// Combine context (including personality if set)
	fullContext := taskContext
	if attachmentContext != "" {
		if fullContext != "" {
			fullContext += "\n"
		}
		fullContext += attachmentContext
	}
	if personalityContext := h.getPersonalityContext(c.Request().Context(), projectID); personalityContext != "" {
		fullContext += personalityContext
	}

	// Get working directory for CLI
	workDir := project.RepoPath

	// Process the LLM call asynchronously using the shared streaming response processor.
	// This ensures consistent behavior (runtime action tools, proper completion ordering,
	// ChatResponseDone broadcast) regardless of whether the message came from web, API, or Telegram.
	// The client receives 201 immediately and polls GET /api/chat/message/:id for the result.
	go h.processStreamingResponse(streamingResponseParams{
		ExecID:           exec.ID,
		TaskID:           task.ID,
		Message:          message,
		Agent:            *agent,
		ChatHistory:      priorHistory,
		ProjectID:        projectID,
		SystemContext:    fullContext,
		WorkDir:          workDir,
		ImageAttachments: imageAttachments,
		IsTaskFollowup:   false,
		ProcessMarkers:   false,
		Surface:          chatcontrol.SurfaceAPI,
	})

	log.Printf("[handler] APIChatMessage exec=%s queued for async processing", exec.ID)

	// Return 201 immediately with the message ID for polling
	resp := ChatMessageAcceptedResponse{
		MessageID: exec.ID,
		Status:    "processing",
		StatusURL: fmt.Sprintf("/api/chat/message/%s", exec.ID),
	}
	if len(attachmentURLs) > 0 {
		resp.AttachmentURLs = attachmentURLs
	}

	return c.JSON(http.StatusCreated, resp)
}

// APIChatMessageStatus godoc
// @Summary Get the status and result of a chat message
// @Description Poll this endpoint to check if an async chat message has completed processing.
// @Description Returns the current status (processing, completed, failed) and the response when available.
// @Tags chat
// @Produce json
// @Param id path string true "Message/execution ID returned from POST /api/chat/message"
// @Success 200 {object} ChatMessageStatusResponse "Message status and response"
// @Failure 404 {object} ErrorResponse "Message not found"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/chat/message/{id} [get]
func (h *Handler) APIChatMessageStatus(c echo.Context) error {
	execID := c.Param("id")
	if execID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "message id is required"})
	}

	exec, err := h.execRepo.GetByID(c.Request().Context(), execID)
	if err != nil {
		log.Printf("[handler] APIChatMessageStatus exec=%s error: %v", execID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to retrieve message status"})
	}
	if exec == nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "message not found"})
	}

	resp := ChatMessageStatusResponse{
		MessageID: exec.ID,
	}

	switch exec.Status {
	case models.ExecCompleted:
		resp.Status = "completed"
		resp.Response = exec.Output
		resp.TokensUsed = exec.TokensUsed
		resp.DurationMs = exec.DurationMs

		// Parse task creation markers from the output to extract task IDs
		taskRequests := parseChatTaskCreations(exec.Output)
		if len(taskRequests) > 0 {
			// The task IDs are embedded in the output after processing;
			// re-extracting from markers gives the titles, not IDs.
			// Instead, look up tasks created from this execution.
			tasks, taskErr := h.taskRepo.ListByProject(c.Request().Context(), exec.TaskID, "")
			if taskErr == nil {
				for _, t := range tasks {
					if t.Category != models.CategoryChat {
						resp.TaskIDs = append(resp.TaskIDs, t.ID)
					}
				}
			}
		}
	case models.ExecFailed:
		resp.Status = "failed"
		resp.Error = exec.ErrorMessage
		resp.DurationMs = exec.DurationMs
	default:
		resp.Status = "processing"
		// Include partial output if available
		if exec.Output != "" {
			resp.Response = exec.Output
		}
	}

	return c.JSON(http.StatusOK, resp)
}

// apiSavedFile holds a multipart file header for deferred processing
type apiSavedFile struct {
	header *multipart.FileHeader
}

// generateShortID creates a short random hex string for unique filenames
func generateShortID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "0000"
	}
	return fmt.Sprintf("%x", b)
}
