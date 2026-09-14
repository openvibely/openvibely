package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

type SkillAnalyticsRepo struct {
	db *sql.DB
}

func NewSkillAnalyticsRepo(db *sql.DB) *SkillAnalyticsRepo {
	return &SkillAnalyticsRepo{db: db}
}

type SkillAnalyticsFilter struct {
	DateFrom            time.Time
	DateTo              time.Time
	ProjectID           string
	AgentID             string
	WorkflowID          string
	Surface             string
	SkillScope          string
	EventType           string
	Limit               int
	GroupBy             string
	EvidencePeriod      string
	EvidenceEvent       string
	EvidenceAgentID     string
	EvidenceSkillHandle string
	EvidenceSkillScope  string
	EvidenceLimit       int
}

type EnabledSkillInfo struct {
	Handle    string
	Scope     string
	Enabled   bool
	AlwaysUse bool
}

func (r *SkillAnalyticsRepo) RecordEvent(ctx context.Context, event *models.SkillAnalyticsEvent) error {
	if r == nil || r.db == nil || event == nil {
		return nil
	}
	event.SkillHandle = strings.TrimSpace(event.SkillHandle)
	if event.SkillHandle == "" || event.EventType == "" {
		return nil
	}
	if event.SkillScope == "" {
		event.SkillScope = models.SkillScopeGlobal
	}
	if event.Source == "" {
		event.Source = models.SkillEventSourceSystem
	}
	if event.Surface == "" {
		event.Surface = models.SkillSurfaceTaskThread
	}
	createdAt := event.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	var createdRaw string
	err := queryRowBoundSQLite(ctx, r.db, `
		INSERT INTO skill_analytics_events (
			id, created_at, project_id, task_id, execution_id, thread_id, agent_id,
			skill_scope, skill_handle, event_type, source, surface
		) VALUES (
			lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		)
		RETURNING id, created_at`,
		formatSQLiteTime(createdAt), nullStringArg(event.ProjectID), nullStringArg(event.TaskID), nullStringArg(event.ExecutionID), nullStringArg(event.ThreadID), nullStringArg(event.AgentID),
		event.SkillScope, event.SkillHandle, event.EventType, event.Source, event.Surface).
		Scan(&event.ID, &createdRaw)
	if err != nil {
		return fmt.Errorf("recording skill analytics event: %w", err)
	}
	event.CreatedAt = parseSQLiteTime(createdRaw)
	return nil
}

