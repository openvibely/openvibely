package service

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/stretchr/testify/require"
)

// Simulate the router's concatenation of independently ordered server results.
type concatenatedTaskStore struct {
	persistence.TaskStore
	tasks []models.Task
}

func (s concatenatedTaskStore) ListBoardByProjectWithCategorySorts(context.Context, string, string, string, string) ([]models.Task, error) {
	return append([]models.Task(nil), s.tasks...), nil
}
func (s concatenatedTaskStore) ListByProjectWithCategorySorts(context.Context, string, string, string, string) ([]models.Task, error) {
	return append([]models.Task(nil), s.tasks...), nil
}

func TestTaskListsSortAcrossServers(t *testing.T) {
	for _, category := range []models.TaskCategory{models.CategoryCompleted, models.CategoryBacklog} {
		for _, order := range []string{"created_asc", "created_desc", "completed_asc", "completed_desc", "title_asc", "title_desc", "priority_asc", "priority_desc"} {
			t.Run(string(category)+"/"+order, func(t *testing.T) {
				var tasks []models.Task
				for _, n := range []int{1, 3, 2, 4} {
					at := time.Unix(int64(n), 0)
					owner := "local"
					if n%2 == 0 {
						owner = "remote"
					}
					tasks = append(tasks, models.Task{ID: string(rune('a' + n)), ProjectID: owner, Title: string(rune('a' + n)), Priority: n, Category: category, Status: models.StatusCompleted, CreatedAt: at, UpdatedAt: at, CompletedAt: &at})
				}
				svc := NewTaskService(concatenatedTaskStore{tasks: tasks}, nil, nil)
				for _, scope := range []string{models.AllProjectsID, "__repo_123456789abcdef0"} {
					for _, list := range []func(context.Context, string, string, string, string) ([]models.Task, error){svc.ListByProjectWithCategorySorts, svc.ListBoardByProjectWithCategorySorts} {
						got, err := list(context.Background(), scope, "", order, order)
						require.NoError(t, err)
						ids := []string{}
						for _, task := range got {
							ids = append(ids, task.ID)
						}
						expected := []string{"b", "c", "d", "e"}
						if order[len(order)-4:] == "desc" {
							expected = []string{"e", "d", "c", "b"}
						}
						require.Equal(t, expected, ids)
						// Single-server requests preserve the store's ordering.
						got, err = list(context.Background(), "local", "", order, order)
						require.NoError(t, err)
						require.Equal(t, tasks, got)
					}
				}
			})
		}
	}
}
