package sqlite

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestInsightsResultWritesRecheckActorAndAuditAtomically(t *testing.T) {
	cases := []struct {
		table, action string
		write         func(context.Context, *InsightsRepo) error
	}{
		{"insights", "insight.created", func(ctx context.Context, r *InsightsRepo) error {
			return r.CreateInsight(ctx, &models.Insight{ProjectID: "default", Type: models.InsightBugPattern, Severity: models.InsightSeverityHigh, Status: models.InsightStatusNew, Title: "private generated text"})
		}},
		{"insight_reports", "insight.report_created", func(ctx context.Context, r *InsightsRepo) error {
			return r.CreateReport(ctx, &models.InsightReport{ProjectID: "default", ReportDate: "2026-10-30", Summary: "private generated text"})
		}},
		{"knowledge_entries", "knowledge.created", func(ctx context.Context, r *InsightsRepo) error {
			return r.CreateKnowledge(ctx, &models.KnowledgeEntry{ProjectID: "default", Topic: "private generated text", Source: "commit"})
		}},
		{"health_checks", "insight.health_check_created", func(ctx context.Context, r *InsightsRepo) error {
			return r.CreateHealthCheck(ctx, &models.HealthCheck{ProjectID: "default", Grade: "B", Assessment: "private generated text"})
		}},
		{"idea_grades", "insight.idea_grade_created", func(ctx context.Context, r *InsightsRepo) error {
			return r.CreateIdeaGrade(ctx, &models.IdeaGrade{ProjectID: "default", Grade: "B", Summary: "private generated text"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.table, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			repo := NewInsightsRepo(db)
			_, err := db.Exec(`INSERT INTO users(id,username) VALUES('analyst','analyst'); INSERT INTO org_memberships VALUES('default','analyst','member')`)
			require.NoError(t, err)
			ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: "analyst", Role: "owner"})
			// Simulate access revoked while model work was in flight; stale claims must not grant access.
			_, err = db.Exec(`DELETE FROM org_memberships WHERE user_id='analyst'`)
			require.NoError(t, err)
			require.ErrorIs(t, tc.write(ctx, repo), persistence.ErrProjectDenied)
			count := func() int {
				var n int
				require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM `+tc.table).Scan(&n))
				return n
			}
			require.Zero(t, count())
			_, err = db.Exec(`INSERT INTO org_memberships VALUES('default','analyst','member')`)
			require.NoError(t, err)
			_, err = db.Exec(`UPDATE project_organizations SET org_role='viewer' WHERE project_id='default'`)
			require.NoError(t, err)
			require.ErrorIs(t, tc.write(ctx, repo), persistence.ErrProjectDenied)
			require.Zero(t, count(), "downgraded editor cannot save a model result")
			_, err = db.Exec(`UPDATE project_organizations SET org_role='editor' WHERE project_id='default'`)
			require.NoError(t, err)
			_, err = db.Exec(`CREATE TRIGGER fail_result_audit BEFORE INSERT ON project_audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			require.NoError(t, err)
			require.ErrorContains(t, tc.write(ctx, repo), "audit unavailable")
			require.Zero(t, count(), "audit failure must roll back the generated result")
			_, err = db.Exec(`DROP TRIGGER fail_result_audit`)
			require.NoError(t, err)
			require.NoError(t, tc.write(ctx, repo))
			require.Equal(t, 1, count())
			var actor, details, target string
			require.NoError(t, db.QueryRow(`SELECT actor_user_id,details,target_id FROM project_audit_events WHERE action=?`, tc.action).Scan(&actor, &details, &target))
			require.Equal(t, "analyst", actor)
			require.Equal(t, `{}`, details)
			require.NotEmpty(t, target)
		})
	}
}

func TestInsightsMutationsRecheckAccessAndRollbackAuditFailure(t *testing.T) {
	for _, operation := range []string{"status", "link", "delete", "knowledge"} {
		t.Run(operation, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			repo := NewInsightsRepo(db)
			ctx := t.Context()
			insight := &models.Insight{ProjectID: "default", Type: models.InsightBugPattern, Severity: models.InsightSeverityHigh, Status: models.InsightStatusNew, Title: "original"}
			require.NoError(t, repo.CreateInsight(ctx, insight))
			knowledge := &models.KnowledgeEntry{ProjectID: "default", Topic: "original", Source: "test"}
			require.NoError(t, repo.CreateKnowledge(ctx, knowledge))
			task := createThreadInputTask(t, ctx, db, "default")
			_, err := db.Exec(`INSERT INTO users(id,username) VALUES('editor','editor'); INSERT INTO org_memberships VALUES('default','editor','member')`)
			require.NoError(t, err)
			actor := models.WithPrincipal(ctx, models.Principal{UserID: "editor", Role: "owner"})
			mutate := func() error {
				switch operation {
				case "status":
					return repo.UpdateStatus(actor, "default", insight.ID, models.InsightStatusResolved)
				case "link":
					return repo.LinkTask(actor, "default", insight.ID, task.ID)
				case "delete":
					return repo.DeleteInsight(actor, "default", insight.ID)
				default:
					return repo.DeleteKnowledge(actor, "default", knowledge.ID)
				}
			}
			unchanged := func() {
				got, err := repo.GetInsight(ctx, "default", insight.ID)
				require.NoError(t, err)
				require.Equal(t, insight, got)
				gotK, err := repo.GetKnowledge(ctx, "default", knowledge.ID)
				require.NoError(t, err)
				require.Equal(t, knowledge, gotK)
			}
			_, err = db.Exec(`UPDATE project_organizations SET org_role='viewer' WHERE project_id='default'`)
			require.NoError(t, err)
			require.ErrorIs(t, mutate(), persistence.ErrProjectDenied)
			unchanged()
			_, err = db.Exec(`UPDATE project_organizations SET org_role='editor' WHERE project_id='default'; DELETE FROM org_memberships WHERE user_id='editor'`)
			require.NoError(t, err)
			require.ErrorIs(t, mutate(), persistence.ErrProjectDenied)
			unchanged()
			_, err = db.Exec(`INSERT INTO org_memberships VALUES('default','editor','member'); CREATE TRIGGER fail_mutation_audit BEFORE INSERT ON project_audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			require.NoError(t, err)
			require.ErrorContains(t, mutate(), "audit unavailable")
			unchanged()
			_, err = db.Exec(`DROP TRIGGER fail_mutation_audit`)
			require.NoError(t, err)
			require.NoError(t, mutate())
			var count int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_audit_events WHERE actor_user_id='editor'`).Scan(&count))
			require.Equal(t, 1, count)
		})
	}
}

func TestInsightCannotLinkForeignProjectTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID, OrgID: models.DefaultOrgID})
	repo := NewInsightsRepo(db)
	insight := &models.Insight{ProjectID: "default", Type: models.InsightBugPattern, Severity: models.InsightSeverityHigh, Status: models.InsightStatusNew, Title: "local"}
	require.NoError(t, repo.CreateInsight(ctx, insight))
	project := &models.Project{Name: "foreign"}
	require.NoError(t, NewProjectRepo(db).Create(ctx, project))
	task := createThreadInputTask(t, ctx, db, project.ID)
	require.ErrorIs(t, repo.LinkTask(ctx, "default", insight.ID, task.ID), persistence.ErrInsightNotFound)
	got, err := repo.GetInsight(ctx, "default", insight.ID)
	require.NoError(t, err)
	require.Equal(t, insight, got)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_audit_events WHERE action='insight.task_linked'`).Scan(&count))
	require.Zero(t, count)
}
