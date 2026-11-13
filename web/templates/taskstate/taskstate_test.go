package taskstate

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestOpenPullRequestState(t *testing.T) {
	for _, tc := range []struct {
		name string
		task models.Task
		want string
	}{
		{"open", models.Task{Status: models.StatusCompleted, HasOpenPullRequest: true}, "pr-open"},
		{"goal achieved", models.Task{Status: models.StatusCompleted, GoalMet: true, HasOpenPullRequest: true}, "pr-open"},
		{"no open PR", models.Task{Status: models.StatusCompleted}, "completed"},
		{"running", models.Task{Status: models.StatusRunning, HasOpenPullRequest: true}, "running"},
		{"failed", models.Task{Status: models.StatusFailed, HasOpenPullRequest: true}, "failed"},
		{"merged", models.Task{Status: models.StatusCompleted, MergeStatus: models.MergeStatusMerged, HasOpenPullRequest: true}, "merged"},
		{"conflict", models.Task{Status: models.StatusCompleted, MergeStatus: models.MergeStatusConflict, HasOpenPullRequest: true}, "merge-conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Display(tc.task).Key; got != tc.want {
				t.Fatalf("state = %q, want %q", got, tc.want)
			}
		})
	}
	var out bytes.Buffer
	if err := Icon(models.Task{Status: models.StatusCompleted, HasOpenPullRequest: true}).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`data-task-state="pr-open"`, `aria-label="Task state: Pull request open"`, `text-success`, `<svg`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %s in %s", want, out.String())
		}
	}
}