func (r *SkillAnalyticsRepo) GetUsageOverTime(ctx context.Context, filter SkillAnalyticsFilter) ([]models.SkillUsagePeriodMetric, error) {
	where, args := skillAnalyticsWhere(filter)
	periodExpr := skillAnalyticsPeriodExpression(filter.GroupBy)
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+periodExpr+` AS period,
		       SUM(CASE WHEN event_type = 'selected' THEN 1 ELSE 0 END) selected_count,
		       SUM(CASE WHEN event_type = 'loaded' THEN 1 ELSE 0 END) loaded_count,
		       SUM(CASE WHEN event_type = 'viewed' THEN 1 ELSE 0 END) viewed_count,
		       SUM(CASE WHEN event_type = 'created' THEN 1 ELSE 0 END) created_count,
		       SUM(CASE WHEN event_type = 'edited' THEN 1 ELSE 0 END) edited_count,
		       COUNT(*) activity_count
		FROM skill_analytics_events e `+where+`
		GROUP BY period
		ORDER BY period ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("getting skill usage over time: %w", err)
	}
	defer rows.Close()
	var out []models.SkillUsagePeriodMetric
	for rows.Next() {
		var m models.SkillUsagePeriodMetric
		if err := rows.Scan(&m.Period, &m.SelectedCount, &m.LoadedCount, &m.ViewedCount, &m.CreatedCount, &m.EditedCount, &m.ActivityCount); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *SkillAnalyticsRepo) GetTopSkills(ctx context.Context, filter SkillAnalyticsFilter) ([]models.SkillAnalyticsSkillMetric, error) {
	where, args := skillAnalyticsWhere(filter)
	limit := filter.Limit
	if limit <= 0 {
		limit = 10
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT skill_handle, skill_scope,
		       SUM(CASE WHEN event_type = 'selected' THEN 1 ELSE 0 END) selected_count,
		       SUM(CASE WHEN event_type = 'loaded' THEN 1 ELSE 0 END) loaded_count,
		       SUM(CASE WHEN event_type = 'viewed' THEN 1 ELSE 0 END) viewed_count,
		       SUM(CASE WHEN event_type = 'created' THEN 1 ELSE 0 END) created_count,
		       SUM(CASE WHEN event_type = 'edited' THEN 1 ELSE 0 END) edited_count,
		       COUNT(*) activity_count,
		       MAX(created_at) last_activity
		FROM skill_analytics_events e `+where+`
		GROUP BY skill_handle, skill_scope
		ORDER BY activity_count DESC, selected_count DESC, skill_handle ASC
		LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("getting top skills: %w", err)
	}
	defer rows.Close()
	var out []models.SkillAnalyticsSkillMetric
	for rows.Next() {
		var m models.SkillAnalyticsSkillMetric
		var lastRaw sql.NullString
		if err := rows.Scan(&m.SkillHandle, &m.SkillScope, &m.SelectedCount, &m.LoadedCount, &m.ViewedCount, &m.CreatedCount, &m.EditedCount, &m.ActivityCount, &lastRaw); err != nil {
			return nil, err
		}
		if m.SelectedCount > 0 {
			rate := float64(m.LoadedCount) / float64(m.SelectedCount)
			m.FollowThroughRate = &rate
		}
		if lastRaw.Valid && strings.TrimSpace(lastRaw.String) != "" {
			last := parseSQLiteTime(lastRaw.String)
			m.LastActivity = &last
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *SkillAnalyticsRepo) GetSelectionFollowThrough(ctx context.Context, filter SkillAnalyticsFilter) ([]models.SkillFollowThroughMetric, error) {
	filter.EventType = ""
	where, args := skillAnalyticsWhere(filter)
	limit := filter.Limit
	if limit <= 0 {
		limit = 10
	}
	rows, err := r.db.QueryContext(ctx, `
		WITH selected_turns AS (
			SELECT skill_handle, skill_scope,
			       COALESCE(NULLIF(execution_id, ''), NULLIF(thread_id, ''), NULLIF(task_id, ''), id) AS turn_key,
			       MAX(CASE WHEN event_type = 'selected' THEN 1 ELSE 0 END) AS selected,
			       MAX(CASE WHEN event_type IN ('loaded','viewed') THEN 1 ELSE 0 END) AS used
			FROM skill_analytics_events e `+where+`
			GROUP BY skill_handle, skill_scope, turn_key
		)
		SELECT skill_handle, skill_scope,
		       SUM(selected) AS selected_count,
		       SUM(CASE WHEN selected = 1 AND used = 1 THEN 1 ELSE 0 END) AS loaded_or_viewed_count,
		       SUM(CASE WHEN selected = 1 AND used = 0 THEN 1 ELSE 0 END) AS ignored_count
		FROM selected_turns
		GROUP BY skill_handle, skill_scope
		HAVING selected_count > 0
		ORDER BY selected_count DESC, ignored_count DESC, skill_handle ASC
		LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("getting skill selection follow-through: %w", err)
	}
	defer rows.Close()
	var out []models.SkillFollowThroughMetric
	for rows.Next() {
		var m models.SkillFollowThroughMetric
		if err := rows.Scan(&m.SkillHandle, &m.SkillScope, &m.SelectedCount, &m.LoadedOrViewed, &m.IgnoredCount); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *SkillAnalyticsRepo) GetAgentUsage(ctx context.Context, filter SkillAnalyticsFilter) (models.SkillAgentUsageHeatmap, error) {
	where, args := skillAnalyticsWhere(filter)
	limit := filter.Limit
	if limit <= 0 {
		limit = 8
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT COALESCE(e.agent_id, ''), COALESCE(a.name, CASE WHEN e.agent_id IS NULL OR e.agent_id = '' THEN 'Unassigned' ELSE e.agent_id END), e.skill_handle, e.skill_scope,
		       SUM(CASE WHEN e.event_type = 'selected' THEN 1 ELSE 0 END) selected_count,
		       SUM(CASE WHEN e.event_type = 'loaded' THEN 1 ELSE 0 END) loaded_count,
		       SUM(CASE WHEN e.event_type = 'viewed' THEN 1 ELSE 0 END) viewed_count,
		       SUM(CASE WHEN e.event_type = 'created' THEN 1 ELSE 0 END) created_count,
		       SUM(CASE WHEN e.event_type = 'edited' THEN 1 ELSE 0 END) edited_count,
		       SUM(CASE WHEN e.event_type IN ('selected','loaded','viewed') THEN 1 ELSE 0 END) activity_count
		FROM skill_analytics_events e
		LEFT JOIN agents a ON a.id = e.agent_id
		`+where+`
		GROUP BY COALESCE(e.agent_id, ''), e.skill_handle, e.skill_scope
		HAVING activity_count > 0
		ORDER BY activity_count DESC, e.skill_handle ASC, e.skill_scope ASC, COALESCE(e.agent_id, '') ASC
		LIMIT ?`, append(args, limit)...)
	if err != nil {
		return models.SkillAgentUsageHeatmap{}, fmt.Errorf("getting agent skill usage: %w", err)
	}
	defer rows.Close()

	heatmap := models.SkillAgentUsageHeatmap{}
	agentSeen := map[string]bool{}
	skillSeen := map[string]bool{}
	legacySeen := map[string]bool{}
	for rows.Next() {
		var cell models.SkillAgentUsageCell
		if err := rows.Scan(&cell.AgentID, &cell.AgentName, &cell.SkillHandle, &cell.SkillScope, &cell.SelectedCount, &cell.LoadedCount, &cell.ViewedCount, &cell.CreatedCount, &cell.EditedCount, &cell.ActivityCount); err != nil {
			return models.SkillAgentUsageHeatmap{}, err
		}
		if !agentSeen[cell.AgentID] {
			agentSeen[cell.AgentID] = true
			heatmap.Agents = append(heatmap.Agents, models.SkillAgentUsageAgent{AgentID: cell.AgentID, AgentName: cell.AgentName})
		}
		pairKey := skillMetricKey(cell.SkillHandle, cell.SkillScope)
		if !skillSeen[pairKey] {
			skillSeen[pairKey] = true
			heatmap.SkillPairs = append(heatmap.SkillPairs, models.SkillAgentUsageSkill{SkillHandle: cell.SkillHandle, SkillScope: cell.SkillScope})
		}
		if !legacySeen[cell.SkillHandle] {
			legacySeen[cell.SkillHandle] = true
			heatmap.Skills = append(heatmap.Skills, cell.SkillHandle)
		}
		heatmap.Cells = append(heatmap.Cells, cell)
	}
	return heatmap, rows.Err()
}

func (r *SkillAnalyticsRepo) GetUnderusedSkills(ctx context.Context, filter SkillAnalyticsFilter, enabledSkills []EnabledSkillInfo) ([]models.UnderusedSkillMetric, error) {
	where, args := skillAnalyticsWhere(filter)
	rows, err := r.db.QueryContext(ctx, `
		SELECT skill_handle, skill_scope,
		       SUM(CASE WHEN event_type = 'selected' THEN 1 ELSE 0 END) selected_count,
		       SUM(CASE WHEN event_type = 'loaded' THEN 1 ELSE 0 END) loaded_count,
		       SUM(CASE WHEN event_type = 'viewed' THEN 1 ELSE 0 END) viewed_count,
		       SUM(CASE WHEN event_type = 'created' THEN 1 ELSE 0 END) created_count,
		       SUM(CASE WHEN event_type = 'edited' THEN 1 ELSE 0 END) edited_count,
		       COUNT(*) activity_count,
		       MAX(created_at) last_activity
		FROM skill_analytics_events e `+where+`
		GROUP BY skill_handle, skill_scope`, args...)
	if err != nil {
		return nil, fmt.Errorf("getting underused skill activity: %w", err)
	}
	activity := map[string]models.UnderusedSkillMetric{}
	for rows.Next() {
		var m models.UnderusedSkillMetric
		var lastRaw sql.NullString
		if err := rows.Scan(&m.SkillHandle, &m.SkillScope, &m.SelectedCount, &m.LoadedCount, &m.ViewedCount, &m.CreatedCount, &m.EditedCount, &m.ActivityCount, &lastRaw); err != nil {
			rows.Close()
			return nil, err
		}
		if lastRaw.Valid && strings.TrimSpace(lastRaw.String) != "" {
			last := parseSQLiteTime(lastRaw.String)
			m.LastActivity = &last
		}
		activity[skillMetricKey(m.SkillHandle, m.SkillScope)] = m
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	bySkill := map[string]models.UnderusedSkillMetric{}
	for _, skill := range enabledSkills {
		handle := strings.TrimSpace(skill.Handle)
		if handle == "" || !skill.Enabled {
			continue
		}
		scope := strings.TrimSpace(skill.Scope)
		if scope == "" {
			scope = models.SkillScopeGlobal
		}
		key := skillMetricKey(handle, scope)
		m := activity[key]
		m.SkillHandle = handle
		m.SkillScope = scope
		m.Enabled = true
		m.AlwaysUse = skill.AlwaysUse
		bySkill[key] = m
	}
	for key, m := range activity {
		if _, ok := bySkill[key]; ok {
			continue
		}
		m.Enabled = false
		bySkill[key] = m
	}

	out := make([]models.UnderusedSkillMetric, 0, len(bySkill))
	for _, m := range bySkill {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Enabled != out[j].Enabled {
			return out[i].Enabled
		}
		if out[i].ActivityCount != out[j].ActivityCount {
			return out[i].ActivityCount < out[j].ActivityCount
		}
		if out[i].LastActivity == nil || out[j].LastActivity == nil {
			return out[i].LastActivity == nil && out[j].LastActivity != nil
		}
		if !out[i].LastActivity.Equal(*out[j].LastActivity) {
			return out[i].LastActivity.Before(*out[j].LastActivity)
		}
		return out[i].SkillHandle < out[j].SkillHandle
	})
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (r *SkillAnalyticsRepo) GetEvidence(ctx context.Context, filter SkillAnalyticsFilter) ([]models.SkillAnalyticsEvidenceRow, int, error) {
	if filter.EvidencePeriod == "" && filter.EvidenceEvent == "" && filter.EvidenceAgentID == "" && filter.EvidenceSkillHandle == "" && filter.EvidenceSkillScope == "" {
		return []models.SkillAnalyticsEvidenceRow{}, 0, nil
	}
	if strings.TrimSpace(filter.ProjectID) == "" {
		return nil, 0, fmt.Errorf("skill Analytics evidence project_id is required")
	}
	where, args := skillAnalyticsWhere(filter)
	if filter.EvidencePeriod != "" {
		where += " AND " + skillAnalyticsPeriodExpression(filter.GroupBy) + " = ?"
		args = append(args, filter.EvidencePeriod)
	}
	if filter.EvidenceEvent == "followed" || filter.EvidenceEvent == "ignored" {
		relatedWhere, relatedArgs := skillAnalyticsWhereAlias(filter, "related")
		relatedTurn := "COALESCE(NULLIF(related.execution_id,''),NULLIF(related.thread_id,''),NULLIF(related.task_id,''),related.id)"
		selectedTurn := "COALESCE(NULLIF(e.execution_id,''),NULLIF(e.thread_id,''),NULLIF(e.task_id,''),e.id)"
		exists := "EXISTS (SELECT 1 FROM skill_analytics_events related " + relatedWhere + " AND related.skill_handle=e.skill_handle AND related.skill_scope=e.skill_scope AND " + relatedTurn + "=" + selectedTurn + " AND related.event_type IN ('loaded','viewed'))"
		where += " AND e.event_type='selected' AND "
		if filter.EvidenceEvent == "ignored" {
			where += "NOT "
		}
		where += exists
		args = append(args, relatedArgs...)
	} else if filter.EvidenceEvent == "used" {
		where += " AND e.event_type IN ('selected','loaded','viewed')"
	} else if filter.EvidenceEvent != "" {
		where += " AND e.event_type = ?"
		args = append(args, filter.EvidenceEvent)
	}
	if filter.EvidenceAgentID == "__unassigned__" {
		where += " AND (e.agent_id IS NULL OR e.agent_id = '')"
	} else if filter.EvidenceAgentID != "" {
		where += " AND e.agent_id = ?"
		args = append(args, filter.EvidenceAgentID)
	}
	if filter.EvidenceSkillHandle != "" {
		where += " AND e.skill_handle = ?"
		args = append(args, filter.EvidenceSkillHandle)
	}
	if filter.EvidenceSkillScope != "" {
		where += " AND e.skill_scope = ?"
		args = append(args, filter.EvidenceSkillScope)
	}
	var total int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM skill_analytics_events e "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting skill Analytics evidence: %w", err)
	}
	limit := filter.EvidenceLimit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `SELECT e.id,e.created_at,COALESCE(e.project_id,''),COALESCE(e.task_id,''),COALESCE(e.execution_id,''),COALESCE(e.agent_id,''),
		COALESCE(a.name,CASE WHEN e.agent_id IS NULL OR e.agent_id='' THEN 'Unassigned' ELSE e.agent_id END),e.skill_handle,e.skill_scope,e.event_type,e.source,e.surface
		FROM skill_analytics_events e LEFT JOIN agents a ON a.id=e.agent_id `+where+` ORDER BY e.created_at DESC,e.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, 0, fmt.Errorf("getting skill Analytics evidence: %w", err)
	}
	defer rows.Close()
	out := []models.SkillAnalyticsEvidenceRow{}
	for rows.Next() {
		var row models.SkillAnalyticsEvidenceRow
		var createdAt string
		if err := rows.Scan(&row.ID, &createdAt, &row.ProjectID, &row.TaskID, &row.ExecutionID, &row.AgentID, &row.AgentName, &row.SkillHandle, &row.SkillScope, &row.EventType, &row.Source, &row.Surface); err != nil {
			return nil, 0, err
		}
		row.CreatedAt = parseSQLiteTime(createdAt)
		out = append(out, row)
	}
	return out, total, rows.Err()
}

