package handler

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestPageConversationSharedSteering(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprint("queued=", queued), func(t *testing.T) {
			env := setupPluginPages(t)
			handle := env.openPage(t, env.inA)
			response, _ := env.bridge(t, env.inA, env.projA.ID, handle, "context.set", `{"summary":"North snapshot"}`)
			require.Equal(t, 200, response.Code)
			sendPageTest(t, env, env.inA, "first")
			first := env.starts[0]
			sendPageTest(t, env, env.inB, "other-page")
			endpoint := pluginAPI(env.inA.ID, env.projA.ID, "/chat/steer")
			if queued {
				sendPageTest(t, env, env.inA, "adjust")
				pending, err := env.h.threadInputRepo.ListPendingForTask(t.Context(), first.TaskID)
				require.NoError(t, err)
				require.Len(t, pending, 1)
				endpoint = pluginAPI(env.inA.ID, env.projA.ID, "/chat/inputs/"+pending[0].ID+"/steer")
				transcript := env.do(t, http.MethodGet, pluginAPI(env.inA.ID, env.projA.ID, "/chat/transcript"), "", nil)
				require.Contains(t, transcript.Body.String(), `aria-label="Edit message"`)
				require.Contains(t, transcript.Body.String(), `aria-label="Convert queued follow-up to steering"`)
			}
			headers := map[string]string{"HX-Request": "true", "Content-Type": "application/x-www-form-urlencoded"}
			stale := env.do(t, http.MethodPost, endpoint, url.Values{"message": {"adjust"}, "expected_turn_id": {env.starts[1].ExecID}}.Encode(), headers)
			require.Equal(t, http.StatusConflict, stale.Code, stale.Body.String())
			good := env.do(t, http.MethodPost, endpoint, url.Values{"message": {"adjust"}, "expected_turn_id": {first.ExecID}}.Encode(), headers)
			require.Equal(t, http.StatusOK, good.Code, good.Body.String())
			require.Contains(t, good.Body.String(), "Steering pending")
			require.Len(t, env.starts, 2, "steering must not start a separate response")
			pending, err := env.h.threadInputRepo.ListPendingForTask(t.Context(), first.TaskID)
			require.NoError(t, err)
			require.Len(t, pending, 1)
			require.Equal(t, models.ThreadInputModeSteering, pending[0].InputMode)
			contextBefore, toolsBefore := first.SystemContext, first.RuntimeTools
			batch, err := env.h.claimPendingSteeringInputs(t.Context(), &first)
			require.NoError(t, err)
			require.Len(t, batch.inputs, 1)
			require.Equal(t, "adjust", batch.inputs[0].Content)
			require.Equal(t, contextBefore, first.SystemContext)
			require.Same(t, toolsBefore, first.RuntimeTools)
			other, err := env.h.claimPendingSteeringInputs(t.Context(), &env.starts[1])
			require.NoError(t, err)
			require.Empty(t, other.inputs)
			// Failed provider delivery uses the shared requeue path, with page context
			// already durable before the steer became visible to the active model.
			env.h.requeueUncommittedSteering(t.Context(), first.ExecID, batch)
			require.NoError(t, env.h.execRepo.Complete(t.Context(), first.ExecID, models.ExecFailed, "", "provider failed", 0, 0))
			require.NoError(t, env.h.taskRepo.UpdateStatus(t.Context(), first.TaskID, models.StatusFailed))
			env.h.startNextQueuedTurnAfter(t.Context(), first, "")
			require.Len(t, env.starts, 3)
			require.Equal(t, "adjust", env.starts[2].Message)
			require.Contains(t, env.starts[2].SystemContext, "North snapshot")
			require.NotNil(t, env.starts[2].RuntimeTools)
			require.False(t, env.starts[2].IsTaskFollowup)
		})
	}
}

func TestPageChatHistoryUsesSharedPaging(t *testing.T) {
	env := setupPluginPages(t)
	sendPageTest(t, env, env.inA, "oldest-marker")
	first := env.starts[0]
	require.NoError(t, env.h.execRepo.Complete(t.Context(), first.ExecID, models.ExecCompleted, "oldest answer", "", 0, 0))
	ids := []string{first.ExecID}
	for i := 0; i < 110; i++ {
		execution := &models.Execution{TaskID: first.TaskID, AgentConfigID: env.agent.ID, Status: models.ExecCompleted, PromptSent: fmt.Sprintf("message-%d", i), IsFollowup: true}
		require.NoError(t, env.h.execRepo.Create(t.Context(), execution))
		ids = append(ids, execution.ID)
	}
	transcript := env.do(t, http.MethodGet, pluginAPI(env.inA.ID, env.projA.ID, "/chat/transcript"), "", nil)
	require.Equal(t, 200, transcript.Code)
	require.Contains(t, transcript.Body.String(), `data-earlier-loader="true"`)
	require.NotContains(t, transcript.Body.String(), "oldest-marker")
	second := env.do(t, http.MethodGet, pluginAPI(env.inA.ID, env.projA.ID, "/chat/history")+"&before="+ids[61], "", nil)
	require.Equal(t, 200, second.Code)
	require.Equal(t, 50, strings.Count(second.Body.String(), `data-execution-pair="true"`))
	require.Contains(t, second.Body.String(), `data-earlier-loader="true"`)
	last := env.do(t, http.MethodGet, pluginAPI(env.inA.ID, env.projA.ID, "/chat/history")+"&before="+ids[11], "", nil)
	require.Equal(t, 200, last.Code)
	require.Contains(t, last.Body.String(), "oldest-marker")
	require.NotContains(t, last.Body.String(), `data-earlier-loader="true"`)
	require.Equal(t, 11, strings.Count(last.Body.String(), `data-execution-pair="true"`))
	wrong := env.do(t, http.MethodGet, pluginAPI(env.inA.ID, env.projB.ID, "/chat/history")+"&before="+ids[11], "", nil)
	require.Equal(t, http.StatusNotFound, wrong.Code)
}
