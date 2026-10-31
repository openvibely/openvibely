package sqlite

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/transcript"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestResponsesHistoryKeepsFallbackOutput(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	original, err := repo.GetByID(ctx, owner.ExecutionID)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, format, key string
		count             int
		status            models.ExecutionStatus
		omitted           bool
	}{
		{"native", "envelope", "responses_input", 1, models.ExecCompleted, true},
		{"failed", "envelope", "responses_input", 1, models.ExecFailed, false},
		{"empty", "envelope", "responses_input", 0, models.ExecCompleted, false},
		{"foreign", "envelope", "messages", 1, models.ExecCompleted, false},
		{"legacy", "array", "", 0, models.ExecCompleted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ex := &models.Execution{TaskID: original.TaskID, Status: models.ExecRunning}
			require.NoError(t, repo.Create(ctx, ex))
			items := []any{}
			if tc.count > 0 {
				items = append(items, map[string]any{"role": "assistant", "content": "answer"})
			}
			require.NoError(t, transcript.NewRecorder(repo, models.ConversationOwner{ExecutionID: ex.ID}).Save(ctx, transcript.ReplayHeader{Format: tc.format, ItemsKey: tc.key}, items))
			require.NoError(t, repo.Complete(ctx, ex.ID, tc.status, "display answer", "", 0, 0))
			rows, err := repo.ListTaskResponsesHistory(ctx, original.TaskID, 100)
			require.NoError(t, err)
			found := false
			for _, row := range rows {
				if row.ID == ex.ID {
					found = true
					if tc.omitted {
						require.Empty(t, row.Output)
					} else {
						require.Equal(t, "display answer", row.Output)
					}
				}
			}
			require.True(t, found)
			full, err := repo.GetByID(ctx, ex.ID)
			require.NoError(t, err)
			require.Equal(t, "display answer", full.Output)
		})
	}
}

func TestAnthropicHistoryKeepsFallbackOutput(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	original, err := repo.GetByID(ctx, owner.ExecutionID)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, format, key string
		count             int
		status            models.ExecutionStatus
		omitted           bool
	}{
		{"native", "envelope", "messages", 1, models.ExecCompleted, true},
		{"failed", "envelope", "messages", 1, models.ExecFailed, false},
		{"empty", "envelope", "messages", 0, models.ExecCompleted, false},
		{"foreign", "envelope", "responses_input", 1, models.ExecCompleted, false},
		{"legacy", "array", "", 0, models.ExecCompleted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ex := &models.Execution{TaskID: original.TaskID, Status: models.ExecRunning}
			require.NoError(t, repo.Create(ctx, ex))
			items := []any{}
			if tc.count > 0 {
				items = append(items, map[string]any{"role": "assistant", "content": "answer"})
			}
			require.NoError(t, transcript.NewRecorder(repo, models.ConversationOwner{ExecutionID: ex.ID}).Save(ctx, transcript.ReplayHeader{Format: tc.format, ItemsKey: tc.key}, items))
			require.NoError(t, repo.Complete(ctx, ex.ID, tc.status, "display answer", "", 0, 0))
			rows, err := repo.ListTaskAnthropicHistory(ctx, original.TaskID, 100)
			require.NoError(t, err)
			found := false
			for _, row := range rows {
				if row.ID == ex.ID {
					found = true
					if tc.omitted {
						require.Empty(t, row.Output)
					} else {
						require.Equal(t, "display answer", row.Output)
					}
				}
			}
			require.True(t, found)
			full, err := repo.GetByID(ctx, ex.ID)
			require.NoError(t, err)
			require.Equal(t, "display answer", full.Output)
		})
	}
}

func TestCompletionsHistoryKeepsFallbackOutput(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	original, err := repo.GetByID(ctx, owner.ExecutionID)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, format, key string
		count             int
		status            models.ExecutionStatus
		omitted           bool
	}{
		{"native", "array", "", 1, models.ExecCompleted, true},
		{"failed", "array", "", 1, models.ExecFailed, false},
		{"cancelled", "array", "", 1, models.ExecCancelled, false},
		{"empty", "array", "", 0, models.ExecCompleted, false},
		{"anthropic", "envelope", "messages", 1, models.ExecCompleted, false},
		{"responses", "envelope", "responses_input", 1, models.ExecCompleted, false},
		{"legacy", "none", "", 0, models.ExecCompleted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ex := &models.Execution{TaskID: original.TaskID, Status: models.ExecRunning}
			require.NoError(t, repo.Create(ctx, ex))
			items := []any{}
			if tc.count > 0 {
				items = append(items, map[string]any{"role": "assistant", "content": "answer"})
			}
			require.NoError(t, transcript.NewRecorder(repo, models.ConversationOwner{ExecutionID: ex.ID}).Save(ctx, transcript.ReplayHeader{Format: tc.format, ItemsKey: tc.key}, items))
			require.NoError(t, repo.Complete(ctx, ex.ID, tc.status, "display answer", "", 0, 0))
			rows, err := repo.ListTaskCompletionsHistory(ctx, original.TaskID, 100)
			require.NoError(t, err)
			found := false
			for _, row := range rows {
				if row.ID == ex.ID {
					found = true
					if tc.omitted {
						require.Empty(t, row.Output)
					} else {
						require.Equal(t, "display answer", row.Output)
					}
				}
			}
			require.True(t, found)
			full, err := repo.GetByID(ctx, ex.ID)
			require.NoError(t, err)
			require.Equal(t, "display answer", full.Output)
		})
	}
}

