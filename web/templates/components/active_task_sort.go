package components

import (
	"context"
	"sort"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

type activeTaskSortKey struct{}

func WithActiveTaskSort(ctx context.Context, value string) context.Context {
	return context.WithValue(ctx, activeTaskSortKey{}, value)
}

func activeTaskSort(ctx context.Context) string {
	value, _ := ctx.Value(activeTaskSortKey{}).(string)
	return value
}

// ActiveBoardTasks selects the visible Active cards, including scheduled work.
func ActiveBoardTasks(tasks []models.Task) []models.Task {
	var result []models.Task
	for _, task := range filterActiveColumn(tasks) {
		switch activeBoardStatus(task) {
		case models.StatusRunning, models.StatusPending, models.StatusQueued, models.StatusBlocked:
			result = append(result, task)
		}
	}
	return result
}

func sortActiveTasks(tasks []models.Task, preference string) []models.Task {
	if preference == "" {
		return tasks
	}
	result := append([]models.Task(nil), tasks...)
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		switch preference {
		case "title_asc":
			return strings.ToLower(a.Title) < strings.ToLower(b.Title)
		case "title_desc":
			return strings.ToLower(a.Title) > strings.ToLower(b.Title)
		case "created_asc":
			return a.CreatedAt.Before(b.CreatedAt)
		case "created_desc":
			return a.CreatedAt.After(b.CreatedAt)
		case "priority_asc":
			return a.Priority < b.Priority
		case "priority_desc":
			return a.Priority > b.Priority
		}
		return false
	})
	return result
}
