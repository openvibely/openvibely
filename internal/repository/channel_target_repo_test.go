package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestChannelTargetRepo_ReplaceProjectTargetsDeletesRemovedRowsBeforeInsert(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	project := &models.Project{Name: "Replace Targets Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	repo := NewChannelTargetRepo(db)

	first := models.ChannelTarget{ID: "target-keep", ProjectID: project.ID, Platform: "email", Name: "keep", TargetID: "keep@example.com"}
	removed := models.ChannelTarget{ID: "target-removed", ProjectID: project.ID, Platform: "email", TargetID: "restore@example.com"}
	require.NoError(t, repo.ReplaceProjectTargets(ctx, project.ID, []models.ChannelTarget{first, removed}))
	require.NoError(t, repo.ReplaceProjectTargets(ctx, project.ID, []models.ChannelTarget{first}))

	readded := models.ChannelTarget{ID: "target-readded", ProjectID: project.ID, Platform: "email", TargetID: "restore@example.com"}
	require.NoError(t, repo.ReplaceProjectTargets(ctx, project.ID, []models.ChannelTarget{first, readded}))

	targets, err := repo.ListByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, targets, 2)
	foundReadded, err := repo.GetByID(ctx, "target-readded")
	require.NoError(t, err)
	require.NotNil(t, foundReadded)
	require.Equal(t, "restore@example.com", foundReadded.TargetID)
}

