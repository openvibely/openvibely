package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// listSchedulesMaxLimit bounds a single list_schedules page so discovery stays cheap
// and the result payload never grows without limit.
const listSchedulesMaxLimit = 50

// listSchedulesDefaultLimit is used when the caller does not request an explicit limit.
const listSchedulesDefaultLimit = 20

// ListSchedulesRequest is the decoded input for the read-only list_schedules
// discovery tool.
type ListSchedulesRequest struct {
	TaskID  string `json:"task_id"`
	Title   string `json:"title"`
	Enabled *bool  `json:"enabled"`
	Limit   int    `json:"limit"`
	Offset  int    `json:"offset"`
}

// scheduleDiscoverySummary is the compact, read-only projection returned for each
// schedule. It carries just enough identity for existing schedule mutation actions
// (schedule_id, task_id) plus recurrence and state fields for informed follow-ups.
type scheduleDiscoverySummary struct {
	ScheduleID          string `json:"schedule_id"`
	TaskID              string `json:"task_id"`
	TaskTitle           string `json:"task_title"`
	Enabled             bool   `json:"enabled"`
	RepeatType          string `json:"repeat_type"`
	RepeatInterval      int    `json:"repeat_interval"`
	Recurrence          string `json:"recurrence"`
	Days                string `json:"days,omitempty"`
	NextRun             string `json:"next_run,omitempty"`
	ClearContextOnStart bool   `json:"clear_context_on_start"`
}

type scheduleDiscoveryResult struct {
	OK        bool                       `json:"ok"`
	Schedules []scheduleDiscoverySummary `json:"schedules"`
	Count     int                        `json:"count"`
	Total     int                        `json:"total"`
	Limit     int                        `json:"limit"`
	Offset    int                        `json:"offset"`
	HasMore   bool                       `json:"has_more"`
}

// ExecuteListSchedulesTool runs the bounded, read-only, current-project schedule
// discovery tool. It never crosses project boundaries. Returns a compact JSON
// summary payload with deterministic ordering and explicit pagination. The returned
// schedule IDs work directly with modify_schedule and delete_schedule.
func ExecuteListSchedulesTool(ctx context.Context, scheduleRepo *repository.ScheduleRepo, projectID string, input json.RawMessage) (string, error) {
	if scheduleRepo == nil {
		return "", fmt.Errorf("list_schedules: schedule repository unavailable")
	}
	if strings.TrimSpace(projectID) == "" {
		return "", fmt.Errorf("list_schedules: no current project — cannot list schedules without a project context")
	}

	var req ListSchedulesRequest
	if len(strings.TrimSpace(string(input))) > 0 {
		if err := json.Unmarshal(input, &req); err != nil {
			return "", fmt.Errorf("list_schedules: invalid input: %w", err)
		}
	}

	limit := req.Limit
	if limit <= 0 {
		limit = listSchedulesDefaultLimit
	}
	if limit > listSchedulesMaxLimit {
		limit = listSchedulesMaxLimit
	}
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}

	rows, total, err := scheduleRepo.ListSchedulesForDiscovery(ctx, projectID, repository.ScheduleDiscoveryFilter{
		TaskID:  strings.TrimSpace(req.TaskID),
		Title:   strings.TrimSpace(req.Title),
		Enabled: req.Enabled,
		Limit:   limit,
		Offset:  offset,
	})
	if err != nil {
		return "", err
	}

	summaries := make([]scheduleDiscoverySummary, 0, len(rows))
	for i := range rows {
		summaries = append(summaries, buildScheduleDiscoverySummary(rows[i]))
	}

	result := scheduleDiscoveryResult{
		OK:        true,
		Schedules: summaries,
		Count:     len(summaries),
		Total:     total,
		Limit:     limit,
		Offset:    offset,
		HasMore:   offset+len(summaries) < total,
	}
	b, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func buildScheduleDiscoverySummary(row repository.ScheduleDiscoveryRow) scheduleDiscoverySummary {
	s := row.Schedule
	summary := scheduleDiscoverySummary{
		ScheduleID:          s.ID,
		TaskID:              s.TaskID,
		TaskTitle:           row.TaskTitle,
		Enabled:             s.Enabled,
		RepeatType:          string(s.RepeatType),
		RepeatInterval:      s.RepeatInterval,
		Recurrence:          FormatRepeatPattern(s.RepeatType, s.RepeatInterval),
		ClearContextOnStart: s.ClearContextOnStart,
	}
	if s.RepeatType == models.RepeatWeekly {
		summary.Days = strings.ToLower(s.RunAt.Local().Weekday().String()[:3])
	}
	if s.NextRun != nil {
		summary.NextRun = s.NextRun.UTC().Format("2006-01-02T15:04:05Z")
	}
	return summary
}