func TestStreamOffsetLookupUsesCoveringIndex(t *testing.T) {
	db, _, _ := conversationTestRepo(t)
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT end_offset FROM execution_stream_text WHERE execution_id=? ORDER BY end_offset DESC LIMIT 1`, "test")
	require.NoError(t, err)
	defer rows.Close()
	found := false
	for rows.Next() {
		var a, b, c int
		var detail string
		require.NoError(t, rows.Scan(&a, &b, &c, &detail))
		found = found || strings.Contains(detail, "COVERING INDEX execution_stream_offsets")
	}
	require.NoError(t, rows.Err())
	require.True(t, found)
}

func TestModelHistoryProjectionIsScoped(t *testing.T) {
	db, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	original, err := repo.GetByID(ctx, owner.ExecutionID)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE tasks SET category='chat' WHERE id=?`, original.TaskID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO email_task_context(task_id,email_from,email_message_id,email_session_key) VALUES(?,'sender@example.test','message','session')`, original.TaskID)
	require.NoError(t, err)
	for _, provider := range []models.LLMProvider{models.ProviderOpenAI, models.ProviderAnthropic, models.ProviderOpenAICompatible, models.ProviderOllama} {
		t.Run(string(provider), func(t *testing.T) {
			scoped := persistence.WithModelHistory(ctx, provider)
			format, key, _ := persistence.ModelHistoryFormat(scoped)
			ex := &models.Execution{TaskID: original.TaskID, Status: models.ExecRunning}
			require.NoError(t, repo.Create(ctx, ex))
			require.NoError(t, transcript.NewRecorder(repo, models.ConversationOwner{ExecutionID: ex.ID}).Save(ctx, transcript.ReplayHeader{Format: format, ItemsKey: key}, []any{map[string]any{"role": "assistant", "content": "answer"}}))
			require.NoError(t, repo.Complete(ctx, ex.ID, models.ExecCompleted, "display", "", 0, 0))
			for _, read := range []func(context.Context) ([]models.Execution, error){
				func(c context.Context) ([]models.Execution, error) {
					return repo.ListByTaskChronological(c, original.TaskID)
				},
				func(c context.Context) ([]models.Execution, error) {
					return repo.ListByTaskChronologicalLimit(c, original.TaskID, 100)
				},
				func(c context.Context) ([]models.Execution, error) { return repo.ListChatHistory(c, "default", 100) },
				func(c context.Context) ([]models.Execution, error) {
					return repo.ListEmailChatHistory(c, "default", "session", 100)
				},
			} {
				for _, c := range []context.Context{ctx, scoped} {
					rows, err := read(c)
					require.NoError(t, err)
					found := false
					for _, r := range rows {
						if r.ID == ex.ID {
							found = true
							if c == ctx {
								require.Equal(t, "display", r.Output)
							} else {
								require.Empty(t, r.Output)
							}
						}
					}
					require.True(t, found)
				}
			}
		})
	}
}

func TestExecutionMetadataProjectionPreservesIdentityAndPaging(t *testing.T) {
	db, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	original, err := repo.GetByID(ctx, owner.ExecutionID)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE tasks SET category='chat' WHERE id=?`, original.TaskID)
	require.NoError(t, err)
	require.NoError(t, repo.FinalizeOutput(ctx, owner.ExecutionID, "large display"))
	_, err = db.Exec(`UPDATE executions SET prompt_sent='prompt',reasoning_content='reasoning',error_message='error',diff_output='diff',cli_session_id='session',starts_new_context=1 WHERE id=?`, owner.ExecutionID)
	require.NoError(t, err)
	next := &models.Execution{TaskID: original.TaskID, Status: models.ExecRunning}
	require.NoError(t, repo.Create(ctx, next))
	reads := []func(context.Context) ([]models.Execution, error){
		func(c context.Context) ([]models.Execution, error) {
			e, err := repo.GetByID(c, owner.ExecutionID)
			if err != nil {
				return nil, err
			}
			return []models.Execution{*e}, nil
		},
		func(c context.Context) ([]models.Execution, error) {
			return repo.ListByTaskChronologicalBefore(c, original.TaskID, next.ID, 100)
		},
		func(c context.Context) ([]models.Execution, error) {
			return repo.ListChatHistoryBefore(c, "default", next.ID, 100)
		},
	}
	for _, read := range reads {
		full, err := read(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, full)
		narrow, err := read(persistence.WithExecutionMetadata(ctx))
		require.NoError(t, err)
		require.Len(t, narrow, len(full))
		for i := range full {
			require.Empty(t, narrow[i].Output)
			require.Empty(t, narrow[i].PromptSent)
			require.Empty(t, narrow[i].ReasoningContent)
			full[i].Output = ""
			full[i].PromptSent = ""
			full[i].ReasoningContent = ""
			full[i].DiffOutput = ""
			full[i].ErrorMessage = ""
			full[i].CliSessionID = ""
			require.Equal(t, full[i], narrow[i])
		}
	}
	again, err := repo.GetByID(ctx, owner.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, "large display", again.Output)
	absent, err := repo.GetByID(persistence.WithExecutionMetadata(ctx), "absent")
	require.NoError(t, err)
	require.Nil(t, absent)
}
