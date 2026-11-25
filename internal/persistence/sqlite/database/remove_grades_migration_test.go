package database

import (
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestRemoveGradesMigration(t *testing.T) {
	goose.SetBaseFS(migrations.FS)
	defer goose.SetBaseFS(nil)
	require.NoError(t, goose.SetDialect("sqlite3"))

	for _, existing := range []bool{false, true} {
		name := "fresh"
		if existing {
			name = "existing grades"
		}
		t.Run(name, func(t *testing.T) {
			db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "grades.db"))
			if existing {
				require.NoError(t, goose.UpTo(db, ".", 239))
				_, err := db.Exec(`
INSERT INTO projects(id,name) VALUES ('grades-project','Grades project');
INSERT INTO health_checks(id,project_id,grade,assessment) VALUES
 ('health-default','default','B','Old feedback'),('health-project','grades-project','A','Other feedback');
INSERT INTO idea_grades(id,project_id,grade,summary) VALUES
 ('ideas-default','default','C','Old ideas'),('ideas-project','grades-project','B','Other ideas');
INSERT INTO insights(id,project_id,type,title) VALUES ('kept-insight','grades-project','tech_debt','Still useful');
INSERT INTO insight_reports(id,project_id,report_date,summary) VALUES ('kept-report','grades-project','2026-01-01','Keep this report');
`)
				require.NoError(t, err)
			}

			require.NoError(t, goose.Up(db, "."))
			assertRemoved := func() {
				t.Helper()
				var count int
				require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE tbl_name IN ('health_checks','idea_grades')`).Scan(&count))
				require.Zero(t, count, "grading tables and indexes must be gone")
			}
			assertRemoved()
			if existing {
				for _, query := range []string{
					`SELECT COUNT(*) FROM projects WHERE id='grades-project'`,
					`SELECT COUNT(*) FROM insights WHERE id='kept-insight' AND title='Still useful'`,
					`SELECT COUNT(*) FROM insight_reports WHERE id='kept-report' AND summary='Keep this report'`,
				} {
					var count int
					require.NoError(t, db.QueryRow(query).Scan(&count))
					require.Equal(t, 1, count)
				}
			}

			// Restarting migration processing must remain safe.
			require.NoError(t, goose.Up(db, "."))
			assertRemoved()
			// Rollback restores empty schemas, not deleted grades.
			require.NoError(t, goose.DownTo(db, ".", 239))
			for _, table := range []string{"health_checks", "idea_grades"} {
				var count int
				require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&count))
				require.Zero(t, count)
				require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND tbl_name=? AND sql IS NOT NULL`, table).Scan(&count))
				require.Equal(t, 2, count)
			}
			require.NoError(t, goose.Up(db, "."))
			assertRemoved()
			rows, err := db.Query(`PRAGMA foreign_key_check`)
			require.NoError(t, err)
			defer rows.Close()
			require.False(t, rows.Next())
			require.NoError(t, rows.Err())
		})
	}
}
