package components

import (
	"bytes"
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMessageAuthorRendering(t *testing.T) {
	var b bytes.Buffer
	exec := models.Execution{ID: "exec", AuthorName: "testuser1", Status: models.ExecRunning, IsFollowup: true, PromptSent: "hello"}
	require.NoError(t, ChatFollowupResponse("hello", exec.ID, "messages", "", true, nil, "default", exec).Render(context.Background(), &b))
	require.Contains(t, b.String(), "testuser1")
	b.Reset()
	require.NoError(t, ChatExecutionPair(exec, nil, []models.Execution{exec}, 0, false, nil, "messages", "", "default").Render(context.Background(), &b))
	require.Contains(t, b.String(), "testuser1")
	b.Reset()
	require.NoError(t, ChatBubble("User", "old message").Render(context.Background(), &b))
	require.Contains(t, b.String(), "User")
	b.Reset()
	require.NoError(t, ChatBubble("User", "hello", "<script>alert(1)</script>").Render(context.Background(), &b))
	require.NotContains(t, b.String(), "<script>")
	require.Contains(t, b.String(), "&lt;script&gt;")
}
