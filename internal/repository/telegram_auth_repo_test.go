package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelegramAuthRepo_CRUD(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTelegramAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	// Create a project
	project := &models.Project{Name: "Auth Test Project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// List should be empty initially
	users, err := repo.ListByProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("ListByProject failed: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("expected 0 users, got %d", len(users))
	}

	// Create an authorized user by user ID
	user1 := &models.TelegramAuthorizedUser{
		ProjectID:      project.ID,
		TelegramUserID: 123456,
		DisplayName:    "Test User",
		AddedBy:        "web",
	}
	if err := repo.Create(ctx, user1); err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if user1.ID == "" {
		t.Error("expected ID to be populated")
	}
	if user1.AddedAt.IsZero() {
		t.Error("expected AddedAt to be populated")
	}

	// Create an authorized user by username
	user2 := &models.TelegramAuthorizedUser{
		ProjectID:        project.ID,
		TelegramUserID:   0, // Unknown
		TelegramUsername: "johndoe",
		DisplayName:      "@johndoe",
		AddedBy:          "web",
	}
	if err := repo.Create(ctx, user2); err != nil {
		t.Fatalf("Create user2 failed: %v", err)
	}

	// List should show 2 users
	users, err = repo.ListByProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("ListByProject failed: %v", err)
	}
	if len(users) != 2 {
		t.Errorf("expected 2 users, got %d", len(users))
	}

	// GetByID
	got, err := repo.GetByID(ctx, user1.ID)
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected user, got nil")
	}
	if got.TelegramUserID != 123456 {
		t.Errorf("expected TelegramUserID 123456, got %d", got.TelegramUserID)
	}

	// GetByID non-existent
	got, err = repo.GetByID(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetByID nonexistent failed: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent user")
	}

	// Delete
	if err := repo.Delete(ctx, user1.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// List should show 1 user
	users, err = repo.ListByProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("ListByProject after delete failed: %v", err)
	}
	if len(users) != 1 {
		t.Errorf("expected 1 user after delete, got %d", len(users))
	}

	// Delete non-existent should error
	if err := repo.Delete(ctx, "nonexistent"); err == nil {
		t.Error("expected error deleting nonexistent user")
	}
}

func TestTelegramAuthRepo_SystemAuthorizationAcrossProjects(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTelegramAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Unique Test"}
	otherProject := &models.Project{Name: "Other Test"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, projectRepo.Create(ctx, otherProject))

	user := &models.TelegramAuthorizedUser{
		ProjectID:      project.ID,
		TelegramUserID: 999,
		DisplayName:    "User",
		AddedBy:        "web",
	}
	require.NoError(t, repo.Create(ctx, user))
	require.NoError(t, repo.Create(ctx, &models.TelegramAuthorizedUser{
		ProjectID:      otherProject.ID,
		TelegramUserID: 999,
		DisplayName:    "User Dup",
		AddedBy:        "web",
	}))

	authorized, err := repo.IsAuthorized(ctx, otherProject.ID, 999, "")
	require.NoError(t, err)
	require.True(t, authorized)
	users, err := repo.ListByProject(ctx, otherProject.ID)
	require.NoError(t, err)
	require.Len(t, users, 1)
}

