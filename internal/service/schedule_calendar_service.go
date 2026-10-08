package service

import (
	"context"
	"fmt"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

func ApplyScheduleCalendarAction(ctx context.Context, repo *repository.ScheduleRepo, projectID string, action models.ScheduleCalendarAction) (models.ScheduleCalendarAction, error) {
	invalid := func() (models.ScheduleCalendarAction, error) {
		return models.ScheduleCalendarAction{}, fmt.Errorf("%w: choose schedules or dates", repository.ErrScheduleCalendarSelection)
	}
	if projectID == "" || len(action.ScheduleIDs) > 2000 || len(action.Skips) > 2000 || len(action.Changes) > 10000 {
		return invalid()
	}
	if action.Action != "undo_skips" && len(action.Changes) != 0 {
		return invalid()
	}
	switch action.Action {
	case "undo_skips":
		if len(action.Changes) == 0 || len(action.Skips) != 0 || len(action.ScheduleIDs) != 0 {
			return invalid()
		}
		for _, change := range action.Changes {
			if change.Skip.StartAt <= 0 || change.Skip.EndAt <= change.Skip.StartAt || (change.Skip.Restored && change.Skip.ScheduleID == "") {
				return invalid()
			}
		}
	case "pause", "resume":
		if len(action.ScheduleIDs) == 0 || len(action.Skips) != 0 {
			return invalid()
		}
	case "skip", "restore":
		if len(action.Skips) == 0 || len(action.ScheduleIDs) != 0 {
			return invalid()
		}
		for _, skip := range action.Skips {
			if skip.Restored || skip.StartAt <= 0 || skip.EndAt <= skip.StartAt || skip.EndAt-skip.StartAt > 25*60*60 {
				return invalid()
			}
		}
	case "pause_all", "resume_all":
		if len(action.ScheduleIDs) != 0 || len(action.Skips) != 0 {
			return invalid()
		}
	default:
		return invalid()
	}
	return repo.ApplyCalendarAction(ctx, projectID, action, time.Now().UTC())
}
