package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

type ArchitectRepo struct {
	db *sql.DB
}

func NewArchitectRepo(db *sql.DB) *ArchitectRepo {
	return &ArchitectRepo{db: db}
}

// Sessions

func (r *ArchitectRepo) CreateSession(ctx context.Context, session *models.ArchitectSession) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO architect_sessions (project_id, title, description, status, phase, vision_data, arch_data, risk_data, phase_data, dep_data, est_data, template_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at`,
		session.ProjectID, session.Title, session.Description, session.Status, session.Phase,
		session.VisionData, session.ArchData, session.RiskData, session.PhaseData, session.DepData, session.EstData,
		session.TemplateID,
	).Scan(&session.ID, &session.CreatedAt, &session.UpdatedAt)
}

func (r *ArchitectRepo) GetSession(ctx context.Context, id string) (*models.ArchitectSession, error) {
	var s models.ArchitectSession
	var templateID sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT id, project_id, title, description, status, phase,
			vision_data, arch_data, risk_data, phase_data, dep_data, est_data,
			template_id, created_at, updated_at
		FROM architect_sessions WHERE id = ?`, id,
	).Scan(&s.ID, &s.ProjectID, &s.Title, &s.Description, &s.Status, &s.Phase,
		&s.VisionData, &s.ArchData, &s.RiskData, &s.PhaseData, &s.DepData, &s.EstData,
		&templateID, &s.CreatedAt, &s.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting session: %w", err)
	}
	if templateID.Valid {
		s.TemplateID = &templateID.String
	}
	return &s, nil
}

func (r *ArchitectRepo) UpdateSession(ctx context.Context, session *models.ArchitectSession) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE architect_sessions
		SET title = ?, description = ?, status = ?, phase = ?,
			vision_data = ?, arch_data = ?, risk_data = ?, phase_data = ?,
			dep_data = ?, est_data = ?, updated_at = datetime('now')
		WHERE id = ?`,
		session.Title, session.Description, session.Status, session.Phase,
		session.VisionData, session.ArchData, session.RiskData, session.PhaseData,
		session.DepData, session.EstData, session.ID)
	if err != nil {
		return fmt.Errorf("updating session: %w", err)
	}
	return nil
}

func (r *ArchitectRepo) DeleteSession(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM architect_sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	return nil
}

func (r *ArchitectRepo) ListSessionsByProject(ctx context.Context, projectID string, status models.ArchitectSessionStatus) ([]models.ArchitectSession, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, title, description, status, phase,
			vision_data, arch_data, risk_data, phase_data, dep_data, est_data,
			template_id, created_at, updated_at
		FROM architect_sessions
		WHERE project_id = ? AND status = ?
		ORDER BY updated_at DESC`, projectID, status)
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	defer rows.Close()

	var sessions []models.ArchitectSession
	for rows.Next() {
		var s models.ArchitectSession
		var templateID sql.NullString
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.Title, &s.Description, &s.Status, &s.Phase,
			&s.VisionData, &s.ArchData, &s.RiskData, &s.PhaseData, &s.DepData, &s.EstData,
			&templateID, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning session: %w", err)
		}
		if templateID.Valid {
			s.TemplateID = &templateID.String
		}
		sessions = append(sessions, s)
	}
	if sessions == nil {
		sessions = []models.ArchitectSession{}
	}
	return sessions, rows.Err()
}

// Messages

func (r *ArchitectRepo) CreateMessage(ctx context.Context, msg *models.ArchitectMessage) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO architect_messages (session_id, role, content, phase)
		VALUES (?, ?, ?, ?)
		RETURNING id, created_at`,
		msg.SessionID, msg.Role, msg.Content, msg.Phase,
	).Scan(&msg.ID, &msg.CreatedAt)
}

func (r *ArchitectRepo) ListMessages(ctx context.Context, sessionID string) ([]models.ArchitectMessage, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, session_id, role, content, phase, created_at
		FROM architect_messages
		WHERE session_id = ?
		ORDER BY created_at ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing messages: %w", err)
	}
	defer rows.Close()

	var messages []models.ArchitectMessage
	for rows.Next() {
		var m models.ArchitectMessage
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.Phase, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning message: %w", err)
		}
		messages = append(messages, m)
	}
	if messages == nil {
		messages = []models.ArchitectMessage{}
	}
	return messages, rows.Err()
}

// Tasks

func (r *ArchitectRepo) CreateTask(ctx context.Context, task *models.ArchitectTask) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO architect_tasks (session_id, title, prompt, phase, priority, depends_on, is_blocking, complexity, est_hours)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at`,
		task.SessionID, task.Title, task.Prompt, task.Phase, task.Priority,
		task.DependsOn, boolToInt(task.IsBlocking), task.Complexity, task.EstHours,
	).Scan(&task.ID, &task.CreatedAt)
}

