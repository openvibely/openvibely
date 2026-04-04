package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCustomPersonalityRepo_Create(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewCustomPersonalityRepo(db)
	ctx := context.Background()

	personality := &models.CustomPersonality{
		Name:         "Test Personality",
		Key:          "test_personality",
		Description:  "A test personality",
		SystemPrompt: "You are a test assistant.",
	}

	err := repo.Create(ctx, personality)
	require.NoError(t, err)
	assert.NotEmpty(t, personality.ID)
	assert.False(t, personality.CreatedAt.IsZero())
	assert.False(t, personality.UpdatedAt.IsZero())
}

func TestCustomPersonalityRepo_GetByKey(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewCustomPersonalityRepo(db)
	ctx := context.Background()

	// Create a personality
	p := &models.CustomPersonality{
		Name:         "Get Test",
		Key:          "get_test",
		Description:  "Description",
		SystemPrompt: "Test prompt",
	}
	require.NoError(t, repo.Create(ctx, p))

	// Get by key
	got, err := repo.GetByKey(ctx, "get_test")
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Equal(t, "Get Test", got.Name)
	assert.Equal(t, "get_test", got.Key)
	assert.Equal(t, "Description", got.Description)
	assert.Equal(t, "Test prompt", got.SystemPrompt)

	// Get non-existent key
	got, err = repo.GetByKey(ctx, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestCustomPersonalityRepo_List(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewCustomPersonalityRepo(db)
	ctx := context.Background()

	// List should be empty initially
	list, err := repo.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, list)

	// Create some personalities
	p1 := &models.CustomPersonality{
		Name:         "Zebra Personality",
		Key:          "zebra",
		Description:  "Z comes last",
		SystemPrompt: "Zebra prompt",
	}
	require.NoError(t, repo.Create(ctx, p1))

	p2 := &models.CustomPersonality{
		Name:         "Alpha Personality",
		Key:          "alpha",
		Description:  "A comes first",
		SystemPrompt: "Alpha prompt",
	}
	require.NoError(t, repo.Create(ctx, p2))

	// List should return them ordered by name
	list, err = repo.List(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 2)
	assert.Equal(t, "Alpha Personality", list[0].Name)
	assert.Equal(t, "Zebra Personality", list[1].Name)
}

func TestCustomPersonalityRepo_Update(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewCustomPersonalityRepo(db)
	ctx := context.Background()

	// Create a personality
	p := &models.CustomPersonality{
		Name:         "Original Name",
		Key:          "update_test",
		Description:  "Original description",
		SystemPrompt: "Original prompt",
	}
	require.NoError(t, repo.Create(ctx, p))

	// Update it
	updated := &models.CustomPersonality{
		Name:         "Updated Name",
		Description:  "Updated description",
		SystemPrompt: "Updated prompt",
	}
	err := repo.Update(ctx, "update_test", updated)
	require.NoError(t, err)

	// Verify the update
	got, err := repo.GetByKey(ctx, "update_test")
	require.NoError(t, err)
	assert.Equal(t, "Updated Name", got.Name)
	assert.Equal(t, "Updated description", got.Description)
	assert.Equal(t, "Updated prompt", got.SystemPrompt)
	assert.Equal(t, "update_test", got.Key) // Key should not change

	// Update non-existent should error
	err = repo.Update(ctx, "nonexistent", updated)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCustomPersonalityRepo_Delete(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewCustomPersonalityRepo(db)
	ctx := context.Background()

	// Create a personality
	p := &models.CustomPersonality{
		Name:         "To Delete",
		Key:          "delete_test",
		Description:  "Will be deleted",
		SystemPrompt: "Delete prompt",
	}
	require.NoError(t, repo.Create(ctx, p))

	// Verify it exists
	got, err := repo.GetByKey(ctx, "delete_test")
	require.NoError(t, err)
	assert.NotNil(t, got)

	// Delete it
	err = repo.Delete(ctx, "delete_test")
	require.NoError(t, err)

	// Verify it's gone
	got, err = repo.GetByKey(ctx, "delete_test")
	require.NoError(t, err)
	assert.Nil(t, got)

	// Delete non-existent should error
	err = repo.Delete(ctx, "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCustomPersonalityRepo_UniqueKey(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewCustomPersonalityRepo(db)
	ctx := context.Background()

	// Create a personality
	p1 := &models.CustomPersonality{
		Name:         "First",
		Key:          "unique_key",
		Description:  "First personality",
		SystemPrompt: "First prompt",
	}
	require.NoError(t, repo.Create(ctx, p1))

	// Try to create another with the same key
	p2 := &models.CustomPersonality{
		Name:         "Second",
		Key:          "unique_key",
		Description:  "Second personality",
		SystemPrompt: "Second prompt",
	}
	err := repo.Create(ctx, p2)
	assert.Error(t, err)
}