func TestChannelTargetRepo_UpsertAndReplaceShareNormalizationAndHomeClearing(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	project := &models.Project{Name: "Shared Persistence Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	repo := NewChannelTargetRepo(db)

	require.NoError(t, repo.Upsert(ctx, models.ChannelTarget{ID: "upsert-home", ProjectID: project.ID, Platform: " Email ", Name: " Team ", TargetID: "Team@Example.com", Home: true, DefaultSubject: " Subject "}))
	upserted, err := repo.GetByID(ctx, "upsert-home")
	require.NoError(t, err)
	require.NotNil(t, upserted)
	require.Equal(t, "email", upserted.Platform)
	require.Equal(t, "email", upserted.TargetKind)
	require.Equal(t, "team", upserted.Name)
	require.Equal(t, "Team@Example.com", upserted.TargetID)
	require.Equal(t, "Subject", upserted.DefaultSubject)
	require.True(t, upserted.Home)

	require.NoError(t, repo.ReplaceProjectTargets(ctx, project.ID, []models.ChannelTarget{
		{ID: "replace-home", Platform: " Telegram ", Name: " Alerts ", TargetID: "-100123", ThreadID: " 42 ", Home: true},
		{ID: "replace-slack", Platform: " Slack ", Name: " Ops ", TargetID: "COPS"},
	}))
	replacedHome, err := repo.GetByID(ctx, "replace-home")
	require.NoError(t, err)
	require.NotNil(t, replacedHome)
	require.Equal(t, project.ID, replacedHome.ProjectID)
	require.Equal(t, "telegram", replacedHome.Platform)
	require.Equal(t, "chat", replacedHome.TargetKind)
	require.Equal(t, "alerts", replacedHome.Name)
	require.Equal(t, "42", replacedHome.ThreadID)
	require.True(t, replacedHome.Home)
	replacedSlack, err := repo.GetByID(ctx, "replace-slack")
	require.NoError(t, err)
	require.NotNil(t, replacedSlack)
	require.Equal(t, "slack", replacedSlack.Platform)
	require.Equal(t, "channel", replacedSlack.TargetKind)
	require.Equal(t, "ops", replacedSlack.Name)
	oldHome, err := repo.GetByID(ctx, "upsert-home")
	require.NoError(t, err)
	require.Nil(t, oldHome, "replace deletes omitted rows before using the shared upsert helper")

	require.NoError(t, repo.Upsert(ctx, models.ChannelTarget{ID: "telegram-new-home", ProjectID: project.ID, Platform: "telegram", TargetID: "-100456", Home: true}))
	replacedHome, err = repo.GetByID(ctx, "replace-home")
	require.NoError(t, err)
	require.NotNil(t, replacedHome)
	require.False(t, replacedHome.Home, "Upsert must clear previous homes for the same platform using the shared helper")
	newHome, err := repo.GetByID(ctx, "telegram-new-home")
	require.NoError(t, err)
	require.NotNil(t, newHome)
	require.True(t, newHome.Home)
}

func TestChannelTargetRepo_CRUDAndAudit(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	project := &models.Project{Name: "Targets Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	repo := NewChannelTargetRepo(db)

	require.NoError(t, repo.Upsert(ctx, models.ChannelTarget{ID: "target-1", ProjectID: project.ID, Platform: "Slack", Name: "Ops", TargetID: "C123", ThreadID: "169.1", Home: true}))
	home, err := repo.FindHome(ctx, project.ID, "slack")
	require.NoError(t, err)
	require.NotNil(t, home)
	require.Equal(t, "ops", home.Name)
	named, err := repo.FindByName(ctx, project.ID, "slack", "ops")
	require.NoError(t, err)
	require.Equal(t, "C123", named.TargetID)
	byTarget, err := repo.FindByTarget(ctx, project.ID, "slack", "C123", "169.1")
	require.NoError(t, err)
	require.NotNil(t, byTarget)

	require.NoError(t, repo.RecordSend(ctx, models.ChannelMessageSend{ID: "send-1", ProjectID: project.ID, Platform: "slack", TargetID: "C123", ThreadID: "169.1", RequestedBySurface: "web", MessagePreview: "hello", Success: true}))
	sends, err := repo.ListSendsByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, sends, 1)
	require.Equal(t, "web", sends[0].RequestedBySurface)

	require.NoError(t, repo.Delete(ctx, "target-1"))
	missing, err := repo.FindByName(ctx, project.ID, "slack", "ops")
	require.NoError(t, err)
	require.Nil(t, missing)
}

func TestChannelTargetRepo_SummarizeByProjectMatchesMaterializedSummary(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	project := &models.Project{Name: "Summary Targets Project"}
	otherProject := &models.Project{Name: "Other Summary Targets Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, projectRepo.Create(ctx, otherProject))
	repo := NewChannelTargetRepo(db)

	fixtures := []models.ChannelTarget{
		{ID: "slack-home", ProjectID: project.ID, Platform: "slack", TargetKind: "channel", Name: "ops", TargetID: "COPS", Home: true},
		{ID: "slack-user", ProjectID: project.ID, Platform: "slack", TargetKind: "user", TargetID: "U123"},
		{ID: "telegram-home", ProjectID: project.ID, Platform: "telegram", TargetKind: "chat", Name: "alerts", TargetID: "-100", Home: true},
		{ID: "discord-channel", ProjectID: project.ID, Platform: "discord", TargetKind: "channel", Name: "guild", TargetID: "C456"},
		{ID: "discord-user", ProjectID: project.ID, Platform: "discord", TargetKind: "user", TargetID: "D789"},
		{ID: "email-team", ProjectID: project.ID, Platform: "email", TargetKind: "email", Name: "team", TargetID: "team@example.com"},
		{ID: "other-slack", ProjectID: otherProject.ID, Platform: "slack", TargetKind: "channel", Name: "other", TargetID: "COTHER", Home: true},
	}
	for _, target := range fixtures {
		require.NoError(t, repo.Upsert(ctx, target))
	}

	summary, err := repo.SummarizeByProject(ctx, project.ID)
	require.NoError(t, err)
	materialized, err := repo.ListByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, channelTargetMaterializedSummary(materialized), summary)
	require.Equal(t, 6, summary.Total)
	require.True(t, summary.Configured)
	require.Equal(t, ChannelTargetPlatformSummary{Total: 2, Home: 1, Named: 1, ByKind: map[string]int{"channel": 1, "user": 1}}, summary.ByPlatform["slack"])
	require.Equal(t, ChannelTargetPlatformSummary{Total: 1, Home: 1, Named: 1, ByKind: map[string]int{"chat": 1}}, summary.ByPlatform["telegram"])
	require.Equal(t, ChannelTargetPlatformSummary{Total: 2, Home: 0, Named: 1, ByKind: map[string]int{"channel": 1, "user": 1}}, summary.ByPlatform["discord"])
	require.Equal(t, ChannelTargetPlatformSummary{Total: 1, Home: 0, Named: 1, ByKind: map[string]int{"email": 1}}, summary.ByPlatform["email"])

	emptySummary, err := repo.SummarizeByProject(ctx, "missing-project")
	require.NoError(t, err)
	require.False(t, emptySummary.Configured)
	require.Equal(t, 0, emptySummary.Total)
	require.Empty(t, emptySummary.ByPlatform)
}

func channelTargetMaterializedSummary(targets []models.ChannelTarget) ChannelTargetProjectSummary {
	out := ChannelTargetProjectSummary{Total: len(targets), Configured: len(targets) > 0, ByPlatform: map[string]ChannelTargetPlatformSummary{}}
	for _, target := range targets {
		platform := normalizeChannelTargetField(target.Platform)
		if platform == "" {
			platform = "unknown"
		}
		kind := normalizeChannelTargetField(target.TargetKind)
		if kind == "" {
			kind = models.DefaultChannelTargetKind(platform)
		}
		platformSummary := out.ByPlatform[platform]
		platformSummary.Total++
		if target.Home {
			platformSummary.Home++
		}
		if strings.TrimSpace(target.Name) != "" {
			platformSummary.Named++
		}
		if platformSummary.ByKind == nil {
			platformSummary.ByKind = map[string]int{}
		}
		platformSummary.ByKind[kind]++
		out.ByPlatform[platform] = platformSummary
	}
	return out
}

func TestChannelTargetRepoLookupQueriesUseDedicatedIndexes(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	project := &models.Project{Name: "Target Lookup Index Project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	repo := NewChannelTargetRepo(db)

	seedChannelTargetLookupFixture(t, db, project.ID)

	homeQuery := `
		SELECT id, project_id, platform, target_kind, name, target_id, thread_id, is_home, default_subject, created_at, updated_at
		FROM channel_targets
		WHERE project_id = ? AND platform = ? AND is_home = 1
		ORDER BY updated_at DESC LIMIT 1`
	homePlan := channelTargetExplainQueryPlan(t, db, homeQuery, project.ID, "slack")
	require.Contains(t, homePlan, "idx_channel_targets_project_platform_home_updated")
	require.Contains(t, homePlan, "project_id=?")
	require.Contains(t, homePlan, "platform=?")
	require.Contains(t, homePlan, "is_home=?")
	require.NotContains(t, homePlan, "USE TEMP B-TREE FOR ORDER BY")

	home, err := repo.FindHome(ctx, project.ID, "slack")
	require.NoError(t, err)
	require.NotNil(t, home)
	require.Equal(t, "new-home", home.ID, "FindHome keeps newest updated home fallback semantics")

	nameQuery := `
		SELECT id, project_id, platform, target_kind, name, target_id, thread_id, is_home, default_subject, created_at, updated_at
		FROM channel_targets
		WHERE project_id = ? AND platform = ? AND name = ?`
	namePlan := channelTargetExplainQueryPlan(t, db, nameQuery, project.ID, "slack", "ops")
	require.Contains(t, namePlan, "idx_channel_targets_project_platform_name")
	require.Contains(t, namePlan, "project_id=?")
	require.Contains(t, namePlan, "platform=?")
	require.Contains(t, namePlan, "name=?")

	named, err := repo.FindByName(ctx, project.ID, "slack", "ops")
	require.NoError(t, err)
	require.NotNil(t, named)
	require.Equal(t, "named-ops", named.ID)

	targetQuery := `
		SELECT id, project_id, platform, target_kind, name, target_id, thread_id, is_home, default_subject, created_at, updated_at
		FROM channel_targets
		WHERE project_id = ? AND platform = ? AND target_id = ? AND thread_id = ?`
	targetPlan := channelTargetExplainQueryPlan(t, db, targetQuery, project.ID, "slack", "C12345", "1690000000.000000")
	require.Contains(t, targetPlan, "idx_channel_targets_project_platform_target_thread")
	require.Contains(t, targetPlan, "project_id=?")
	require.Contains(t, targetPlan, "platform=?")
	require.Contains(t, targetPlan, "target_id=?")
	require.Contains(t, targetPlan, "thread_id=?")

	byTarget, err := repo.FindByTarget(ctx, project.ID, "slack", "C12345", "1690000000.000000")
	require.NoError(t, err)
	require.NotNil(t, byTarget)
	require.Equal(t, "saved-target", byTarget.ID)
}

func BenchmarkChannelTargetRepoFindHomeLookup(b *testing.B) {
	db := testutil.NewTestDB(b)
	projectID := "channel-target-bench-project"
	seedChannelTargetBenchFixture(b, db, projectID)
	repo := NewChannelTargetRepo(db)
	ctx := context.Background()

	plan := channelTargetExplainQueryPlan(b, db, `
				SELECT id, project_id, platform, target_kind, name, target_id, thread_id, is_home, default_subject, created_at, updated_at
				FROM channel_targets
				WHERE project_id = ? AND platform = ? AND is_home = 1
				ORDER BY updated_at DESC LIMIT 1`, projectID, "slack")
	require.Contains(b, plan, "idx_channel_targets_project_platform_home_updated")
	require.NotContains(b, plan, "USE TEMP B-TREE FOR ORDER BY")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		target, err := repo.FindHome(ctx, projectID, "slack")
		if err != nil {
			b.Fatalf("find home: %v", err)
		}
		if target == nil || target.ID != "bench-home-newest" {
			b.Fatalf("home target = %#v, want bench-home-newest", target)
		}
	}
}

func seedChannelTargetLookupFixture(t *testing.T, db *sql.DB, projectID string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO channel_targets (id, project_id, platform, target_kind, name, target_id, thread_id, is_home, updated_at) VALUES
		('old-home', ?, 'slack', 'channel', 'old-home', 'COLD', '', 1, '2024-01-01 00:00:00'),
		('new-home', ?, 'slack', 'channel', 'new-home', 'CNEW', '', 1, '2024-01-02 00:00:00'),
		('named-ops', ?, 'slack', 'channel', 'ops', 'COPS', '', 0, '2024-01-03 00:00:00'),
		('saved-target', ?, 'slack', 'channel', 'threaded', 'C12345', '1690000000.000000', 0, '2024-01-04 00:00:00')`, projectID, projectID, projectID, projectID); err != nil {
		t.Fatalf("seed lookup targets: %v", err)
	}
	for i := 0; i < 300; i++ {
		platform := "slack"
		if i%3 == 1 {
			platform = "telegram"
		} else if i%3 == 2 {
			platform = "email"
		}
		if _, err := db.Exec(`
			INSERT INTO channel_targets (id, project_id, platform, target_kind, name, target_id, thread_id, is_home, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)`,
			fmt.Sprintf("noise-%03d", i), projectID, platform, models.DefaultChannelTargetKind(platform), fmt.Sprintf("noise-%03d", i), fmt.Sprintf("T%03d", i), fmt.Sprintf("thread-%03d", i), time.Date(2024, 1, 5, 0, i%60, 0, 0, time.UTC).Format("2006-01-02 15:04:05")); err != nil {
			t.Fatalf("seed noise target %d: %v", i, err)
		}
	}
}

func seedChannelTargetBenchFixture(tb testing.TB, db *sql.DB, projectID string) {
	tb.Helper()
	if _, err := db.Exec(`INSERT INTO projects (id, name, description, repo_path) VALUES (?, 'Channel Target Bench', '', '')`, projectID); err != nil {
		tb.Fatalf("seed benchmark project: %v", err)
	}
	const rows = 25000
	for i := 0; i < rows; i++ {
		project := projectID
		if i%5 != 0 {
			project = fmt.Sprintf("channel-target-bench-other-%d", i%5)
		}
		platform := "slack"
		switch i % 3 {
		case 1:
			platform = "telegram"
		case 2:
			platform = "email"
		}
		isHome := 0
		id := fmt.Sprintf("bench-target-%05d", i)
		if i == rows-2 {
			project = projectID
			platform = "slack"
			isHome = 1
			id = "bench-home-old"
		}
		if i == rows-1 {
			project = projectID
			platform = "slack"
			isHome = 1
			id = "bench-home-newest"
		}
		if strings.HasPrefix(project, "channel-target-bench-other-") {
			_, _ = db.Exec(`INSERT OR IGNORE INTO projects (id, name, description, repo_path) VALUES (?, ?, '', '')`, project, project)
		}
		if _, err := db.Exec(`
			INSERT INTO channel_targets (id, project_id, platform, target_kind, name, target_id, thread_id, is_home, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, project, platform, models.DefaultChannelTargetKind(platform), fmt.Sprintf("bench-name-%05d", i), fmt.Sprintf("BENCH%05d", i), fmt.Sprintf("thread-%05d", i), isHome, time.Date(2024, 1, 1, 0, 0, i%60, 0, time.UTC).Add(time.Duration(i)*time.Second).Format("2006-01-02 15:04:05")); err != nil {
			tb.Fatalf("seed benchmark target %d: %v", i, err)
		}
	}
}

