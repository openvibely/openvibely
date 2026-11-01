package sqlite

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"time"
)

// All calendar changes share one transaction, including cross-project undo.
func applyAllProjectsCalendarAction(ctx context.Context, exec sqlExecutor, action models.ScheduleCalendarAction, now time.Time) (models.ScheduleCalendarAction, error) {
	inverse := models.ScheduleCalendarAction{Action: "undo_projects", ProjectActions: map[string]models.ScheduleCalendarAction{}}
	projects := map[string]models.ScheduleCalendarAction{}
	rows, err := exec.QueryContext(ctx, "SELECT id FROM projects WHERE id != ? ORDER BY id", models.AllProjectsID)
	if err != nil {
		return inverse, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return inverse, err
		}
		projects[id] = models.ScheduleCalendarAction{Action: action.Action}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return inverse, err
	}
	owner := func(id string) (string, error) {
		var project string
		err := exec.QueryRowContext(ctx, "SELECT t.project_id FROM schedules s JOIN tasks t ON t.id=s.task_id WHERE s.id=?", id).Scan(&project)
		if err != nil {
			return "", persistence.ErrScheduleCalendarSelection
		}
		if _, ok := projects[project]; !ok {
			return "", persistence.ErrScheduleCalendarSelection
		}
		return project, nil
	}
	if action.Action == "undo_projects" {
		for id, part := range action.ProjectActions {
			if _, ok := projects[id]; !ok || len(part.ProjectActions) != 0 {
				return inverse, persistence.ErrScheduleCalendarSelection
			}
		}
		projects = action.ProjectActions
	} else {
		for _, id := range action.ScheduleIDs {
			project, err := owner(id)
			if err != nil {
				return inverse, err
			}
			part := projects[project]
			part.ScheduleIDs = append(part.ScheduleIDs, id)
			projects[project] = part
		}
		for _, skip := range action.Skips {
			if skip.ScheduleID == "" {
				for project, part := range projects {
					part.Skips = append(part.Skips, skip)
					projects[project] = part
				}
				continue
			}
			project, err := owner(skip.ScheduleID)
			if err != nil {
				return inverse, err
			}
			part := projects[project]
			part.Skips = append(part.Skips, skip)
			projects[project] = part
		}
		for _, change := range action.Changes {
			if change.Skip.ScheduleID == "" {
				for project, part := range projects {
					part.Changes = append(part.Changes, change)
					projects[project] = part
				}
				continue
			}
			project, err := owner(change.Skip.ScheduleID)
			if err != nil {
				return inverse, err
			}
			part := projects[project]
			part.Changes = append(part.Changes, change)
			projects[project] = part
		}
	}
	for project, part := range projects {
		if part.Action != "pause_all" && part.Action != "resume_all" && len(part.ScheduleIDs) == 0 && len(part.Skips) == 0 && len(part.Changes) == 0 {
			continue
		}
		undo, err := applyCalendarAction(ctx, exec, project, part, now)
		if err != nil {
			return inverse, err
		}
		if undo.Action == "pause_all" || undo.Action == "resume_all" || len(undo.ScheduleIDs) > 0 || len(undo.Changes) > 0 || len(undo.Skips) > 0 {
			inverse.ProjectActions[project] = undo
		}
	}
	return inverse, nil
}
