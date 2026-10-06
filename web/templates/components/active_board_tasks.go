package components

import "github.com/openvibely/openvibely/internal/models"

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