func (r *SkillAnalyticsRepo) BuildDashboard(ctx context.Context, filter SkillAnalyticsFilter, enabledSkills []EnabledSkillInfo) (*models.SkillAnalyticsDashboard, error) {
	usage, err := r.GetUsageOverTime(ctx, filter)
	if err != nil {
		return nil, err
	}
	top, err := r.GetTopSkills(ctx, filter)
	if err != nil {
		return nil, err
	}
	follow, err := r.GetSelectionFollowThrough(ctx, filter)
	if err != nil {
		return nil, err
	}
	agent, err := r.GetAgentUsage(ctx, filter)
	if err != nil {
		return nil, err
	}
	underusedFilter := filter
	underusedFilter.Limit = 25
	underused, err := r.GetUnderusedSkills(ctx, underusedFilter, enabledSkills)
	if err != nil {
		return nil, err
	}
	evidence, evidenceTotal, err := r.GetEvidence(ctx, filter)
	if err != nil {
		return nil, err
	}
	return &models.SkillAnalyticsDashboard{UsageOverTime: usage, TopSkills: top, FollowThrough: follow, AgentUsage: agent, Underused: underused, Evidence: evidence, EvidenceTotal: evidenceTotal}, nil
}

func skillAnalyticsWhere(filter SkillAnalyticsFilter) (string, []any) {
	return skillAnalyticsWhereAlias(filter, "e")
}

