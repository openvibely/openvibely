package repository

import (
	"context"
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

func TestTelegramAuthRepo_UniqueConstraint(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewTelegramAuthRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{Name: "Unique Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	user := &models.TelegramAuthorizedUser{
		ProjectID:      project.ID,
		TelegramUserID: 999,
		DisplayName:    "User",
		AddedBy:        "web",
	}
	if err := repo.Create(ctx, user); err != nil {
		t.Fatalf("first Create failed: %v", err)
	}

	// Duplicate should fail
	dup := &models.TelegramAuthorizedUser{
		ProjectID:      project.ID,
		TelegramUserID: 999,
		DisplayName:    "User Dup",
		AddedBy:        "web",
	}
	if err := repo.Create(ctx, dup); err == nil {
		t.Error("expected error on duplicate (project_id, telegram_user_id)")
	}
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