func TestTelegramAuthRepo_IsAuthorized(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTelegramAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Auth Check"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// No users configured → HasAnyAuthorizedUsers should be false
	has, err := repo.HasAnyAuthorizedUsers(ctx, project.ID)
	if err != nil {
		t.Fatalf("HasAnyAuthorizedUsers failed: %v", err)
	}
	if has {
		t.Error("expected no authorized users initially")
	}

	// Add user by ID
	u1 := &models.TelegramAuthorizedUser{
		ProjectID:      project.ID,
		TelegramUserID: 111,
		DisplayName:    "User 111",
		AddedBy:        "web",
	}
	if err := repo.Create(ctx, u1); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Add user by username (ID=0)
	u2 := &models.TelegramAuthorizedUser{
		ProjectID:        project.ID,
		TelegramUserID:   0,
		TelegramUsername: "alice",
		DisplayName:      "@alice",
		AddedBy:          "web",
	}
	if err := repo.Create(ctx, u2); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Now HasAnyAuthorizedUsers should be true
	has, err = repo.HasAnyAuthorizedUsers(ctx, project.ID)
	if err != nil {
		t.Fatalf("HasAnyAuthorizedUsers failed: %v", err)
	}
	if !has {
		t.Error("expected authorized users to exist")
	}

	// Check authorized by user ID
	authorized, err := repo.IsAuthorized(ctx, project.ID, 111, "")
	if err != nil {
		t.Fatalf("IsAuthorized failed: %v", err)
	}
	if !authorized {
		t.Error("user 111 should be authorized")
	}

	// Check unauthorized user
	authorized, err = repo.IsAuthorized(ctx, project.ID, 999, "")
	if err != nil {
		t.Fatalf("IsAuthorized failed: %v", err)
	}
	if authorized {
		t.Error("user 999 should not be authorized")
	}

	// Check authorized by username
	authorized, err = repo.IsAuthorized(ctx, project.ID, 222, "alice")
	if err != nil {
		t.Fatalf("IsAuthorized failed: %v", err)
	}
	if !authorized {
		t.Error("user with username 'alice' should be authorized")
	}

	// Check unauthorized by username
	authorized, err = repo.IsAuthorized(ctx, project.ID, 333, "bob")
	if err != nil {
		t.Fatalf("IsAuthorized failed: %v", err)
	}
	if authorized {
		t.Error("user with username 'bob' should not be authorized")
	}
}

func TestTelegramAuthRepo_IsAuthorizedAnywhere(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTelegramAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	// No users at all → not authorized anywhere
	authorized, err := repo.IsAuthorizedAnywhere(ctx, 999, "nobody")
	require.NoError(t, err)
	assert.False(t, authorized)

	// Add a user to a project
	project := &models.Project{Name: "Anywhere Test"}
	require.NoError(t, projectRepo.Create(ctx, project))

	u := &models.TelegramAuthorizedUser{
		ProjectID:      project.ID,
		TelegramUserID: 111,
		DisplayName:    "User 111",
		AddedBy:        "test",
	}
	require.NoError(t, repo.Create(ctx, u))

	// That user is now authorized somewhere
	authorized, err = repo.IsAuthorizedAnywhere(ctx, 111, "")
	require.NoError(t, err)
	assert.True(t, authorized)

	// Unknown user is still not authorized
	authorized, err = repo.IsAuthorizedAnywhere(ctx, 999, "nobody")
	require.NoError(t, err)
	assert.False(t, authorized)

	// Username-based match
	u2 := &models.TelegramAuthorizedUser{
		ProjectID:        project.ID,
		TelegramUserID:   0,
		TelegramUsername: "bob",
		DisplayName:      "@bob",
		AddedBy:          "test",
	}
	require.NoError(t, repo.Create(ctx, u2))

	authorized, err = repo.IsAuthorizedAnywhere(ctx, 0, "bob")
	require.NoError(t, err)
	assert.True(t, authorized)
}

func TestTelegramAuthRepo_BackfillUserID(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTelegramAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Backfill Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Add user by username only
	u := &models.TelegramAuthorizedUser{
		ProjectID:        project.ID,
		TelegramUserID:   0,
		TelegramUsername: "charlie",
		DisplayName:      "@charlie",
		AddedBy:          "web",
	}
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Verify user is authorized by username
	authorized, err := repo.IsAuthorized(ctx, project.ID, 555, "charlie")
	if err != nil {
		t.Fatalf("IsAuthorized failed: %v", err)
	}
	if !authorized {
		t.Error("user charlie should be authorized by username")
	}

	// Backfill the user ID
	if err := repo.BackfillUserID(ctx, project.ID, "charlie", 555); err != nil {
		t.Fatalf("BackfillUserID failed: %v", err)
	}

	// Now should be authorized by user ID alone
	authorized, err = repo.IsAuthorized(ctx, project.ID, 555, "")
	if err != nil {
		t.Fatalf("IsAuthorized failed: %v", err)
	}
	if !authorized {
		t.Error("user 555 should be authorized after backfill")
	}

	// Verify the record was updated
	got, err := repo.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}
	if got.TelegramUserID != 555 {
		t.Errorf("expected TelegramUserID 555 after backfill, got %d", got.TelegramUserID)
	}
}

