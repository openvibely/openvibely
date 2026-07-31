package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
)

// AutomationSaveWrite is the complete, already validated state for one atomic
// Automation Save. Graph and resource rows are committed or rolled back together.
type AutomationSaveWrite struct {
	ProjectID              string
	AutomationID           string
	GraphID                string
	ExpectedCurrentGraphID string
	StableKey              string
	Source                 string
	CreatedVia             string
	Candidate              models.AutomationDraftCandidate
	ConfirmationTokenID    string
	ConfirmationPrincipal  string
	ConfirmationThreadID   string
	ConfirmingUserInputID  string
	Tasks                  []AutomationSaveTask
	Schedules              []AutomationSaveSchedule
}

type AutomationSaveTask struct {
	NodeKey           string
	ExistingTaskID    string
	Title             string
	Prompt            string
	Category          models.TaskCategory
	Priority          int
	AgentDefinitionID *string
	ApplyTopology     bool
	ParentNodeKey     string
	ChildNodeKey      string
	ChildTitle        string
	ChildPromptPrefix string
	ChildCategory     models.TaskCategory
}

type AutomationSaveSchedule struct {
	NodeKey             string
	ExistingScheduleID  string
	TaskNodeKey         string
	RunAt               time.Time
	RepeatType          models.RepeatType
	RepeatInterval      int
	Enabled             bool
	ClearContextOnStart bool
	PreserveTiming      bool
}

