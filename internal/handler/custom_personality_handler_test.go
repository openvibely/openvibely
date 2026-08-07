package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupCustomPersonalityHandler(t *testing.T) (*Handler, *echo.Echo, *repository.CustomPersonalityRepo, *repository.SettingsRepo) {
	t.Helper()
	db := testutil.NewTestDB(t)

	projectRepo := repository.NewProjectRepo(db)
	customPersonalityRepo := repository.NewCustomPersonalityRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	projectSvc := service.NewProjectService(projectRepo)

	h := New(
		projectSvc,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		llmConfigRepo, nil, nil, nil, nil, nil, nil, projectRepo, settingsRepo, nil, nil,
	)
	h.SetCustomPersonalityRepo(customPersonalityRepo)

	e := echo.New()
	h.RegisterRoutes(e)

	return h, e, customPersonalityRepo, settingsRepo
}

func TestHandler_CreateCustomPersonality(t *testing.T) {
	h, e, repo, _ := setupCustomPersonalityHandler(t)
	ctx := context.Background()

	form := url.Values{}
	form.Set("name", "Test Personality")
	form.Set("description", "A test personality")
	form.Set("system_prompt", "You are a test assistant with at least 20 characters.")

	req := httptest.NewRequest(http.MethodPost, "/personality/custom", strings.NewReader(form.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.CreateCustomPersonality(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	// Should return re-rendered personality section with the new card
	assert.Contains(t, body, "Test Personality")
	assert.Contains(t, body, "personality-section")
	assert.Contains(t, body, "+ Add Personality")

	got, err := repo.GetByKey(ctx, "test_personality")
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Equal(t, "Test Personality", got.Name)
	assert.Equal(t, "test_personality", got.Key)
}

func TestHandler_GetCustomPersonality(t *testing.T) {
	h, e, repo, _ := setupCustomPersonalityHandler(t)
	ctx := context.Background()

	p := &models.CustomPersonality{
		Name:         "Get Test",
		Key:          "get_test",
		Description:  "Description",
		SystemPrompt: "Test prompt for getting personality data",
	}
	require.NoError(t, repo.Create(ctx, p))

	req := httptest.NewRequest(http.MethodGet, "/personality/custom/get_test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/personality/custom/:key")
	c.SetParamNames("key")
	c.SetParamValues("get_test")

	err := h.GetCustomPersonality(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp models.CustomPersonality
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "Get Test", resp.Name)
	assert.Equal(t, "get_test", resp.Key)
}

func TestHandler_UpdateCustomPersonality(t *testing.T) {
	h, e, repo, _ := setupCustomPersonalityHandler(t)
	ctx := context.Background()

	p := &models.CustomPersonality{
		Name:         "Original",
		Key:          "update_test",
		Description:  "Original desc",
		SystemPrompt: "Original prompt that is long enough",
	}
	require.NoError(t, repo.Create(ctx, p))

	form := url.Values{}
	form.Set("name", "Updated")
	form.Set("description", "Updated desc")
	form.Set("system_prompt", "Updated prompt that is long enough to pass validation")

	req := httptest.NewRequest(http.MethodPut, "/personality/custom/update_test", strings.NewReader(form.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/personality/custom/:key")
	c.SetParamNames("key")
	c.SetParamValues("update_test")

	err := h.UpdateCustomPersonality(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "Updated")
	assert.Contains(t, body, "personality-section")

	got, err := repo.GetByKey(ctx, "update_test")
	require.NoError(t, err)
	assert.Equal(t, "Updated", got.Name)
	assert.Equal(t, "update_test", got.Key)
}

func TestHandler_DeleteCustomPersonality(t *testing.T) {
	h, e, repo, settingsRepo := setupCustomPersonalityHandler(t)
	ctx := context.Background()

	p := &models.CustomPersonality{
		Name:         "Delete Test",
		Key:          "delete_test",
		Description:  "To be deleted",
		SystemPrompt: "Test prompt for deletion testing",
	}
	require.NoError(t, repo.Create(ctx, p))

	// Set it as the active personality
	require.NoError(t, settingsRepo.Set(ctx, "personality", "delete_test"))

	req := httptest.NewRequest(http.MethodDelete, "/personality/custom/delete_test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/personality/custom/:key")
	c.SetParamNames("key")
	c.SetParamValues("delete_test")

	err := h.DeleteCustomPersonality(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	got, err := repo.GetByKey(ctx, "delete_test")
	require.NoError(t, err)
	assert.Nil(t, got)

	// Verify it was reset to default
	personality, _ := settingsRepo.Get(ctx, "personality")
	assert.Equal(t, "", personality)
}

func TestHandler_UpdateCustomPersonality_UpsertsPresetOverride(t *testing.T) {
	h, e, repo, _ := setupCustomPersonalityHandler(t)
	ctx := context.Background()

	// No existing custom row for this preset key.
	got, err := repo.GetByKey(ctx, "pirate_captain")
	require.NoError(t, err)
	require.Nil(t, got)

	form := url.Values{}
	form.Set("name", "Pirate Captain (Edited)")
	form.Set("description", "Custom local pirate style")
	form.Set("system_prompt", "You are a custom pirate prompt override with enough detail.")

	req := httptest.NewRequest(http.MethodPut, "/personality/custom/pirate_captain", strings.NewReader(form.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/personality/custom/:key")
	c.SetParamNames("key")
	c.SetParamValues("pirate_captain")

	err = h.UpdateCustomPersonality(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	got, err = repo.GetByKey(ctx, "pirate_captain")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Pirate Captain (Edited)", got.Name)
	assert.Equal(t, "Custom local pirate style", got.Description)
	assert.Equal(t, "You are a custom pirate prompt override with enough detail.", got.SystemPrompt)
}

func TestHandler_DeleteCustomPersonality_PresetWithoutOverrideResetsActive(t *testing.T) {
	h, e, repo, settingsRepo := setupCustomPersonalityHandler(t)
	ctx := context.Background()

	// Set active to a built-in preset with no custom override.
	require.NoError(t, settingsRepo.Set(ctx, "personality", "pirate_captain"))

	req := httptest.NewRequest(http.MethodDelete, "/personality/custom/pirate_captain", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/personality/custom/:key")
	c.SetParamNames("key")
	c.SetParamValues("pirate_captain")

	err := h.DeleteCustomPersonality(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Active personality should reset to default.
	personality, err := settingsRepo.Get(ctx, "personality")
	require.NoError(t, err)
	assert.Equal(t, "", personality)

	// No custom row should exist for the preset key.
	got, err := repo.GetByKey(ctx, "pirate_captain")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestHandler_PersonalityPage_HeaderAlignsAddButtonWithOtherManagementPages(t *testing.T) {
	h, e, _, _ := setupCustomPersonalityHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/personality", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.handleAppSettings(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, `<div id="personality-container">`)
	assert.Contains(t, body, `<div id="personality-section"`)
	assert.Contains(t, body, `data-selected-personality=""`)
	assert.Contains(t, body, `<div class="flex items-center justify-between mb-6">`)
	assert.Contains(t, body, `<h2 class="text-2xl font-bold">Personality</h2>`)
	assert.Contains(t, body, `<button class="btn btn-primary btn-sm" onclick="openNewPersonalityModal()">`)
	assert.Contains(t, body, `+ Add Personality`)
	assert.NotContains(t, body, `<div class="flex items-center justify-between mb-4">`)
	assert.NotContains(t, body, `<div id="settings-container">`)
	// Search input present
	assert.Contains(t, body, `data-card-search="personality"`)
}

func TestHandler_PersonalityPage_CardRendering(t *testing.T) {
	h, e, repo, settingsRepo := setupCustomPersonalityHandler(t)
	ctx := context.Background()

	// Set active personality
	require.NoError(t, settingsRepo.Set(ctx, "personality", "sarcastic_engineer"))

	// Create a custom personality
	custom := &models.CustomPersonality{
		Name:         "My Custom Bot",
		Key:          "my_custom_bot",
		Description:  "A custom bot personality",
		SystemPrompt: "You are my custom bot with unique behavior and personality traits.",
	}
	require.NoError(t, repo.Create(ctx, custom))

	// Create a built-in override
	override := &models.CustomPersonality{
		Name:         "Pirate Captain (Overridden)",
		Key:          "pirate_captain",
		Description:  "My pirate override",
		SystemPrompt: "You be a custom pirate, arr matey with special behavior!",
	}
	require.NoError(t, repo.Create(ctx, override))

	req := httptest.NewRequest(http.MethodGet, "/personality", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.handleAppSettings(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()

	// Add Personality button
	assert.Contains(t, body, "+ Add Personality")

	// Base card (no personality)
	assert.Contains(t, body, "Base")
	assert.Contains(t, body, "No personality prompt applied.")
	assert.Contains(t, body, `data-personality-is-default-card="true"`)
	// data-selected-personality should be on section root
	assert.Contains(t, body, `data-selected-personality="sarcastic_engineer"`)

	// Built-in personality cards
	assert.Contains(t, body, "Sarcastic Engineer")
	assert.Contains(t, body, "Zen Debugger")

	// Active badge on sarcastic engineer
	// (The Active badge should appear in the sarcastic_engineer card)

	// Built-in override should show overridden name
	assert.Contains(t, body, "Pirate Captain (Overridden)")
	assert.Contains(t, body, "Override") // Override badge

	// Custom personality card
	assert.Contains(t, body, "My Custom Bot")
	assert.Contains(t, body, "Custom") // Custom badge
	assert.Contains(t, body, "A custom bot personality")

	// Prompt snippet preview
	assert.Contains(t, body, "You be a custom pirate")

	// Modal should be present
	assert.Contains(t, body, "personality_modal")

	// Set as Default should be in the kebab menus for non-active cards
	assert.Contains(t, body, "Set as Default")
}

func TestHandler_PersonalityPage_DefaultCardNotClickable(t *testing.T) {
	h, e, _, _ := setupCustomPersonalityHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/personality", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.handleAppSettings(c)
	require.NoError(t, err)

	body := rec.Body.String()

	// The default card should NOT have onclick="editPersonalityFromData"
	// We check by verifying the default card has the marker attribute
	assert.Contains(t, body, `data-personality-is-default-card="true"`)
	// But should NOT contain cursor-pointer for the default card specifically
	// (the default card div should not have the editPersonalityFromData onclick)
	// The default card is the only one without onclick="editPersonalityFromData"
}

func TestHandler_PersonalityPage_BaseCardKebabHiddenWhenSelected(t *testing.T) {
	h, e, _, _ := setupCustomPersonalityHandler(t)

	// Default personality is "" (base selected)
	req := httptest.NewRequest(http.MethodGet, "/personality", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.handleAppSettings(c)
	require.NoError(t, err)

	body := rec.Body.String()

	// Base card should have Default badge
	assert.Contains(t, body, "Default")
	assert.Contains(t, body, `data-personality-is-default-card="true"`)
	assert.Contains(t, body, `badge badge-sm ml-2 ov-badge-default">Default</span>`)
	assert.NotContains(t, body, `badge badge-neutral badge-sm ml-2">Default</span>`)

	// When base is selected, the base card should NOT have the kebab menu
	// (no dropdown with Set as Default for the base card)
	// Find the base card section - it should not contain handleDropdownToggle
	// since the kebab is conditionally hidden when personality == ""
	baseCardIdx := strings.Index(body, `data-personality-is-default-card="true"`)
	require.Greater(t, baseCardIdx, 0)
	// Find next card after the base card
	nextCardIdx := strings.Index(body[baseCardIdx:], `data-personality-key="sarcastic_engineer"`)
	require.Greater(t, nextCardIdx, 0)
	baseCardHTML := body[baseCardIdx : baseCardIdx+nextCardIdx]
	// Base card should NOT contain kebab dropdown when selected
	assert.NotContains(t, baseCardHTML, "handleDropdownToggle")
}

func TestHandler_PersonalityPage_BaseCardKebabShownWhenNotSelected(t *testing.T) {
	h, e, _, settingsRepo := setupCustomPersonalityHandler(t)
	ctx := context.Background()

	// Set personality to something else so base is not selected
	require.NoError(t, settingsRepo.Set(ctx, "personality", "zen_debugger"))

	req := httptest.NewRequest(http.MethodGet, "/personality", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.handleAppSettings(c)
	require.NoError(t, err)

	body := rec.Body.String()

	// Base card should have kebab with "Set as Default" when not selected
	baseCardIdx := strings.Index(body, `data-personality-is-default-card="true"`)
	require.Greater(t, baseCardIdx, 0)
	nextCardIdx := strings.Index(body[baseCardIdx:], `data-personality-key="sarcastic_engineer"`)
	require.Greater(t, nextCardIdx, 0)
	baseCardHTML := body[baseCardIdx : baseCardIdx+nextCardIdx]
	assert.Contains(t, baseCardHTML, "handleDropdownToggle")
	assert.Contains(t, baseCardHTML, "Set as Default")
}

func TestHandler_PersonalityPage_ActiveBadgesUseCanonicalClass(t *testing.T) {
	h, e, _, settingsRepo := setupCustomPersonalityHandler(t)
	ctx := context.Background()

	req := httptest.NewRequest(http.MethodGet, "/personality", nil)

	// Built-in selected personality should render canonical Active badge class.
	require.NoError(t, settingsRepo.Set(ctx, "personality", "zen_debugger"))
	recBuiltIn := httptest.NewRecorder()
	cBuiltIn := e.NewContext(req, recBuiltIn)
	err := h.handleAppSettings(cBuiltIn)
	require.NoError(t, err)
	bodyBuiltIn := recBuiltIn.Body.String()
	assert.Contains(t, bodyBuiltIn, `badge badge-sm ml-2 ov-badge-default">Active</span>`)
	assert.NotContains(t, bodyBuiltIn, `badge badge-primary badge-sm ml-2">Active</span>`)

	// Custom selected personality should also render canonical Active badge class.
	p := &models.CustomPersonality{
		Name:         "Custom Active",
		Key:          "custom_active",
		Description:  "Custom active description",
		SystemPrompt: "A sufficiently long custom personality prompt for active badge verification.",
	}
	require.NoError(t, h.customPersonalityRepo.Create(ctx, p))
	require.NoError(t, settingsRepo.Set(ctx, "personality", p.Key))

	recCustom := httptest.NewRecorder()
	cCustom := e.NewContext(httptest.NewRequest(http.MethodGet, "/personality", nil), recCustom)
	err = h.handleAppSettings(cCustom)
	require.NoError(t, err)
	bodyCustom := recCustom.Body.String()
	assert.Contains(t, bodyCustom, `badge badge-sm ml-2 ov-badge-default">Active</span>`)
	assert.NotContains(t, bodyCustom, `badge badge-primary badge-sm ml-2">Active</span>`)
}

func TestHandler_PersonalityPage_SelectedDefaultCardRendersFirst(t *testing.T) {
	h, e, _, settingsRepo := setupCustomPersonalityHandler(t)
	ctx := context.Background()

	// Built-in selected default should render before Base card.
	require.NoError(t, settingsRepo.Set(ctx, "personality", "zen_debugger"))
	recBuiltIn := httptest.NewRecorder()
	cBuiltIn := e.NewContext(httptest.NewRequest(http.MethodGet, "/personality", nil), recBuiltIn)
	err := h.handleAppSettings(cBuiltIn)
	require.NoError(t, err)
	bodyBuiltIn := recBuiltIn.Body.String()
	builtInIdx := strings.Index(bodyBuiltIn, `data-personality-key="zen_debugger"`)
	baseIdx := strings.Index(bodyBuiltIn, `data-personality-is-default-card="true"`)
	require.Greater(t, builtInIdx, 0)
	require.Greater(t, baseIdx, 0)
	assert.Less(t, builtInIdx, baseIdx)

	// Custom selected default should render before Base card too.
	p := &models.CustomPersonality{
		Name:         "Custom First",
		Key:          "custom_first",
		Description:  "Custom should render first when selected",
		SystemPrompt: "A sufficiently long custom personality prompt to verify selected card ordering.",
	}
	require.NoError(t, h.customPersonalityRepo.Create(ctx, p))
	require.NoError(t, settingsRepo.Set(ctx, "personality", p.Key))
	recCustom := httptest.NewRecorder()
	cCustom := e.NewContext(httptest.NewRequest(http.MethodGet, "/personality", nil), recCustom)
	err = h.handleAppSettings(cCustom)
	require.NoError(t, err)
	bodyCustom := recCustom.Body.String()
	customIdx := strings.Index(bodyCustom, `data-personality-key="custom_first"`)
	baseIdx = strings.Index(bodyCustom, `data-personality-is-default-card="true"`)
	require.Greater(t, customIdx, 0)
	require.Greater(t, baseIdx, 0)
	assert.Less(t, customIdx, baseIdx)
}

func TestHandler_PersonalityPage_BaseCardNeverHighlighted(t *testing.T) {
	h, e, _, _ := setupCustomPersonalityHandler(t)

	// Default personality is "" (base selected)
	req := httptest.NewRequest(http.MethodGet, "/personality", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.handleAppSettings(c)
	require.NoError(t, err)

	body := rec.Body.String()

	// Find the base card div — it should never have ring-2 ring-primary
	baseCardIdx := strings.Index(body, `data-personality-is-default-card="true"`)
	require.Greater(t, baseCardIdx, 0)
	// Look backwards from the data attribute to find the opening div class
	// The base card class should NOT contain ring-2 ring-primary
	cardStart := strings.LastIndex(body[:baseCardIdx], "<div")
	require.Greater(t, cardStart, 0)
	baseCardOpen := body[cardStart:baseCardIdx]
	assert.NotContains(t, baseCardOpen, "ring-2 ring-primary")
}

func TestHandler_PersonalityPage_DialogSetAsDefaultAction(t *testing.T) {
	h, e, _, settingsRepo := setupCustomPersonalityHandler(t)
	ctx := context.Background()

	require.NoError(t, settingsRepo.Set(ctx, "personality", "zen_debugger"))

	req := httptest.NewRequest(http.MethodGet, "/personality", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.handleAppSettings(c)
	require.NoError(t, err)

	body := rec.Body.String()

	// The dialog JS should contain Set as Default logic
	assert.Contains(t, body, "Set as Default")
	// data-selected-personality should be present for JS detection
	assert.Contains(t, body, `data-selected-personality="zen_debugger"`)
	// The JS should reference data.selectedPersonality for conditional rendering
	assert.Contains(t, body, "selectedPersonality")
}

func TestHandler_PersonalitySave_SetAsDefault_UpdatesCardState(t *testing.T) {
	h, e, _, settingsRepo := setupCustomPersonalityHandler(t)
	ctx := context.Background()

	// Initially set to pirate_captain
	require.NoError(t, settingsRepo.Set(ctx, "personality", "pirate_captain"))

	// Use the kebab menu "Set as Default" pattern (query param)
	req := httptest.NewRequest(http.MethodPost, "/personality/save?personality=zen_debugger", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.handlePersonalitySave(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	// The returned HTML should have personality-section
	assert.Contains(t, body, "personality-section")
	// Zen Debugger should now be active and rendered with canonical active badge styling
	assert.Contains(t, body, "Zen Debugger")
	assert.Contains(t, body, `badge badge-sm ml-2 ov-badge-default">Active</span>`)
	assert.NotContains(t, body, "ring-2 ring-primary")

	// Verify DB state
	val, err := settingsRepo.Get(ctx, "personality")
	require.NoError(t, err)
	assert.Equal(t, "zen_debugger", val)
}

func TestHandler_PersonalitySave_SetDefaultNoPersonality(t *testing.T) {
	h, e, _, settingsRepo := setupCustomPersonalityHandler(t)
	ctx := context.Background()

	// Set to something first
	require.NoError(t, settingsRepo.Set(ctx, "personality", "zen_debugger"))

	// Set to empty (Default No Personality) via query param
	req := httptest.NewRequest(http.MethodPost, "/personality/save?personality=", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.handlePersonalitySave(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "personality-section")

	// Verify DB state is empty (default)
	val, err := settingsRepo.Get(ctx, "personality")
	require.NoError(t, err)
	assert.Equal(t, "", val)
}

func TestHandler_PersonalityPage_BuiltinOverrideResetBehavior(t *testing.T) {
	h, e, repo, settingsRepo := setupCustomPersonalityHandler(t)
	ctx := context.Background()

	// Create an override for a built-in
	override := &models.CustomPersonality{
		Name:         "Zen Debugger (Custom)",
		Key:          "zen_debugger",
		Description:  "Custom zen",
		SystemPrompt: "You are a deeply custom zen debugger with extended behavior.",
	}
	require.NoError(t, repo.Create(ctx, override))
	require.NoError(t, settingsRepo.Set(ctx, "personality", "zen_debugger"))

	// Delete the override (reset to default) via form handler
	req := httptest.NewRequest(http.MethodDelete, "/personality/custom/zen_debugger", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/personality/custom/:key")
	c.SetParamNames("key")
	c.SetParamValues("zen_debugger")

	err := h.DeleteCustomPersonality(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	// The override row should be gone
	assert.NotContains(t, body, "Zen Debugger (Custom)")
	// But the built-in zen debugger should still be listed
	assert.Contains(t, body, "Zen Debugger")

	// Custom override should be gone from DB
	got, err := repo.GetByKey(ctx, "zen_debugger")
	require.NoError(t, err)
	assert.Nil(t, got)

	// Active personality should be reset since the deleted key was active
	val, err := settingsRepo.Get(ctx, "personality")
	require.NoError(t, err)
	assert.Equal(t, "", val)
}