func TestTelegramAuthRepo_CaseInsensitiveUsername(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTelegramAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Case Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Add user with lowercase username
	u := &models.TelegramAuthorizedUser{
		ProjectID:        project.ID,
		TelegramUserID:   0,
		TelegramUsername: "bobsmith",
		DisplayName:      "@bobsmith",
		AddedBy:          "web",
	}
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Should match case-insensitively
	authorized, err := repo.IsAuthorized(ctx, project.ID, 444, "BobSmith")
	if err != nil {
		t.Fatalf("IsAuthorized failed: %v", err)
	}
	if !authorized {
		t.Error("username match should be case-insensitive")
	}

	// Should match exact case too
	authorized, err = repo.IsAuthorized(ctx, project.ID, 444, "bobsmith")
	if err != nil {
		t.Fatalf("IsAuthorized failed: %v", err)
	}
	if !authorized {
		t.Error("exact case should still match")
	}

	// Backfill should also work case-insensitively
	if err := repo.BackfillUserID(ctx, project.ID, "BOBSMITH", 444); err != nil {
		t.Fatalf("BackfillUserID failed: %v", err)
	}

	// After backfill, should be authorized by user ID
	authorized, err = repo.IsAuthorized(ctx, project.ID, 444, "")
	if err != nil {
		t.Fatalf("IsAuthorized failed: %v", err)
	}
	if !authorized {
		t.Error("user 444 should be authorized after case-insensitive backfill")
	}
}

func TestTelegramAuthRepo_DoesNotDuplicateLegacyMixedCaseUsername(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTelegramAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Legacy Mixed Case Username Test"}
	require.NoError(t, projectRepo.Create(ctx, project))

	_, err := db.ExecContext(ctx,
		`INSERT INTO telegram_authorized_users (id, project_id, telegram_user_id, telegram_username, display_name, added_by)
		 VALUES ('legacy-telegram-user', ?, 0, 'AliceUser', '@AliceUser', 'test')`,
		project.ID)
	require.NoError(t, err)

	duplicate := &models.TelegramAuthorizedUser{
		ProjectID:        project.ID,
		TelegramUserID:   0,
		TelegramUsername: "aliceuser",
		DisplayName:      "@aliceuser",
		AddedBy:          "web",
	}
	require.NoError(t, repo.Create(ctx, duplicate))

	users, err := repo.ListByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.Equal(t, "legacy-telegram-user", duplicate.ID)
	assert.Equal(t, "AliceUser", users[0].TelegramUsername)
}

func TestTelegramAuthRepo_MultipleUsernameEntries(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTelegramAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Multi Username Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Add first user by username
	u1 := &models.TelegramAuthorizedUser{
		ProjectID:        project.ID,
		TelegramUserID:   0,
		TelegramUsername: "alice",
		DisplayName:      "@alice",
		AddedBy:          "web",
	}
	if err := repo.Create(ctx, u1); err != nil {
		t.Fatalf("Create alice failed: %v", err)
	}

	// Add second user by username — must succeed (was blocked by old UNIQUE constraint)
	u2 := &models.TelegramAuthorizedUser{
		ProjectID:        project.ID,
		TelegramUserID:   0,
		TelegramUsername: "bob",
		DisplayName:      "@bob",
		AddedBy:          "web",
	}
	if err := repo.Create(ctx, u2); err != nil {
		t.Fatalf("Create bob failed: %v", err)
	}

	// Both should be authorized
	authorized, err := repo.IsAuthorized(ctx, project.ID, 0, "alice")
	if err != nil {
		t.Fatalf("IsAuthorized alice failed: %v", err)
	}
	if !authorized {
		t.Error("alice should be authorized")
	}

	authorized, err = repo.IsAuthorized(ctx, project.ID, 0, "bob")
	if err != nil {
		t.Fatalf("IsAuthorized bob failed: %v", err)
	}
	if !authorized {
		t.Error("bob should be authorized")
	}

	// Unauthorized user should be blocked
	authorized, err = repo.IsAuthorized(ctx, project.ID, 999, "eve")
	if err != nil {
		t.Fatalf("IsAuthorized eve failed: %v", err)
	}
	if authorized {
		t.Error("eve should not be authorized")
	}

	// Verify list shows 2 users
	users, err := repo.ListByProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("ListByProject failed: %v", err)
	}
	if len(users) != 2 {
		t.Errorf("expected 2 users, got %d", len(users))
	}
}

