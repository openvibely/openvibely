package sqlite

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestExecutionReplaySuffix(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewExecutionRepo(db)
	ctx := context.Background()
	task := &models.Task{ProjectID: "default", Title: "replay", Category: models.CategoryBacklog, Status: models.StatusPending}
	require.NoError(t, NewTaskRepo(db, nil).Create(ctx, task))
	ex := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, repo.Create(ctx, ex))
	require.NoError(t, repo.UpdateOutput(ctx, ex.ID, "hé"))
	for _, chunk := range []string{"llo", "世界", "!"} {
		require.NoError(t, repo.AppendOutput(ctx, ex.ID, chunk))
	}
	full := "héllo世界!"
	for offset := 0; offset <= len(full); offset++ {
		got, err := repo.ReadOutputAfter(ctx, ex.ID, offset)
		require.NoError(t, err)
		require.Equal(t, len(full), got.Total)
		require.Equal(t, full[got.Start:], got.Output)
		expectedStart := offset
		for expectedStart > 0 && expectedStart < len(full) && !utf8.RuneStart(full[expectedStart]) {
			expectedStart--
		}
		require.Equal(t, expectedStart, got.Start)
	}
	got, err := repo.ReadOutputAfter(ctx, ex.ID, len(full)+20)
	require.NoError(t, err)
	require.Empty(t, got.Output)
	require.NoError(t, repo.Complete(ctx, ex.ID, models.ExecCompleted, "short", "", 0, 0))
	got, err = repo.ReadOutputAfter(ctx, ex.ID, len(full))
	require.NoError(t, err)
	require.Equal(t, 0, got.Start)
	require.Equal(t, "short", got.Output)
	require.Equal(t, 5, got.Total)
	got, err = repo.ReadOutputAfter(ctx, "missing", 0)
	require.NoError(t, err)
	require.Nil(t, got)
}

func BenchmarkExecutionReplayTail(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewExecutionRepo(db)
	ctx := context.Background()
	task := &models.Task{ProjectID: "default", Title: "replay benchmark", Category: models.CategoryBacklog, Status: models.StatusPending}
	require.NoError(b, NewTaskRepo(db, nil).Create(ctx, task))
	ex := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(b, repo.Create(ctx, ex))
	chunk := strings.Repeat("x", 8192)
	for i := 0; i < 128; i++ {
		require.NoError(b, repo.AppendOutput(ctx, ex.ID, chunk))
	}
	require.NoError(b, repo.AppendOutput(ctx, ex.ID, strings.Repeat("y", 100)))
	b.Run("full_read", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, err := repo.GetByID(ctx, ex.ID)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("missing_100_bytes", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			got, err := repo.ReadOutputAfter(ctx, ex.ID, 1024*1024)
			if err != nil {
				b.Fatal(err)
			}
			if len(got.Output) != 100 {
				b.Fatal(len(got.Output))
			}
		}
	})
}