func channelTargetExplainQueryPlan(tb testing.TB, db *sql.DB, query string, args ...any) string {
	tb.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		tb.Fatalf("explain query plan: %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			tb.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		tb.Fatalf("iterate query plan: %v", err)
	}
	return strings.Join(details, "\n")
}

func TestChannelTargetRepo_DeleteForProjectAndDeleteProjectExcept(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	projectA := &models.Project{Name: "Channel Delete Project A"}
	projectB := &models.Project{Name: "Channel Delete Project B"}
	require.NoError(t, projectRepo.Create(ctx, projectA))
	require.NoError(t, projectRepo.Create(ctx, projectB))
	repo := NewChannelTargetRepo(db)
	targets := []models.ChannelTarget{
		{ID: "delete-a", ProjectID: projectA.ID, Platform: "email", TargetID: "delete@example.com"},
		{ID: "keep-a", ProjectID: projectA.ID, Platform: "slack", TargetID: "C1", Name: "keep"},
		{ID: "remove-a", ProjectID: projectA.ID, Platform: "discord", TargetID: "D1", Name: "remove"},
		{ID: "foreign-b", ProjectID: projectB.ID, Platform: "email", TargetID: "foreign@example.com"},
	}
	for _, target := range targets {
		require.NoError(t, repo.Upsert(ctx, target))
	}
	require.ErrorContains(t, repo.DeleteForProject(ctx, projectB.ID, "delete-a"), "channel target not found")
	require.NoError(t, repo.DeleteForProject(ctx, projectA.ID, "delete-a"))
	require.NoError(t, repo.DeleteProjectExcept(ctx, projectA.ID, []string{" keep-a ", ""}))
	remaining, err := repo.ListByProject(ctx, projectA.ID)
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	require.Equal(t, "keep-a", remaining[0].ID)
	foreign, err := repo.ListByProject(ctx, projectB.ID)
	require.NoError(t, err)
	require.Len(t, foreign, 1)
	require.Equal(t, "foreign-b", foreign[0].ID)
}