func skillAnalyticsWhereAlias(filter SkillAnalyticsFilter, alias string) (string, []any) {
	where := "WHERE 1=1"
	args := []any{}
	if !filter.DateFrom.IsZero() {
		where += " AND " + alias + ".created_at >= ?"
		args = append(args, formatSQLiteTime(filter.DateFrom.UTC()))
	}
	if !filter.DateTo.IsZero() {
		where += " AND " + alias + ".created_at < ?"
		args = append(args, formatSQLiteTime(filter.DateTo.UTC()))
	}
	if filter.ProjectID != "" {
		where += " AND " + alias + ".project_id = ?"
		args = append(args, filter.ProjectID)
	}
	if filter.AgentID == "__unassigned__" {
		where += " AND (" + alias + ".agent_id IS NULL OR " + alias + ".agent_id = '')"
	} else if filter.AgentID != "" {
		where += " AND " + alias + ".agent_id = ?"
		args = append(args, filter.AgentID)
	}
	if filter.WorkflowID != "" {
		where += " AND EXISTS (SELECT 1 FROM automation_dispatch_outbox ado JOIN automation_invocations ai ON ai.id=ado.invocation_id WHERE ado.task_id=" + alias + ".task_id AND ai.project_id=" + alias + ".project_id AND ai.automation_id=?)"
		args = append(args, filter.WorkflowID)
	}
	if filter.Surface != "" {
		where += " AND " + alias + ".surface = ?"
		args = append(args, filter.Surface)
	}
	if filter.SkillScope != "" {
		where += " AND " + alias + ".skill_scope = ?"
		args = append(args, filter.SkillScope)
	}
	if filter.EventType != "" {
		where += " AND " + alias + ".event_type = ?"
		args = append(args, filter.EventType)
	}
	return where, args
}

func placeholders(values []string) (string, []any) {
	parts := make([]string, 0, len(values))
	args := make([]any, 0, len(values))
	for _, value := range values {
		parts = append(parts, "?")
		args = append(args, value)
	}
	return strings.Join(parts, ","), args
}

func skillMetricKey(handle, scope string) string {
	return strings.TrimSpace(scope) + "\x00" + strings.TrimSpace(handle)
}

func skillAnalyticsPeriodExpression(groupBy string) string {
	switch groupBy {
	case "week":
		return "strftime('%Y-W%W', e.created_at, 'localtime')"
	case "month":
		return "strftime('%Y-%m', e.created_at, 'localtime')"
	default:
		return "date(e.created_at, 'localtime')"
	}
}
