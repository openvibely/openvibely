package handler

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPageUsesTaskThreadViewAndAuthorizedReaders(t *testing.T) {
	env := setupPluginPages(t)
	// Empty pages need an identity before mounting the existing thread runtime.
	env.openPage(t, env.inA)
	base := pluginAPI(env.inA.ID, env.projA.ID, "/chat/thread")
	first := env.do(t, http.MethodGet, base, "", nil)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Contains(t, first.Body.String(), `id="task-thread-view"`)
	require.Contains(t, first.Body.String(), `id="task-thread-runtime-`)
	require.Contains(t, first.Body.String(), `data-thread-api-base="/api/plugin-pages/`+env.inA.ID+`/chat/thread"`)
	require.Contains(t, first.Body.String(), `data-thread-project-id="`+env.projA.ID+`"`)
	require.Contains(t, first.Body.String(), `window.reconcileTaskThreadState`)
	require.NotContains(t, first.Body.String(), `controller.mount`)
	sendPageTest(t, env, env.inA, "reader owner")
	exec := env.starts[0]
	for _, suffix := range []string{"/chat/thread", "/chat/thread/pending-inputs", "/chat/thread/composer-action", "/chat/thread/executions/" + exec.ExecID + "/fragment"} {
		rec := env.do(t, http.MethodGet, pluginAPI(env.inA.ID, env.projA.ID, suffix), "", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		wrong := env.do(t, http.MethodGet, pluginAPI(env.inA.ID, env.projB.ID, suffix), "", nil)
		require.Equal(t, http.StatusNotFound, wrong.Code)
	}
	other := env.do(t, http.MethodGet, pluginAPI(env.inB.ID, env.projB.ID, "/chat/thread/executions/"+exec.ExecID+"/fragment"), "", nil)
	require.Equal(t, http.StatusNotFound, other.Code)
	poll := env.do(t, http.MethodGet, base+"&poll=1&preserved_exec_ids="+exec.ExecID, "", nil)
	require.Equal(t, http.StatusOK, poll.Code)
	require.Contains(t, poll.Body.String(), `hx-preserve="true"`)
	require.NotContains(t, poll.Body.String(), `window.reconcileTaskThreadState =`)
	require.NoError(t, env.h.pagePlugins.Disable(t.Context(), env.inA.ID))
	denied := env.do(t, http.MethodGet, base, "", nil)
	require.Equal(t, http.StatusNotFound, denied.Code)
}
