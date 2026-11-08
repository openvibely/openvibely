package service

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestGitHubFeedbackParsersPreserveImmutableReviewerID(t *testing.T) {
	data := []byte(`{"id":42,"user":{"id":123,"login":"reviewer","type":"User"}}`)
	var comment githubIssueCommentAPI
	require.NoError(t, json.Unmarshal(data, &comment))
	var review githubPullRequestReviewAPI
	require.NoError(t, json.Unmarshal(data, &review))
	var inline githubPullRequestReviewCommentAPI
	require.NoError(t, json.Unmarshal(data, &inline))
	for _, item := range []GitHubPullRequestFeedback{comment.toFeedback(), review.toFeedback(), inline.toFeedback()} {
		require.Equal(t, int64(123), item.AuthorUserID)
		require.Equal(t, "reviewer", item.AuthorLogin)
	}
}
