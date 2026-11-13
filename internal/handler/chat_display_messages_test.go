package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestResponsesRemainContinuous(t *testing.T) {
	for _, category := range []models.TaskCategory{models.CategoryChat, models.CategoryActive} {
		for _, status := range []models.ExecutionStatus{models.ExecCompleted, models.ExecFailed, models.ExecCancelled} {
			t.Run(string(category)+"/"+string(status), func(t *testing.T) {
				h, e, configs := setupTestHandler(t)
				agent := createAgent(t, configs)
				project := createProject(t, h, "continuous output")
				other := createProject(t, h, "other project")
				task := createTask(t, h, project.ID, "messages", func(task *models.Task) { task.Category = category })
				execution := createExec(t, h, task.ID, agent.ID)
				var changes []models.ConversationChange
				for i := 0; i < 45; i++ {
					raw, err := json.Marshal(map[string]any{"type": "text", "text": fmt.Sprintf("visible-%02d\n", i)})
					require.NoError(t, err)
					changes = append(changes, models.ConversationChange{ItemID: fmt.Sprintf("display:%02d", i), Position: int64(i), Lane: models.ConversationDisplay, Operation: models.ConversationPut, Payload: raw, MutationID: fmt.Sprintf("msg-%d", i)})
				}
				_, err := h.execRepo.AppendConversationChanges(t.Context(), models.ConversationOwner{ExecutionID: execution.ID}, changes)
				require.NoError(t, err)
				require.NoError(t, h.execRepo.Complete(t.Context(), execution.ID, status, "", "terminal error", 0, 0))
				path := "/tasks/" + task.ID
				fragment := "/tasks/" + task.ID + "/thread/executions/" + execution.ID + "/fragment"
				if category == models.CategoryChat {
					path = "/chat"
					fragment = "/chat?approval_execution_id=" + execution.ID
				}
				for _, route := range []string{path, fragment} {
					separator := "?"
					if route == fragment && category == models.CategoryChat {
						separator = "&"
					}
					response := htmxGet(e, route+separator+"project_id="+project.ID)
					require.Equal(t, http.StatusOK, response.Code)
					body := response.Body.String()
					for i := 0; i < 45; i++ {
						require.Contains(t, body, fmt.Sprintf("visible-%02d", i))
					}
					require.NotContains(t, body, "data-message-window")
					require.NotContains(t, body, ">Earlier messages</button>")
					require.NotContains(t, body, ">Latest messages</button>")
				}
				if category == models.CategoryChat {
					denied := htmxGet(e, path+"?project_id="+other.ID)
					require.NotContains(t, denied.Body.String(), "visible-00")
				} else {
					otherTask := createTask(t, h, other.ID, "other task")
					denied := htmxGet(e, "/tasks/"+otherTask.ID+"/thread/executions/"+execution.ID+"/fragment")
					require.Equal(t, http.StatusNotFound, denied.Code)
				}
				removed := htmxGet(e, "/chat/executions/"+execution.ID+"/messages?project_id="+project.ID)
				require.Equal(t, http.StatusNotFound, removed.Code)
			})
		}
	}
}

func TestOutputWindowProjectScopeAndContinuation(t *testing.T) {
	h, e, configs := setupTestHandler(t)
	agent := createAgent(t, configs)
	project := createProject(t, h, "window")
	other := createProject(t, h, "other")
	task := createTask(t, h, project.ID, "thread", func(task *models.Task) { task.Category = models.CategoryChat })
	execution := createExec(t, h, task.ID, agent.ID)
	output := "fixture_oldest_marker\n" + strings.Repeat("text\n[Using tool: shell]\n[Tool shell done]\n"+strings.Repeat("x", 10000)+"\n[/Tool]\n", 150) + "latest\n"
	require.NoError(t, h.execRepo.Complete(t.Context(), execution.ID, models.ExecCompleted, output, "", 0, 0))
	initial := htmxGet(e, "/chat?project_id="+project.ID)
	require.Equal(t, http.StatusOK, initial.Code)
	require.Contains(t, initial.Body.String(), "latest")
	require.False(t, strings.Contains(initial.Body.String(), "fixture_oldest_marker"), "initial response loaded the full history")
	execution, err := h.loadDisplayExecution(t.Context(), execution.ID)
	require.NoError(t, err)
	require.NotNil(t, execution.DisplayTail)
	tail := execution.DisplayTail
	path := fmt.Sprintf("/chat/output/%s?before=%d&revision=%s&project_id=", execution.ID, tail.Before, tail.Revision)
	denied := htmxGet(e, path+other.ID)
	require.Equal(t, http.StatusNotFound, denied.Code)
	var recovered = models.DisplayTail{Text: execution.Output, Before: tail.Before, Revision: tail.Revision}
	joined := recovered.Text
	for recovered.Before != 0 {
		path = fmt.Sprintf("/chat/output/%s?before=%d&revision=%s&project_id=%s", execution.ID, recovered.Before, recovered.Revision, project.ID)
		response := htmxGet(e, path)
		require.Equal(t, http.StatusOK, response.Code)
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &recovered))
		joined = recovered.Text + joined
	}
	require.Equal(t, output, joined)
	stale := htmxGet(e, fmt.Sprintf("/chat/output/%s?before=%d&revision=stale&project_id=%s", execution.ID, tail.Before, project.ID))
	require.Equal(t, http.StatusConflict, stale.Code)

	// A conflict can refresh the latest bounded window without an old revision,
	// but must retain exactly the same project authorization.
	refreshPath := fmt.Sprintf("/chat/output/%s?before=0&revision=stale&project_id=", execution.ID)
	require.Equal(t, http.StatusNotFound, htmxGet(e, refreshPath+other.ID).Code)
	refresh := htmxGet(e, refreshPath+project.ID)
	require.Equal(t, http.StatusOK, refresh.Code)
	require.Equal(t, "no-store", refresh.Header().Get("Cache-Control"))
	var latest models.DisplayTail
	require.NoError(t, json.Unmarshal(refresh.Body.Bytes(), &latest))
	require.Equal(t, tail.Before, latest.Before)
	require.Equal(t, tail.Revision, latest.Revision)
	require.Equal(t, execution.Output, latest.Text)
	require.Equal(t, http.StatusConflict, htmxGet(e, fmt.Sprintf("/chat/output/%s?before=-1&project_id=%s", execution.ID, project.ID)).Code)
	for _, cursor := range []string{"", "invalid"} {
		require.Equal(t, http.StatusBadRequest, htmxGet(e, fmt.Sprintf("/chat/output/%s?before=%s&project_id=%s", execution.ID, cursor, project.ID)).Code)
	}
}
