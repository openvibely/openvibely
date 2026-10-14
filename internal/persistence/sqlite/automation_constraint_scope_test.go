package sqlite_test

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestAutomationCompositeConstraintsAndProjectCascade(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := sqlite.NewProjectRepo(db)
	project := models.Project{Name: "Cascade"}
	require.NoError(t, projectRepo.Create(context.Background(), &project))
	other := models.Project{Name: "Mismatch"}
	require.NoError(t, projectRepo.Create(context.Background(), &other))

	_, err := db.Exec(`INSERT INTO automations (id, project_id, stable_key, name) VALUES ('a', ?, 'a', 'A')`, project.ID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO automation_versions (id, project_id, automation_id, version, adapter_key) VALUES ('v', ?, 'a', 1, 'native_sdlc')`, other.ID)
	require.Error(t, err, "composite parent constraints must reject a foreign project")

	require.NoError(t, projectRepo.Delete(context.Background(), project.ID))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM automations WHERE id = 'a'`).Scan(&count))
	require.Zero(t, count)
}