func (r *AutomationRepo) SaveCurrentGraph(ctx context.Context, in AutomationSaveWrite) (*models.AutomationDefinition, []models.Task, error) {
	if r == nil || r.db == nil {
		return nil, nil, errors.New("automation repository is unavailable")
	}
	if in.ProjectID == "" || in.AutomationID == "" || in.GraphID == "" {
		return nil, nil, errors.New("automation Save identity is incomplete")
	}
	if in.StableKey == "" {
		in.StableKey = "automation/" + in.AutomationID
	}
	if in.Source == "" {
		in.Source = "manual"
	}
	if in.CreatedVia == "" {
		in.CreatedVia = "web"
	}
	for _, schedule := range in.Schedules {
		if schedule.ExistingScheduleID != "" && schedule.PreserveTiming {
			continue
		}
		if err := models.ValidateScheduleRepeatInterval(schedule.RepeatInterval); err != nil {
			return nil, nil, fmt.Errorf("invalid repeat interval for schedule node %q: %w", schedule.NodeKey, err)
		}
	}

	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	if in.ConfirmationTokenID != "" {
		candidateJSON, err := json.Marshal(in.Candidate)
		if err != nil {
			return nil, nil, fmt.Errorf("encoding confirmed Automation graph: %w", err)
		}
		result, err := conn.ExecContext(ctx, `UPDATE automation_chat_confirmation_receipts
			SET confirming_user_input_id = ?, confirmation_method = 'command', consumed_at = CURRENT_TIMESTAMP
			WHERE token_id = ? AND project_id = ? AND principal_id = ? AND thread_id = ?
			  AND automation_name = ? AND source = ? AND candidate_json = ?
			  AND consumed_at IS NULL AND expires_at > CURRENT_TIMESTAMP
			  AND EXISTS (SELECT 1 FROM automation_chat_confirmation_inputs i
				WHERE i.input_id = ? AND i.token_id = automation_chat_confirmation_receipts.token_id
				  AND i.project_id = automation_chat_confirmation_receipts.project_id
				  AND i.principal_id = automation_chat_confirmation_receipts.principal_id
				  AND i.thread_id = automation_chat_confirmation_receipts.thread_id
				  AND i.confirmation_method = 'command')`, in.ConfirmingUserInputID, in.ConfirmationTokenID,
			in.ProjectID, in.ConfirmationPrincipal, in.ConfirmationThreadID, in.Candidate.Name, in.Source,
			string(candidateJSON), in.ConfirmingUserInputID)
		if err != nil {
			return nil, nil, fmt.Errorf("consuming Automation confirmation: %w", err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return nil, nil, errors.New("Automation confirmation is expired, already used, or does not match this Save")
		}
	}

	var automation models.Automation
	err = scanAutomation(conn.QueryRowContext(ctx, `SELECT id, project_id, stable_key, name, description, automation_type,
		lifecycle_state, health_state, health_reason, health_evaluated_at, published_version_id,
		created_via, created_at, updated_at, archived_at FROM automations WHERE project_id = ? AND id = ?`, in.ProjectID, in.AutomationID), &automation)
	newAutomation := errors.Is(err, sql.ErrNoRows)
	if err != nil && !newAutomation {
		return nil, nil, err
	}
	if newAutomation {
		if in.ExpectedCurrentGraphID != "" {
			return nil, nil, errors.New("automation changed before Save")
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO automations
			(id, project_id, stable_key, name, description, automation_type, lifecycle_state, created_via)
			VALUES (?, ?, ?, ?, ?, ?, 'active', ?)`, in.AutomationID, in.ProjectID, in.StableKey,
			in.Candidate.Name, in.Candidate.Description, in.Candidate.AutomationType, in.CreatedVia); err != nil {
			return nil, nil, fmt.Errorf("creating Automation: %w", err)
		}
		automation = models.Automation{ID: in.AutomationID, ProjectID: in.ProjectID, StableKey: in.StableKey,
			Name: in.Candidate.Name, Description: in.Candidate.Description, AutomationType: in.Candidate.AutomationType,
			LifecycleState: models.AutomationActive, CreatedVia: in.CreatedVia}
	} else {
		currentGraphID := ""
		if automation.PublishedVersionID != nil {
			currentGraphID = *automation.PublishedVersionID
		}
		if currentGraphID != in.ExpectedCurrentGraphID {
			return nil, nil, errors.New("automation changed before Save; reopen it and save again")
		}
		if currentGraphID == "" {
			return nil, nil, errors.New("automation has no current saved graph")
		}
		var adapterKey string
		if err := conn.QueryRowContext(ctx, `SELECT adapter_key FROM automation_versions WHERE id = ? AND automation_id = ? AND project_id = ?`,
			currentGraphID, in.AutomationID, in.ProjectID).Scan(&adapterKey); err != nil {
			return nil, nil, err
		}
		if adapterKey != in.Candidate.AdapterKey {
			return nil, nil, fmt.Errorf("saved automation adapter cannot change from %q to %q", adapterKey, in.Candidate.AdapterKey)
		}
	}

	graphSequence := 1
	if in.ExpectedCurrentGraphID != "" {
		if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1
			FROM automation_versions WHERE project_id = ? AND automation_id = ?`, in.ProjectID, in.AutomationID).Scan(&graphSequence); err != nil {
			return nil, nil, fmt.Errorf("selecting current Automation graph sequence: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO automation_versions
		(id, project_id, automation_id, version, state, source, adapter_key, schema_version, published_at)
		VALUES (?, ?, ?, ?, 'published', ?, ?, ?, CURRENT_TIMESTAMP)`, in.GraphID, in.ProjectID, in.AutomationID,
		graphSequence, in.Source, in.Candidate.AdapterKey, in.Candidate.SchemaVersion); err != nil {
		return nil, nil, fmt.Errorf("creating current Automation graph: %w", err)
	}
	if err := writeAutomationGraph(ctx, conn, AutomationGraphWrite{ProjectID: in.ProjectID, AutomationID: in.AutomationID,
		GraphID: in.GraphID, Candidate: in.Candidate}); err != nil {
		return nil, nil, err
	}

	nodeIDs := make(map[string]string, len(in.Candidate.Nodes))
	rows, err := conn.QueryContext(ctx, `SELECT node_key, id FROM automation_nodes WHERE version_id = ?`, in.GraphID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var key, id string
		if err := rows.Scan(&key, &id); err != nil {
			rows.Close()
			return nil, nil, err
		}
		nodeIDs[key] = id
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}

	taskRepo := NewTaskRepo(r.db, nil)
	taskIDs := make(map[string]string, len(in.Tasks))
	for _, write := range in.Tasks {
		if write.ExistingTaskID != "" {
			var projectID string
			if err := conn.QueryRowContext(ctx, `SELECT project_id FROM tasks WHERE id = ?`, write.ExistingTaskID).Scan(&projectID); err != nil {
				return nil, nil, fmt.Errorf("loading bound task for node %q: %w", write.NodeKey, err)
			}
			if projectID != in.ProjectID {
				return nil, nil, fmt.Errorf("task for node %q belongs to another project", write.NodeKey)
			}
			taskIDs[write.NodeKey] = write.ExistingTaskID
			continue
		}
		task := &models.Task{ProjectID: in.ProjectID, Title: write.Title, Prompt: write.Prompt, Category: models.CategoryBacklog,
			Priority: write.Priority, Status: models.StatusPending, AgentDefinitionID: write.AgentDefinitionID,
			CreatedVia: AutomationCompilerTaskCreatedVia(in.AutomationID, write.NodeKey)}
		if err := taskRepo.createWithExecutor(ctx, conn, task); err != nil {
			return nil, nil, fmt.Errorf("creating task for node %q: %w", write.NodeKey, err)
		}
		taskIDs[write.NodeKey] = task.ID
	}

	var runnable []models.Task
	for _, write := range in.Tasks {
		taskID := taskIDs[write.NodeKey]
		category := write.Category
		if automation.LifecycleState != models.AutomationActive && category == models.CategoryActive {
			category = models.CategoryBacklog
		}
		parentID := (*string)(nil)
		chainConfig := "{}"
		status := models.StatusPending
		if write.ApplyTopology {
			if write.ParentNodeKey != "" {
				id := taskIDs[write.ParentNodeKey]
				if id == "" {
					return nil, nil, fmt.Errorf("task node %q has no saved parent", write.NodeKey)
				}
				parentID = &id
				status = models.StatusBlocked
			}
			if write.ChildNodeKey != "" {
				childID := taskIDs[write.ChildNodeKey]
				if childID == "" {
					return nil, nil, fmt.Errorf("task node %q has no saved child", write.NodeKey)
				}
				encoded, err := json.Marshal(models.ChainConfiguration{Enabled: true, Trigger: "on_completion", ChildTaskID: childID,
					ChildAutomationNodeKey: write.ChildNodeKey, ChildTitle: write.ChildTitle, ChildPromptPrefix: write.ChildPromptPrefix,
					ChildCategory: string(write.ChildCategory)})
				if err != nil {
					return nil, nil, err
				}
				chainConfig = string(encoded)
			}
		}
		query := `UPDATE tasks SET title = ?, prompt = ?, category = ?, priority = ?, agent_definition_id = ?, updated_at = CURRENT_TIMESTAMP`
		args := []any{write.Title, write.Prompt, category, write.Priority, write.AgentDefinitionID}
		if write.ApplyTopology {
			query += `, parent_task_id = ?, chain_config = ?, status = CASE WHEN status IN ('running','queued') THEN status ELSE ? END`
			args = append(args, parentID, chainConfig, status)
		}
		query += ` WHERE id = ? AND project_id = ?`
		args = append(args, taskID, in.ProjectID)
		result, err := conn.ExecContext(ctx, query, args...)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed: tasks.project_id, tasks.title") {
				return nil, nil, ErrDuplicateTask
			}
			return nil, nil, fmt.Errorf("saving task for node %q: %w", write.NodeKey, err)
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return nil, nil, fmt.Errorf("task for node %q is unavailable", write.NodeKey)
		}
		if nodeID := nodeIDs[write.NodeKey]; nodeID != "" {
			if _, err := conn.ExecContext(ctx, `INSERT INTO automation_definition_resources
				(project_id, automation_id, version_id, node_id, resource_type, resource_id, relation)
				VALUES (?, ?, ?, ?, 'task', ?, 'owned')`, in.ProjectID, in.AutomationID, in.GraphID, nodeID, taskID); err != nil {
				return nil, nil, err
			}
		}
		var storedCategory models.TaskCategory
		var storedStatus models.TaskStatus
		var storedParentID sql.NullString
		if err := conn.QueryRowContext(ctx, `SELECT category, status, parent_task_id FROM tasks WHERE id = ? AND project_id = ?`, taskID, in.ProjectID).
			Scan(&storedCategory, &storedStatus, &storedParentID); err != nil {
			return nil, nil, fmt.Errorf("loading saved task for node %q: %w", write.NodeKey, err)
		}
		if automation.LifecycleState == models.AutomationActive && storedCategory == models.CategoryActive && storedStatus == models.StatusPending && !storedParentID.Valid {
			runnable = append(runnable, models.Task{ID: taskID, ProjectID: in.ProjectID, Title: write.Title, Prompt: write.Prompt,
				Category: storedCategory, Priority: write.Priority, Status: storedStatus, AgentDefinitionID: write.AgentDefinitionID,
				ChainConfig: chainConfig})
		}
	}

	for _, write := range in.Schedules {
		taskID := taskIDs[write.TaskNodeKey]
		if taskID == "" {
			return nil, nil, fmt.Errorf("schedule node %q has no saved task", write.NodeKey)
		}
		enabled := automation.LifecycleState == models.AutomationActive && write.Enabled
		scheduleID := write.ExistingScheduleID
		if scheduleID == "" {
			nextRun := write.RunAt
			if err := conn.QueryRowContext(ctx, `INSERT INTO schedules
				(id, task_id, run_at, repeat_type, repeat_interval, enabled, clear_context_on_start, next_run)
				VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?)
				RETURNING id`, taskID, write.RunAt, write.RepeatType, write.RepeatInterval, enabled, write.ClearContextOnStart, nextRun).Scan(&scheduleID); err != nil {
				return nil, nil, fmt.Errorf("creating schedule for node %q: %w", write.NodeKey, err)
			}
		} else {
			var ownerAutomationID, ownerProjectID string
			if err := conn.QueryRowContext(ctx, `SELECT automation_id, project_id FROM automation_trigger_owners WHERE schedule_id = ?`, scheduleID).
				Scan(&ownerAutomationID, &ownerProjectID); err != nil {
				return nil, nil, fmt.Errorf("loading schedule ownership for node %q: %w", write.NodeKey, err)
			}
			if ownerAutomationID != in.AutomationID || ownerProjectID != in.ProjectID {
				return nil, nil, fmt.Errorf("schedule for node %q is not owned by this Automation", write.NodeKey)
			}
			if write.PreserveTiming {
				if _, err := conn.ExecContext(ctx, `UPDATE schedules SET task_id = ?, enabled = ?, clear_context_on_start = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
					taskID, enabled, write.ClearContextOnStart, scheduleID); err != nil {
					return nil, nil, fmt.Errorf("updating schedule for node %q: %w", write.NodeKey, err)
				}
			} else if _, err := conn.ExecContext(ctx, `UPDATE schedules SET task_id = ?, run_at = ?, repeat_type = ?, repeat_interval = ?, enabled = ?,
				clear_context_on_start = ?, next_run = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, taskID, write.RunAt, write.RepeatType,
				write.RepeatInterval, enabled, write.ClearContextOnStart, write.RunAt, scheduleID); err != nil {
				return nil, nil, fmt.Errorf("updating schedule for node %q: %w", write.NodeKey, err)
			}
		}
		nodeID := nodeIDs[write.NodeKey]
		if nodeID == "" {
			return nil, nil, fmt.Errorf("schedule node %q is unavailable", write.NodeKey)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO automation_definition_resources
			(project_id, automation_id, version_id, node_id, resource_type, resource_id, relation)
			VALUES (?, ?, ?, ?, 'schedule', ?, 'owned')`, in.ProjectID, in.AutomationID, in.GraphID, nodeID, scheduleID); err != nil {
			return nil, nil, err
		}
		ownershipState := "active"
		if automation.LifecycleState == models.AutomationPaused {
			ownershipState = "paused"
		} else if automation.LifecycleState == models.AutomationArchived {
			ownershipState = "archived"
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO automation_trigger_owners
			(schedule_id, project_id, automation_id, version_id, node_id, ownership_state)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(schedule_id) DO UPDATE SET version_id = excluded.version_id,
			node_id = excluded.node_id, ownership_state = excluded.ownership_state, updated_at = CURRENT_TIMESTAMP`, scheduleID,
			in.ProjectID, in.AutomationID, in.GraphID, nodeID, ownershipState); err != nil {
			return nil, nil, err
		}
	}

	if err := deleteObsoleteOwnedAutomationSchedules(ctx, conn, in.ProjectID, in.AutomationID, in.GraphID); err != nil {
		return nil, nil, err
	}

	if _, err := conn.ExecContext(ctx, `UPDATE automations SET name = ?, description = ?, automation_type = ?, lifecycle_state = ?,
		published_version_id = ?, archived_at = CASE WHEN ? = 'archived' THEN archived_at ELSE NULL END, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND project_id = ?`, in.Candidate.Name, in.Candidate.Description, in.Candidate.AutomationType,
		automation.LifecycleState, in.GraphID, automation.LifecycleState, in.AutomationID, in.ProjectID); err != nil {
		return nil, nil, err
	}
	if in.ExpectedCurrentGraphID != "" {
		if err := backfillLegacyGitHubIssueTaskOrigins(ctx, conn, in.ProjectID, in.AutomationID, in.ExpectedCurrentGraphID); err != nil {
			return nil, nil, err
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM automation_versions WHERE id <> ? AND automation_id = ? AND project_id = ?`,
			in.GraphID, in.AutomationID, in.ProjectID); err != nil {
			return nil, nil, err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE automation_versions SET version = 1 WHERE id = ? AND automation_id = ? AND project_id = ?`,
			in.GraphID, in.AutomationID, in.ProjectID); err != nil {
			return nil, nil, err
		}
	}

	if err := scanAutomation(conn.QueryRowContext(ctx, `SELECT id, project_id, stable_key, name, description, automation_type,
		lifecycle_state, health_state, health_reason, health_evaluated_at, published_version_id,
		created_via, created_at, updated_at, archived_at FROM automations WHERE project_id = ? AND id = ?`, in.ProjectID, in.AutomationID), &automation); err != nil {
		return nil, nil, err
	}
	definition, err := r.loadDefinition(ctx, conn, automation, in.GraphID)
	if err != nil {
		return nil, nil, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, nil, err
	}
	committed = true

	r.PublishInvalidation(events.AutomationDefinitionUpdated, in.ProjectID, models.AutomationBinding{AutomationID: in.AutomationID, VersionID: in.GraphID})
	return definition, runnable, nil
}

func backfillLegacyGitHubIssueTaskOrigins(ctx context.Context, exec SQLExecutor, projectID, automationID, versionID string) error {
	_, err := exec.ExecContext(ctx, `UPDATE tasks
		SET created_via = (
			SELECT 'automation:' || activity.automation_id || ':' || node.node_key
			FROM automation_activity_resources resource
			JOIN automation_activities activity ON activity.id = resource.activity_id
			JOIN automation_nodes node ON node.id = activity.node_id
				AND node.version_id = activity.version_id AND node.automation_id = activity.automation_id
				AND node.project_id = activity.project_id
			WHERE resource.resource_type = 'task' AND resource.resource_id = tasks.id AND resource.relation = 'child'
				AND activity.project_id = ? AND activity.automation_id = ? AND activity.version_id = ?
				AND activity.activity_type = 'create_task' AND activity.work_item_id IS NOT NULL
				AND activity.activity_key = 'work-item:' || activity.work_item_id || ':implementation-task'
				AND node.node_type = 'agent_task' AND node.role IN ('task', 'implementation')
			ORDER BY activity.started_at, activity.id LIMIT 1)
		WHERE project_id = ? AND trim(COALESCE(created_via, '')) = ''
			AND EXISTS (
				SELECT 1 FROM automation_activity_resources resource
				JOIN automation_activities activity ON activity.id = resource.activity_id
				JOIN automation_nodes node ON node.id = activity.node_id
					AND node.version_id = activity.version_id AND node.automation_id = activity.automation_id
					AND node.project_id = activity.project_id
				WHERE resource.resource_type = 'task' AND resource.resource_id = tasks.id AND resource.relation = 'child'
					AND activity.project_id = ? AND activity.automation_id = ? AND activity.version_id = ?
					AND activity.activity_type = 'create_task' AND activity.work_item_id IS NOT NULL
					AND activity.activity_key = 'work-item:' || activity.work_item_id || ':implementation-task'
					AND node.node_type = 'agent_task' AND node.role IN ('task', 'implementation'))`,
		projectID, automationID, versionID, projectID, projectID, automationID, versionID)
	if err != nil {
		return fmt.Errorf("backfilling legacy Automation GitHub issue task origins: %w", err)
	}
	return nil
}
