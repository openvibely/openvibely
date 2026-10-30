package components

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestChatRevisionCompatibility(t *testing.T) {
	for _, tc := range []struct{ content, want string }{
		{"", "0-cbf29ce484222325"},
		{"hello", "5-a430d84680aabd0b"},
		{"é世界🙂", "c-7e1d530e0f60dbc9"},
		{"\x00ÿ", "3-d6b5f71869db7511"},
	} {
		for range 2 {
			if got := chatContentRevision(tc.content); got != tc.want {
				t.Errorf("content %q: got %q, want %q", tc.content, got, tc.want)
			}
		}
	}
	exec := models.Execution{ID: "exec-1", Status: models.ExecCompleted, PromptSent: "é世界🙂", Output: "output", ErrorMessage: "error", IsFollowup: true}
	input := models.ThreadInput{ID: "input-1", InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, ExpectedTurnID: "turn-1", Content: "é世界🙂", AttachmentSessionID: "attachment-1"}
	for _, tc := range []struct {
		name     string
		revision func() string
		want     string
	}{
		{"empty execution", func() string { return ChatExecutionRevision(models.Execution{}) }, "60dede5b1167f8ea"},
		{"execution", func() string { return ChatExecutionRevision(exec) }, "165cdef6ffe4e470"},
		{"empty transcript", func() string { return ChatTranscriptRevision(nil, nil, "") }, "0-af64724c8602eb6e"},
		{"transcript", func() string {
			return ChatTranscriptRevision([]models.Execution{exec}, []models.ThreadInput{input}, "metadata")
		}, "2-a90aab559d2cbd47"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for range 2 {
				if got := tc.revision(); got != tc.want {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
			}
		})
	}
}

func TestChatRevisionFieldChanges(t *testing.T) {
	base := models.Execution{ID: "exec", Status: models.ExecRunning, PromptSent: "prompt", Output: "output", ErrorMessage: "error"}
	for name, change := range map[string]func(*models.Execution){
		"id":          func(e *models.Execution) { e.ID += "!" },
		"status":      func(e *models.Execution) { e.Status = models.ExecCompleted },
		"prompt":      func(e *models.Execution) { e.PromptSent += "!" },
		"output":      func(e *models.Execution) { e.Output += "!" },
		"error":       func(e *models.Execution) { e.ErrorMessage += "!" },
		"followup":    func(e *models.Execution) { e.IsFollowup = true },
		"author id":   func(e *models.Execution) { e.AuthorUserID = "user-1" },
		"author name": func(e *models.Execution) { e.AuthorName = "testuser1" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			change(&changed)
			if ChatExecutionRevision(base) == ChatExecutionRevision(changed) {
				t.Fatal("changed execution field must invalidate revision")
			}
			if ChatTranscriptRevision([]models.Execution{base}, nil, "") == ChatTranscriptRevision([]models.Execution{changed}, nil, "") {
				t.Fatal("changed execution field must invalidate transcript")
			}
		})
	}
	input := models.ThreadInput{ID: "input", InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, ExpectedTurnID: "turn", Content: "content", AttachmentSessionID: "attachment"}
	for name, change := range map[string]func(*models.ThreadInput){
		"input id":      func(i *models.ThreadInput) { i.ID += "!" },
		"input mode":    func(i *models.ThreadInput) { i.InputMode = models.ThreadInputModeSteering },
		"input status":  func(i *models.ThreadInput) { i.InputStatus = models.ThreadInputApplied },
		"expected turn": func(i *models.ThreadInput) { i.ExpectedTurnID += "!" },
		"content":       func(i *models.ThreadInput) { i.Content += "!" },
		"attachment":    func(i *models.ThreadInput) { i.AttachmentSessionID += "!" },
		"author id":     func(i *models.ThreadInput) { i.AuthorUserID = "user-1" },
		"author name":   func(i *models.ThreadInput) { i.AuthorName = "testuser1" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := input
			change(&changed)
			if ChatTranscriptRevision(nil, []models.ThreadInput{input}, "") == ChatTranscriptRevision(nil, []models.ThreadInput{changed}, "") {
				t.Fatal("changed pending input field must invalidate transcript")
			}
		})
	}
	if ChatTranscriptRevision(nil, nil, "running") == ChatTranscriptRevision(nil, nil, "completed") {
		t.Fatal("changed metadata must invalidate transcript")
	}
}

func TestChatRevisionFieldBoundaries(t *testing.T) {
	if ChatExecutionRevision(models.Execution{PromptSent: "ab", Output: "c"}) == ChatExecutionRevision(models.Execution{PromptSent: "a", Output: "bc"}) {
		t.Fatal("execution fields must retain boundaries")
	}
	if ChatTranscriptRevision(nil, []models.ThreadInput{{Content: "ab", AttachmentSessionID: "c"}}, "") == ChatTranscriptRevision(nil, []models.ThreadInput{{Content: "a", AttachmentSessionID: "bc"}}, "") {
		t.Fatal("pending input fields must retain boundaries")
	}
}
