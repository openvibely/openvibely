package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/openvibely/openvibely/internal/pageintegration"

	"github.com/openvibely/openvibely/internal/models"
)

type PagePluginRepo struct {
	db *sql.DB
}

func NewPagePluginRepo(db *sql.DB) *PagePluginRepo { return &PagePluginRepo{db: db} }

const pagePluginPackageColumns = `id, plugin_id, version, name, content_hash, manifest_json, install_path, installed_by, installed_at`

func scanPagePluginPackage(scan func(...any) error) (*models.PagePluginPackage, error) {
	var p models.PagePluginPackage
	if err := scan(&p.ID, &p.PluginID, &p.Version, &p.Name, &p.ContentHash, &p.ManifestJSON, &p.InstallPath, &p.InstalledBy, &p.InstalledAt); err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *PagePluginRepo) CreatePackage(ctx context.Context, p *models.PagePluginPackage) error {
	return queryRowBoundSQLite(ctx, r.db, `INSERT INTO page_plugin_packages (plugin_id, version, name, content_hash, manifest_json, install_path, installed_by)
		VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id, installed_at`,
		p.PluginID, p.Version, p.Name, p.ContentHash, p.ManifestJSON, p.InstallPath, p.InstalledBy).Scan(&p.ID, &p.InstalledAt)
}

