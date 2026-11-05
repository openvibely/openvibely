package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestChatTaskTitlePreview(t *testing.T) {
	cases := []struct {
		name, message, preview string
	}{
		{"three-byte crossing", strings.Repeat("界", 17), strings.Repeat("界", 16)},
		{"ASCII", strings.Repeat("a", 60), strings.Repeat("a", 50)},
		{"two-byte crossing", strings.Repeat("a", 49) + "é", strings.Repeat("a", 49)},
		{"four-byte crossing", strings.Repeat("a", 48) + "😀", strings.Repeat("a", 48)},
		{"exact boundary", strings.Repeat("a", 47) + "界", strings.Repeat("a", 47) + "界"},
		{"complete boundary before more text", strings.Repeat("a", 47) + "界more", strings.Repeat("a", 47) + "界"},
		{"short", "Hello 界", "Hello 界"},
	}
	for _, path := range []string{"browser", "API", "queued"} {
		for _, tc := range cases {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				h, e, configs := setupTestHandler(t)
				h.workerSvc = nil
				ctx := context.Background()
				agent := createAgent(t, configs)
				project := createProject(t, h, "Chat title regression")
				h.llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())

				if path == "queued" {
					input := &models.ThreadInput{
						Scope: models.ThreadInputScopeChat, ProjectID: project.ID,
						AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued,
						InputStatus: models.ThreadInputPending, Content: tc.message,
						ChatMode: models.ChatModeOrchestrate, Source: models.TaskOriginTelegram,
						TelegramChatID: 12345,
					}
					require.NoError(t, h.threadInputRepo.CreateQueued(ctx, input))
					h.startQueuedChatInput(ctx, *input)
				} else {
					endpoint := "/chat/send?project_id=" + project.ID
					if path == "API" {
						endpoint = "/api/chat/message"
					}
					form := url.Values{"message": {tc.message}, "project_id": {project.ID}, "agent_id": {agent.ID}}
					req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					req.Header.Set("HX-Request", "true")
					rec := httptest.NewRecorder()
					e.ServeHTTP(rec, req)
					require.Less(t, rec.Code, 300, rec.Body.String())
				}

				tasks, err := h.taskRepo.ListByProject(ctx, project.ID, "")
				require.NoError(t, err)
				require.Len(t, tasks, 1)
				task := tasks[0]
				require.True(t, utf8.ValidString(task.Title), "title: %q", task.Title)
				require.Regexp(t, `^Chat \d{2}:\d{2}:\d{2}\.\d{3}: `, task.Title)
				_, preview, found := strings.Cut(task.Title, ": ")
				require.True(t, found)
				require.Equal(t, tc.preview, preview)
				require.LessOrEqual(t, len(preview), 50)
				require.Equal(t, tc.message, task.Prompt)
			})
		}
	}
}
