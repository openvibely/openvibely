package service

import (
	"context"
	"encoding/json"
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

func TestIsAvailablePersonalityKey(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewCustomPersonalityRepo(db)
	ctx := context.Background()
	cp := &models.CustomPersonality{
		Name:         "Available Custom",
		Key:          "available_custom",
		Description:  "A custom test personality",
		SystemPrompt: "Be a custom test personality with enough text.",
	}
	if err := repo.Create(ctx, cp); err != nil {
		t.Fatalf("create: %v", err)
	}

	if !IsAvailablePersonalityKey(ctx, "", repo) {
		t.Error("empty key should be available for base/default")
	}
	if !IsAvailablePersonalityKey(ctx, "pirate_captain", repo) {
		t.Error("built-in key should be available")
	}
	if !IsAvailablePersonalityKey(ctx, "available_custom", repo) {
		t.Error("existing custom key should be available")
	}
	if IsAvailablePersonalityKey(ctx, "missing_custom", repo) {
		t.Error("missing custom key should not be available")
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

func TestExecuteSaveCustomPersonalityRuntime_CreateUpdateAndActivate(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	customRepo := repository.NewCustomPersonalityRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)

	out, err := ExecuteSaveCustomPersonalityRuntime(ctx, CustomPersonalitySaveOptions{
		Input:                 json.RawMessage(`{"mode":"create","name":" Release Notes Reviewer ","description":" Concise release-note review ","system_prompt":" You review release notes with concise, user-facing clarity. ","activate":true}`),
		CustomPersonalityRepo: customRepo,
		SettingsRepo:          settingsRepo,
	})
	if err != nil {
		t.Fatalf("create custom personality: %v", err)
	}
	var createResp customPersonalitySaveResponse
	if err := json.Unmarshal([]byte(out), &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if !createResp.OK || createResp.Mode != "create" || createResp.Key != "release_notes_reviewer" || !createResp.Activated {
		t.Fatalf("unexpected create response: %+v", createResp)
	}
	created, err := customRepo.GetByKey(ctx, "release_notes_reviewer")
	if err != nil || created == nil {
		t.Fatalf("created personality missing: personality=%+v err=%v", created, err)
	}
	if created.Name != "Release Notes Reviewer" || created.Description != "Concise release-note review" || created.SystemPrompt != "You review release notes with concise, user-facing clarity." {
		t.Fatalf("created personality not trimmed/persisted correctly: %+v", created)
	}
	if current, err := settingsRepo.Get(ctx, "personality"); err != nil || current != "release_notes_reviewer" {
		t.Fatalf("active personality = %q err=%v, want release_notes_reviewer", current, err)
	}
	if !IsAvailablePersonalityKey(ctx, "release_notes_reviewer", customRepo) {
		t.Fatal("created custom personality should be available to list/select flows")
	}

	out, err = ExecuteSaveCustomPersonalityRuntime(ctx, CustomPersonalitySaveOptions{
		Input:                 json.RawMessage(`{"mode":"update","key":"release_notes_reviewer","name":"Release Reviewer","description":"Updated description","system_prompt":"You now review release notes with stricter product clarity and brevity."}`),
		CustomPersonalityRepo: customRepo,
		SettingsRepo:          settingsRepo,
	})
	if err != nil {
		t.Fatalf("update custom personality: %v", err)
	}
	var updateResp customPersonalitySaveResponse
	if err := json.Unmarshal([]byte(out), &updateResp); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updateResp.Mode != "update" || updateResp.Key != "release_notes_reviewer" || updateResp.Activated {
		t.Fatalf("unexpected update response: %+v", updateResp)
	}
	updated, err := customRepo.GetByKey(ctx, "release_notes_reviewer")
	if err != nil || updated == nil {
		t.Fatalf("updated personality missing: personality=%+v err=%v", updated, err)
	}
	if updated.Name != "Release Reviewer" || GetPersonalityPromptWithCustom(ctx, "release_notes_reviewer", customRepo) != "You now review release notes with stricter product clarity and brevity." {
		t.Fatalf("updated personality not reflected in custom prompt lookup: %+v", updated)
	}
}

func TestExecuteSaveCustomPersonalityRuntime_RejectsInvalidInputsWithoutPartialWrites(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	customRepo := repository.NewCustomPersonalityRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	if err := customRepo.Create(ctx, &models.CustomPersonality{
		Name:         "Existing Custom",
		Key:          "existing_custom",
		Description:  "Original description",
		SystemPrompt: "Original custom personality prompt with enough detail.",
	}); err != nil {
		t.Fatalf("seed custom personality: %v", err)
	}
	if err := settingsRepo.Set(ctx, "personality", "existing_custom"); err != nil {
		t.Fatalf("seed active personality: %v", err)
	}

	cases := []struct {
		name        string
		input       string
		wantErr     string
		wantMissing string
	}{
		{name: "duplicate create key", input: `{"mode":"create","key":"existing_custom","name":"Duplicate","system_prompt":"Duplicate custom personality prompt with enough detail."}`, wantErr: `already exists`},
		{name: "blank generated key", input: `{"mode":"create","name":"!!!","system_prompt":"Punctuation-only name has a long enough prompt."}`, wantErr: `Key is required`, wantMissing: ""},
		{name: "blank name", input: `{"mode":"create","name":" ","key":"blank_name","system_prompt":"Prompt text long enough for validation."}`, wantErr: `Name is required`, wantMissing: "blank_name"},
		{name: "short prompt", input: `{"mode":"create","name":"Short Prompt","key":"short_prompt","system_prompt":"too short"}`, wantErr: `System prompt must be at least 20 characters`, wantMissing: "short_prompt"},
		{name: "missing update target", input: `{"mode":"update","key":"missing_custom","name":"Missing","system_prompt":"Missing target prompt with enough detail."}`, wantErr: `not found`},
		{name: "built-in update target", input: `{"mode":"update","key":"pirate_captain","name":"Pirate Override","system_prompt":"Attempt to mutate built in prompt with enough detail."}`, wantErr: `Cannot update built-in personality`},
		{name: "invalid mode", input: `{"mode":"delete","name":"No Delete","system_prompt":"Delete mode prompt with enough detail."}`, wantErr: `mode must be create or update`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ExecuteSaveCustomPersonalityRuntime(ctx, CustomPersonalitySaveOptions{
				Input:                 json.RawMessage(tt.input),
				CustomPersonalityRepo: customRepo,
				SettingsRepo:          settingsRepo,
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
			if tt.wantMissing != "" {
				got, getErr := customRepo.GetByKey(ctx, tt.wantMissing)
				if getErr != nil || got != nil {
					t.Fatalf("unexpected partial write for %q: personality=%+v err=%v", tt.wantMissing, got, getErr)
				}
			}
			current, getErr := settingsRepo.Get(ctx, "personality")
			if getErr != nil || current != "existing_custom" {
				t.Fatalf("active personality changed after failed save: %q err=%v", current, getErr)
			}
		})
	}
	existing, err := customRepo.GetByKey(ctx, "existing_custom")
	if err != nil || existing == nil || existing.Name != "Existing Custom" || existing.SystemPrompt != "Original custom personality prompt with enough detail." {
		t.Fatalf("existing custom personality changed after failed saves: %+v err=%v", existing, err)
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