func (r *ArchitectRepo) ListTasksBySession(ctx context.Context, sessionID string) ([]models.ArchitectTask, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, session_id, title, prompt, phase, priority, depends_on,
			is_blocking, complexity, est_hours, task_id, is_activated, created_at
		FROM architect_tasks
		WHERE session_id = ?
		ORDER BY phase, priority DESC, created_at ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}
	defer rows.Close()

	var tasks []models.ArchitectTask
	for rows.Next() {
		var t models.ArchitectTask
		var taskID sql.NullString
		var isBlocking, isActivated int
		if err := rows.Scan(&t.ID, &t.SessionID, &t.Title, &t.Prompt, &t.Phase, &t.Priority,
			&t.DependsOn, &isBlocking, &t.Complexity, &t.EstHours, &taskID, &isActivated, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}
		t.IsBlocking = isBlocking != 0
		t.IsActivated = isActivated != 0
		if taskID.Valid {
			t.TaskID = &taskID.String
		}
		tasks = append(tasks, t)
	}
	if tasks == nil {
		tasks = []models.ArchitectTask{}
	}
	return tasks, rows.Err()
}

func (r *ArchitectRepo) ListTasksByPhase(ctx context.Context, sessionID string, phase models.ArchitectTaskPhase) ([]models.ArchitectTask, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, session_id, title, prompt, phase, priority, depends_on,
			is_blocking, complexity, est_hours, task_id, is_activated, created_at
		FROM architect_tasks
		WHERE session_id = ? AND phase = ?
		ORDER BY priority DESC, created_at ASC`, sessionID, phase)
	if err != nil {
		return nil, fmt.Errorf("listing tasks by phase: %w", err)
	}
	defer rows.Close()

	var tasks []models.ArchitectTask
	for rows.Next() {
		var t models.ArchitectTask
		var taskID sql.NullString
		var isBlocking, isActivated int
		if err := rows.Scan(&t.ID, &t.SessionID, &t.Title, &t.Prompt, &t.Phase, &t.Priority,
			&t.DependsOn, &isBlocking, &t.Complexity, &t.EstHours, &taskID, &isActivated, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}
		t.IsBlocking = isBlocking != 0
		t.IsActivated = isActivated != 0
		if taskID.Valid {
			t.TaskID = &taskID.String
		}
		tasks = append(tasks, t)
	}
	if tasks == nil {
		tasks = []models.ArchitectTask{}
	}
	return tasks, rows.Err()
}

func (r *ArchitectRepo) ActivateTask(ctx context.Context, architectTaskID string, realTaskID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE architect_tasks SET task_id = ?, is_activated = 1 WHERE id = ?`,
		realTaskID, architectTaskID)
	if err != nil {
		return fmt.Errorf("activating task: %w", err)
	}
	return nil
}

func (r *ArchitectRepo) DeleteTasksBySession(ctx context.Context, sessionID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM architect_tasks WHERE session_id = ?`, sessionID)
	if err != nil {
		return fmt.Errorf("deleting tasks: %w", err)
	}
	return nil
}

// Templates

func (r *ArchitectRepo) CreateTemplate(ctx context.Context, tmpl *models.ArchitectTemplate) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO architect_templates (name, description, category, vision_data, arch_data, tasks_data)
		VALUES (?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at`,
		tmpl.Name, tmpl.Description, tmpl.Category, tmpl.VisionData, tmpl.ArchData, tmpl.TasksData,
	).Scan(&tmpl.ID, &tmpl.CreatedAt, &tmpl.UpdatedAt)
}

func (r *ArchitectRepo) GetTemplate(ctx context.Context, id string) (*models.ArchitectTemplate, error) {
	var t models.ArchitectTemplate
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, category, vision_data, arch_data, tasks_data, usage_count, created_at, updated_at
		FROM architect_templates WHERE id = ?`, id,
	).Scan(&t.ID, &t.Name, &t.Description, &t.Category, &t.VisionData, &t.ArchData, &t.TasksData,
		&t.UsageCount, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting template: %w", err)
	}
	return &t, nil
}

func (r *ArchitectRepo) ListTemplates(ctx context.Context) ([]models.ArchitectTemplate, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, category, vision_data, arch_data, tasks_data, usage_count, created_at, updated_at
		FROM architect_templates
		ORDER BY usage_count DESC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing templates: %w", err)
	}
	defer rows.Close()

	var templates []models.ArchitectTemplate
	for rows.Next() {
		var t models.ArchitectTemplate
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Category, &t.VisionData, &t.ArchData, &t.TasksData,
			&t.UsageCount, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning template: %w", err)
		}
		templates = append(templates, t)
	}
	if templates == nil {
		templates = []models.ArchitectTemplate{}
	}
	return templates, rows.Err()
}

func (r *ArchitectRepo) IncrementTemplateUsage(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE architect_templates SET usage_count = usage_count + 1, updated_at = datetime('now') WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("incrementing template usage: %w", err)
	}
	return nil
}

func (r *ArchitectRepo) DeleteTemplate(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM architect_templates WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting template: %w", err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