func TestTelegramAuthRepo_IsAuthorizedAnywhereLargeUsernameOnlyUsesIndexes(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTelegramAuthRepo(db)
	ctx := context.Background()
	targetUsername := seedLargeTelegramUsernameOnlyFixture(t, db)

	plan := explainTelegramAuthQueryPlan(t, db, telegramIsAuthorizedAnywhereQuery, int64(123456789), targetUsername)
	if strings.Contains(plan, "SCAN telegram_authorized_users") {
		t.Fatalf("query plan = %s, want indexed username lookup without full table scan", plan)
	}
	require.Contains(t, plan, "idx_telegram_auth_user")
	require.Contains(t, plan, "idx_telegram_auth_username_lower_unknown_id")

	authorized, err := repo.IsAuthorizedAnywhere(ctx, 123456789, "missing_large_username")
	require.NoError(t, err)
	assert.False(t, authorized)

	authorized, err = repo.IsAuthorizedAnywhere(ctx, 123456789, strings.ToUpper(targetUsername))
	require.NoError(t, err)
	assert.True(t, authorized)
}

func BenchmarkTelegramAuthRepo_IsAuthorizedAnywhereLargeUsernameOnly(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewTelegramAuthRepo(db)
	ctx := context.Background()
	targetUsername := seedLargeTelegramUsernameOnlyFixture(b, db)

	b.Run("absent_username", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			authorized, err := repo.IsAuthorizedAnywhere(ctx, 123456789, "missing_large_username")
			if err != nil {
				b.Fatal(err)
			}
			if authorized {
				b.Fatal("missing username should not be authorized")
			}
		}
	})

	b.Run("present_username", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			authorized, err := repo.IsAuthorizedAnywhere(ctx, 123456789, targetUsername)
			if err != nil {
				b.Fatal(err)
			}
			if !authorized {
				b.Fatal("target username should be authorized")
			}
		}
	})
}

func seedLargeTelegramUsernameOnlyFixture(tb testing.TB, db *sql.DB) string {
	tb.Helper()
	projectRepo := NewProjectRepo(db)
	project := &models.Project{Name: "Large Telegram Username Fixture"}
	require.NoError(tb, projectRepo.Create(context.Background(), project))

	const rowCount = 100000
	_, err := db.ExecContext(context.Background(), `
		WITH digits(d) AS (VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9)),
		seq(n) AS (
			SELECT ones.d + tens.d*10 + hundreds.d*100 + thousands.d*1000 + ten_thousands.d*10000 + 1
			FROM digits AS ones
			CROSS JOIN digits AS tens
			CROSS JOIN digits AS hundreds
			CROSS JOIN digits AS thousands
			CROSS JOIN digits AS ten_thousands
		)
		INSERT INTO telegram_authorized_users (id, project_id, telegram_user_id, telegram_username, display_name, added_by)
		SELECT printf('telegram-large-%06d', n), ?, 0, printf('largeuser%06d', n), printf('@largeuser%06d', n), 'test'
		FROM seq
		WHERE n <= ?`, project.ID, rowCount)
	require.NoError(tb, err)
	return fmt.Sprintf("largeuser%06d", rowCount/2)
}

func explainTelegramAuthQueryPlan(tb testing.TB, db *sql.DB, query string, args ...any) string {
	tb.Helper()
	rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	require.NoError(tb, err)
	defer rows.Close()

	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(tb, rows.Scan(&id, &parent, &unused, &detail))
		details = append(details, detail)
	}
	require.NoError(tb, rows.Err())
	return strings.Join(details, "\n")
}

func TestTelegramAuthRepo_CascadeDelete(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTelegramAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Cascade Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Add authorized user
	u := &models.TelegramAuthorizedUser{
		ProjectID:      project.ID,
		TelegramUserID: 777,
		DisplayName:    "Cascade User",
		AddedBy:        "web",
	}
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Delete the project
	if err := projectRepo.Delete(ctx, project.ID); err != nil {
		t.Fatalf("Delete project failed: %v", err)
	}

	// Authorized users should be cascade-deleted
	users, err := repo.ListByProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("ListByProject failed: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("expected 0 users after project cascade delete, got %d", len(users))
	}
}
