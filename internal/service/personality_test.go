package service

import (
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestGetPersonalityPrompt(t *testing.T) {
	// Default/empty personality returns empty string
	if got := GetPersonalityPrompt(""); got != "" {
		t.Errorf("expected empty string for empty personality, got %q", got)
	}

	// Unknown personality returns empty string
	if got := GetPersonalityPrompt("unknown_personality"); got != "" {
		t.Errorf("expected empty string for unknown personality, got %q", got)
	}

	// All defined personalities return non-empty prompts
	for _, p := range AllPersonalities() {
		if p.Key == "" {
			continue // Skip the "Default" entry
		}
		prompt := GetPersonalityPrompt(p.Key)
		if prompt == "" {
			t.Errorf("expected non-empty prompt for personality %q, got empty string", p.Key)
		}
	}
}

func TestAllPersonalities(t *testing.T) {
	personalities := AllPersonalities()

	// Should have 16 entries (15 personalities + 1 default)
	if len(personalities) != 16 {
		t.Errorf("expected 16 personalities, got %d", len(personalities))
	}

	// First entry should be the default (empty key)
	if personalities[0].Key != "" {
		t.Errorf("expected first personality to be default (empty key), got %q", personalities[0].Key)
	}

	// All non-default entries should have non-empty Key, Name, and Description
	keys := make(map[string]bool)
	for _, p := range personalities {
		if p.Name == "" {
			t.Error("personality Name should not be empty")
		}
		if p.Description == "" {
			t.Error("personality Description should not be empty")
		}
		if keys[p.Key] {
			t.Errorf("duplicate personality key: %q", p.Key)
		}
		keys[p.Key] = true
	}

	// Verify specific personalities exist
	expectedKeys := []string{
		"sarcastic_engineer", "no_nonsense_pro", "optimistic_mentor",
		"academic_professor", "zen_debugger", "caffeinated_hacker",
		"startup_hustler", "game_master", "dad_joke_developer",
		"pirate_captain", "movie_quote_bot", "time_traveler",
		"security_paranoid", "performance_obsessed", "accessibility_champion",
	}
	for _, key := range expectedKeys {
		if !keys[key] {
			t.Errorf("expected personality %q to exist", key)
		}
	}
}

func TestGetPersonalityPrompt_SpecificContent(t *testing.T) {
	tests := []struct {
		key      string
		contains string
	}{
		{"sarcastic_engineer", "sarcastic"},
		{"pirate_captain", "pirate"},
		{"zen_debugger", "calm"},
		{"caffeinated_hacker", "enthusiastic"},
		{"security_paranoid", "security"},
		{"performance_obsessed", "speed"},
		{"accessibility_champion", "accessibility"},
	}

	for _, tt := range tests {
		prompt := GetPersonalityPrompt(tt.key)
		if prompt == "" {
			t.Errorf("GetPersonalityPrompt(%q) returned empty string", tt.key)
			continue
		}
		if !strings.Contains(strings.ToLower(prompt), strings.ToLower(tt.contains)) {
			t.Errorf("GetPersonalityPrompt(%q) should contain %q, got %q", tt.key, tt.contains, prompt)
		}
	}
}

func TestIsPresetPersonality(t *testing.T) {
	if !IsPresetPersonality("") {
		t.Error("empty key should be a preset")
	}
	if !IsPresetPersonality("pirate_captain") {
		t.Error("pirate_captain should be a preset")
	}
	if IsPresetPersonality("custom_key") {
		t.Error("custom_key should not be a preset")
	}
}

func TestAllPersonalitiesWithCustom(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewCustomPersonalityRepo(db)
	ctx := context.Background()

	// Without custom personalities, should return same as AllPersonalities
	result := AllPersonalitiesWithCustom(ctx, repo)
	if len(result) != 16 {
		t.Errorf("expected 16, got %d", len(result))
	}

	// Add a custom personality
	cp := &models.CustomPersonality{
		Name:         "Custom Test",
		Key:          "custom_test",
		Description:  "A custom test personality",
		SystemPrompt: "Be a custom test personality.",
	}
	if err := repo.Create(ctx, cp); err != nil {
		t.Fatalf("create: %v", err)
	}

	result = AllPersonalitiesWithCustom(ctx, repo)
	if len(result) != 17 {
		t.Errorf("expected 17, got %d", len(result))
	}

	// Last entry should be the custom personality
	last := result[len(result)-1]
	if last.Key != "custom_test" {
		t.Errorf("expected last key 'custom_test', got %q", last.Key)
	}
	if !last.IsCustom {
		t.Error("custom personality should have IsCustom=true")
	}
}

func TestAllPersonalitiesWithCustom_NilRepo(t *testing.T) {
	result := AllPersonalitiesWithCustom(context.Background(), nil)
	if len(result) != 16 {
		t.Errorf("with nil repo, expected 16, got %d", len(result))
	}
}

func TestGetPersonalityPromptWithCustom(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewCustomPersonalityRepo(db)
	ctx := context.Background()

	// Create a custom personality
	cp := &models.CustomPersonality{
		Name:         "Custom Prompt Test",
		Key:          "custom_prompt_test",
		Description:  "Test custom prompt resolution",
		SystemPrompt: "This is the custom system prompt.",
	}
	if err := repo.Create(ctx, cp); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Custom personality should return its system prompt
	got := GetPersonalityPromptWithCustom(ctx, "custom_prompt_test", repo)
	if got != "This is the custom system prompt." {
		t.Errorf("expected custom prompt, got %q", got)
	}

	// Preset personality should still work
	got = GetPersonalityPromptWithCustom(ctx, "pirate_captain", repo)
	if got == "" {
		t.Error("expected preset prompt for pirate_captain")
	}

	// Empty key should return empty
	got = GetPersonalityPromptWithCustom(ctx, "", repo)
	if got != "" {
		t.Errorf("expected empty for empty key, got %q", got)
	}

	// Unknown key should return empty
	got = GetPersonalityPromptWithCustom(ctx, "nonexistent", repo)
	if got != "" {
		t.Errorf("expected empty for unknown key, got %q", got)
	}

	// Nil repo should fall back to presets
	got = GetPersonalityPromptWithCustom(ctx, "pirate_captain", nil)
	if got == "" {
		t.Error("expected preset prompt with nil repo")
	}
}
