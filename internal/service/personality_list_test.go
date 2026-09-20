package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func seedCustomPersonalities(t *testing.T, repo *repository.CustomPersonalityRepo, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		p := &models.CustomPersonality{
			Name:         fmt.Sprintf("Custom %03d", i),
			Key:          fmt.Sprintf("custom_%03d", i),
			Description:  strings.Repeat("d", 40) + fmt.Sprintf(" %03d", i),
			SystemPrompt: "Custom personality system prompt for runtime paging tests.",
		}
		if err := repo.Create(ctx, p); err != nil {
			t.Fatalf("create custom %d: %v", i, err)
		}
	}
}

func TestPersonalityListPageArgs(t *testing.T) {
	limit, offset, err := PersonalityListPageArgs(nil)
	if err != nil {
		t.Fatalf("omitted: %v", err)
	}
	if limit != PersonalityListDefaultLimit || offset != 0 {
		t.Fatalf("omitted got limit=%d offset=%d", limit, offset)
	}
	limit, offset, err = PersonalityListPageArgs(json.RawMessage(`{}`))
	if err != nil || limit != 20 || offset != 0 {
		t.Fatalf("empty object got limit=%d offset=%d err=%v", limit, offset, err)
	}
	limit, offset, err = PersonalityListPageArgs(json.RawMessage(`{"limit":5,"offset":16}`))
	if err != nil || limit != 5 || offset != 16 {
		t.Fatalf("explicit got limit=%d offset=%d err=%v", limit, offset, err)
	}
	if _, _, err := PersonalityListPageArgs(json.RawMessage(`{"limit":0}`)); err == nil {
		t.Fatal("explicit zero limit should fail")
	}
	if _, _, err := PersonalityListPageArgs(json.RawMessage(`{"Limit":0}`)); err == nil {
		t.Fatal("explicit Limit:0 should fail")
	}
	if _, _, err := PersonalityListPageArgs(json.RawMessage(`{"limit":51}`)); err == nil {
		t.Fatal("over max should fail")
	}
	if _, _, err := PersonalityListPageArgs(json.RawMessage(`{"offset":-1}`)); err == nil {
		t.Fatal("negative offset should fail")
	}
}

func TestListPersonalitiesRuntimePage_DefaultFirstPageAndOffset(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewCustomPersonalityRepo(db)
	ctx := context.Background()
	seedCustomPersonalities(t, repo, 25)

	page, err := ListPersonalitiesRuntimePage(ctx, repo, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	presets := len(AllPersonalities())
	if page.Total != presets+25 || page.Returned != 20 || !page.HasMore || page.NextOffset != 20 {
		t.Fatalf("first page %+v", page)
	}
	if page.Items[0].Key != "" || page.Items[0].Name != "Base" {
		t.Fatalf("first item should be built-in default, got %+v", page.Items[0])
	}
	customOnFirst := 0
	for _, item := range page.Items {
		if item.IsCustom {
			customOnFirst++
		}
	}
	if customOnFirst != 4 {
		t.Fatalf("expected 4 customs on first page, got %d", customOnFirst)
	}

	second, err := ListPersonalitiesRuntimePage(ctx, repo, 20, 20)
	if err != nil {
		t.Fatal(err)
	}
	if second.Returned != 20 || !second.Items[0].IsCustom || second.Items[0].Key != "custom_004" {
		t.Fatalf("second page %+v first=%+v", second, second.Items[0])
	}

	empty, err := ListPersonalitiesRuntimePage(ctx, repo, 20, 500)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Returned != 0 || empty.HasMore {
		t.Fatalf("empty page %+v", empty)
	}
}

func TestListPersonalitiesRuntimePage_PageSizeCapAndEmptyCatalog(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewCustomPersonalityRepo(db)
	ctx := context.Background()

	page, err := ListPersonalitiesRuntimePage(ctx, repo, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != len(AllPersonalities()) || page.Returned != len(AllPersonalities()) || page.HasMore {
		t.Fatalf("empty custom catalog %+v", page)
	}
	if _, err := ListPersonalitiesRuntimePage(ctx, repo, 51, 0); err == nil {
		t.Fatal("limit 51 should fail")
	}
}

func TestFindPersonality_ExactKeyWithoutFullCatalog(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewCustomPersonalityRepo(db)
	ctx := context.Background()
	seedCustomPersonalities(t, repo, 3)

	got, ok := FindPersonality(ctx, "custom_002", repo)
	if !ok || got.Key != "custom_002" || !got.IsCustom {
		t.Fatalf("custom lookup %+v ok=%t", got, ok)
	}
	got, ok = FindPersonality(ctx, "no_nonsense_pro", repo)
	if !ok || got.Key != "no_nonsense_pro" || got.IsCustom {
		t.Fatalf("builtin lookup %+v ok=%t", got, ok)
	}
	if _, ok := FindPersonality(ctx, "missing_custom", repo); ok {
		t.Fatal("missing custom should fail")
	}
}

func TestExecuteListPersonalitiesTool_SelectedPersonalityAndUnknownKey(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	customRepo := repository.NewCustomPersonalityRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	seedCustomPersonalities(t, customRepo, 8)
	if err := settingsRepo.Set(ctx, "personality", "custom_007"); err != nil {
		t.Fatal(err)
	}

	out, err := ExecuteListPersonalitiesTool(ctx, customRepo, settingsRepo, json.RawMessage(`{"limit":5}`), true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Available Personalities") || !strings.Contains(out, "Current personality: **custom_007**") || !strings.Contains(out, "has_more=true") {
		t.Fatalf("web output:\n%s", out)
	}
	if strings.Contains(out, "(key: `custom_007`") {
		t.Fatalf("custom_007 should not be on the first 5-item page:\n%s", out)
	}

	channel, err := ExecuteListPersonalitiesTool(ctx, customRepo, settingsRepo, json.RawMessage(`{"limit":5,"offset":16}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(channel, "Current personality: custom_007") || !strings.Contains(channel, "custom_000") {
		t.Fatalf("channel page:\n%s", channel)
	}

	if _, err := ExecuteListPersonalitiesTool(ctx, customRepo, settingsRepo, json.RawMessage(`{"limit":0}`), true); err == nil {
		t.Fatal("explicit zero should error")
	}

	if _, ok := FindPersonality(ctx, "custom_007", customRepo); !ok {
		t.Fatal("set_personality must still resolve a custom key off the current page")
	}
}

func TestFormatPersonalityListPage_TruncatesLongDescriptions(t *testing.T) {
	page := PersonalityListPage{
		Items: []PersonalityInfo{{
			Key:         "wordy",
			Name:        "Wordy",
			Description: strings.Repeat("long description ", 40),
			IsCustom:    true,
		}},
		Limit: 20, Offset: 0, Returned: 1, Total: 1,
	}
	out := FormatPersonalityListPage(page, "wordy", true)
	if strings.Count(out, "long description") > 12 {
		t.Fatalf("description was not compacted:\n%s", out)
	}
	if !strings.Contains(out, "...") {
		t.Fatal("expected truncated description")
	}
}