func (r *PagePluginRepo) GetPackage(ctx context.Context, id string) (*models.PagePluginPackage, error) {
	p, err := scanPagePluginPackage(r.db.QueryRowContext(ctx, `SELECT `+pagePluginPackageColumns+` FROM page_plugin_packages WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func (r *PagePluginRepo) GetPackageByVersion(ctx context.Context, pluginID, version string) (*models.PagePluginPackage, error) {
	p, err := scanPagePluginPackage(r.db.QueryRowContext(ctx, `SELECT `+pagePluginPackageColumns+` FROM page_plugin_packages WHERE plugin_id = ? AND version = ?`, pluginID, version).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func (r *PagePluginRepo) ListPackages(ctx context.Context) ([]models.PagePluginPackage, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+pagePluginPackageColumns+` FROM page_plugin_packages ORDER BY plugin_id, installed_at`)
	if err != nil {
		return nil, fmt.Errorf("listing page plugin packages: %w", err)
	}
	defer rows.Close()
	var out []models.PagePluginPackage
	for rows.Next() {
		p, err := scanPagePluginPackage(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (r *PagePluginRepo) DeletePackage(ctx context.Context, id string) error {
	_, err := execBoundSQLite(ctx, r.db, `DELETE FROM page_plugin_packages WHERE id = ?`, id)
	return err
}

func (r *PagePluginRepo) CountInstancesForPackage(ctx context.Context, packageID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_plugin_instances WHERE package_id = ? OR id IN (SELECT instance_id FROM page_plugin_replacements WHERE package_id = ?)`, packageID, packageID).Scan(&n)
	return n, err
}

const pagePluginInstanceSelect = `SELECT i.id, i.project_id, i.package_id, i.page_id, i.title, i.enabled, i.config_json, i.state_json,
	i.approved_hash, i.approved_by, i.approved_at, i.created_at, i.updated_at, p.plugin_id, p.version, p.content_hash, COALESCE(pr.name, '')
	FROM page_plugin_instances i
	JOIN page_plugin_packages p ON p.id = i.package_id
	LEFT JOIN projects pr ON pr.id = i.project_id`

func scanPagePluginInstance(scan func(...any) error) (*models.PagePluginInstance, error) {
	var in models.PagePluginInstance
	var approvedAt sql.NullTime
	if err := scan(&in.ID, &in.ProjectID, &in.PackageID, &in.PageID, &in.Title, &in.Enabled, &in.ConfigJSON, &in.StateJSON,
		&in.ApprovedHash, &in.ApprovedBy, &approvedAt, &in.CreatedAt, &in.UpdatedAt, &in.PluginID, &in.Version, &in.ContentHash, &in.ProjectName); err != nil {
		return nil, err
	}
	if approvedAt.Valid {
		t := approvedAt.Time
		in.ApprovedAt = &t
	}
	return &in, nil
}

func (r *PagePluginRepo) CreateInstance(ctx context.Context, in *models.PagePluginInstance) error {
	if in.ConfigJSON == "" {
		in.ConfigJSON = "{}"
	}
	if in.StateJSON == "" {
		in.StateJSON = "{}"
	}
	return queryRowBoundSQLite(ctx, r.db, `INSERT INTO page_plugin_instances (project_id, package_id, page_id, title, enabled, config_json, state_json)
		VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id, created_at, updated_at`,
		in.ProjectID, in.PackageID, in.PageID, in.Title, in.Enabled, in.ConfigJSON, in.StateJSON).Scan(&in.ID, &in.CreatedAt, &in.UpdatedAt)
}

func (r *PagePluginRepo) GetInstance(ctx context.Context, id string) (*models.PagePluginInstance, error) {
	in, err := scanPagePluginInstance(r.db.QueryRowContext(ctx, pagePluginInstanceSelect+` WHERE i.id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return in, err
}

func (r *PagePluginRepo) listInstances(ctx context.Context, where string, args ...any) ([]models.PagePluginInstance, error) {
	rows, err := r.db.QueryContext(ctx, pagePluginInstanceSelect+where+` ORDER BY pr.name, i.title, i.created_at`, args...)
	if err != nil {
		return nil, fmt.Errorf("listing page plugin instances: %w", err)
	}
	defer rows.Close()
	var out []models.PagePluginInstance
	for rows.Next() {
		in, err := scanPagePluginInstance(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *in)
	}
	return out, rows.Err()
}

func (r *PagePluginRepo) ListInstances(ctx context.Context, projectID string, enabledOnly bool) ([]models.PagePluginInstance, error) {
	return r.listInstances(ctx, ` WHERE i.project_id = ? AND (? = 0 OR i.enabled = 1)`, projectID, enabledOnly)
}

func (r *PagePluginRepo) ListAllInstances(ctx context.Context, enabledOnly bool) ([]models.PagePluginInstance, error) {
	return r.listInstances(ctx, ` WHERE (? = 0 OR i.enabled = 1)`, enabledOnly)
}

func (r *PagePluginRepo) SetInstanceEnabled(ctx context.Context, id string, enabled bool) error {
	_, err := execBoundSQLite(ctx, r.db, `UPDATE page_plugin_instances SET enabled = ?, updated_at = datetime('now') WHERE id = ?`, enabled, id)
	return err
}

func (r *PagePluginRepo) ApproveInstance(ctx context.Context, id, contentHash, approvedBy string) error {
	_, err := execBoundSQLite(ctx, r.db, `UPDATE page_plugin_instances SET approved_hash = ?, approved_by = ?, approved_at = datetime('now'), updated_at = datetime('now') WHERE id = ?`, contentHash, approvedBy, id)
	return err
}

// ApproveInstanceIfContent approves only while the instance's package still has
// contentHash, so a concurrent package swap cannot inherit the approval.
func (r *PagePluginRepo) ApproveInstanceIfContent(ctx context.Context, id, contentHash, approvedBy string) (bool, error) {
	res, err := execBoundSQLite(ctx, r.db, `UPDATE page_plugin_instances SET approved_hash = ?, approved_by = ?, approved_at = datetime('now'), updated_at = datetime('now')
 WHERE id = ? AND (SELECT p.content_hash FROM page_plugin_packages p WHERE p.id = page_plugin_instances.package_id) = ?`, contentHash, approvedBy, id, contentHash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *PagePluginRepo) UpdateInstanceState(ctx context.Context, id, stateJSON string) error {
	_, err := execBoundSQLite(ctx, r.db, `UPDATE page_plugin_instances SET state_json = ?, updated_at = datetime('now') WHERE id = ?`, stateJSON, id)
	return err
}

// DeleteInstance also deletes the side-chat backing task, which cascades its executions.
func (r *PagePluginRepo) DeleteInstance(ctx context.Context, id string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE id IN (SELECT task_id FROM page_conversations WHERE kind = 'plugin' AND instance_id = ?)`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM page_plugin_instances WHERE id = ?`, id)
		return err
	})
}

func (r *PagePluginRepo) GetConversationTask(ctx context.Context, instanceID string) (string, error) {
	var taskID string
	err := r.db.QueryRowContext(ctx, `SELECT task_id FROM page_conversations WHERE kind = 'plugin' AND instance_id = ?`, instanceID).Scan(&taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return taskID, err
}

func (r *PagePluginRepo) conversationStore(ctx context.Context, instanceID string) (pageintegration.ConversationStore, error) {
	in, err := r.GetInstance(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	if in == nil {
		return nil, errors.New("page instance not found")
	}
	return NewPageConversationRepo(r.db).ForPage(pageintegration.PageRef{Kind: "plugin", InstanceID: in.ID, PageID: in.PageID, ProjectID: in.ProjectID}), nil
}
func (r *PagePluginRepo) SetConversationTask(ctx context.Context, instanceID, taskID string) error {
	store, err := r.conversationStore(ctx, instanceID)
	if err != nil {
		return err
	}
	return store.BindTask(ctx, taskID)
}
func (r *PagePluginRepo) SaveTurnContext(ctx context.Context, executionID, instanceID, raw string) error {
	store, err := r.conversationStore(ctx, instanceID)
	if err != nil {
		return err
	}
	return store.SaveSnapshot(ctx, executionID, raw)
}

func (r *PagePluginRepo) GetTurnContext(ctx context.Context, executionID string) (string, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `SELECT snapshot_json FROM page_turn_contexts WHERE execution_id = ?`, executionID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return raw, err
}

// StageReplacement retains only the information required to undo an unfinished
// update. Instance identity, data and conversation associations never change.
func (r *PagePluginRepo) StageReplacement(ctx context.Context, id, oldID, newID string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO page_plugin_replacements (instance_id,package_id,approved_hash,approved_by,approved_at,was_enabled)
   SELECT id,package_id,approved_hash,approved_by,approved_at,enabled FROM page_plugin_instances WHERE id=? AND package_id=?`, id, oldID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("page changed during update")
		}
		_, err = tx.ExecContext(ctx, `UPDATE page_plugin_instances SET package_id=?,enabled=0,approved_hash='',approved_by='',approved_at=NULL,updated_at=datetime('now') WHERE id=?`, newID, id)
		return err
	})
}
func (r *PagePluginRepo) GetReplacement(ctx context.Context, id string) (*models.PagePluginInstance, error) {
	var in models.PagePluginInstance
	err := r.db.QueryRowContext(ctx, `SELECT instance_id,package_id,was_enabled FROM page_plugin_replacements WHERE instance_id=?`, id).Scan(&in.ID, &in.PackageID, &in.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &in, err
}
func (r *PagePluginRepo) FinishReplacement(ctx context.Context, id string, restore bool) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		if restore {
			_, err := tx.ExecContext(ctx, `UPDATE page_plugin_instances SET package_id=b.package_id, approved_hash=b.approved_hash,approved_by=b.approved_by,approved_at=b.approved_at,enabled=0,updated_at=datetime('now') FROM page_plugin_replacements b WHERE page_plugin_instances.id=b.instance_id AND b.instance_id=?`, id)
			if err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM page_plugin_replacements WHERE instance_id=?`, id)
		return err
	})
}
