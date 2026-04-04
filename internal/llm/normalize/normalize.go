package normalize

import (
	"path/filepath"
	"strings"

	"github.com/openvibely/openvibely/internal/llm/attachment"
	"github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/llm/prompt"
)

// NormalizeRequest applies canonical preprocessing before provider dispatch.
func NormalizeRequest(req contracts.AgentRequest) (contracts.AgentRequest, error) {
	n := req
	n.Message = strings.TrimSpace(req.Message)
	if req.WorkDir == "" {
		n.WorkDir = ""
	} else {
		// Best-effort normalization; keep original on error.
		if abs, err := filepath.Abs(req.WorkDir); err == nil {
			n.WorkDir = abs
		}
	}

	if len(req.Attachments) > 0 {
		for i := range n.Attachments {
			n.Attachments[i].MediaType = strings.TrimSpace(n.Attachments[i].MediaType)
			n.Attachments[i].FileName = strings.TrimSpace(n.Attachments[i].FileName)
			if n.Attachments[i].FilePath != "" {
				n.Attachments[i].FilePath = prompt.AttachmentAbsPath(n.Attachments[i])
			}
		}
		prepped, err := attachment.Preprocess(n.Attachments)
		if err != nil {
			return contracts.AgentRequest{}, err
		}
		n.Attachments = prepped
	}

	if n.ChatHistory != nil {
		n.ChatHistory = prompt.LimitChatHistory(n.ChatHistory)
		for i := range n.ChatHistory {
			n.ChatHistory[i].Output = NormalizeReplayOutputText(n.ChatHistory[i].Output)
		}
	}

	n.Message = NormalizeToolCallIDsInText(n.Message)
	return n, nil
}
