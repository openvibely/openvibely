package service

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestParseImpactAnalysisResponse_ValidJSON(t *testing.T) {
	input := `{
		"files": ["internal/handler/handler.go", "internal/service/llm_service.go"],
		"apis": ["/api/tasks", "/api/projects"],
		"schemas": ["tasks.status"],
		"components": ["TaskService", "WorkerService"],
		"summary": "Modifies the task execution pipeline",
		"confidence": 0.85
	}`

	response, err := parseImpactAnalysisResponse(input)
	if err != nil {
		t.Fatalf("parseImpactAnalysisResponse failed: %v", err)
	}

	if len(response.Files) != 2 {
		t.Errorf("expected 2 files, got %d", len(response.Files))
	}
	if len(response.APIs) != 2 {
		t.Errorf("expected 2 APIs, got %d", len(response.APIs))
	}
	if len(response.Schemas) != 1 {
		t.Errorf("expected 1 schema, got %d", len(response.Schemas))
	}
	if len(response.Components) != 2 {
		t.Errorf("expected 2 components, got %d", len(response.Components))
	}
	if response.Confidence != 0.85 {
		t.Errorf("expected confidence 0.85, got %f", response.Confidence)
	}
	if response.Summary != "Modifies the task execution pipeline" {
		t.Errorf("unexpected summary: %q", response.Summary)
	}
}

func TestParseImpactAnalysisResponse_WithMarkdownFences(t *testing.T) {
	input := "```json\n{\"files\":[\"test.go\"],\"apis\":[],\"schemas\":[],\"components\":[],\"summary\":\"test\",\"confidence\":0.5}\n```"

	response, err := parseImpactAnalysisResponse(input)
	if err != nil {
		t.Fatalf("parseImpactAnalysisResponse with fences failed: %v", err)
	}
	if len(response.Files) != 1 {
		t.Errorf("expected 1 file, got %d", len(response.Files))
	}
}

func TestParseImpactAnalysisResponse_WithExtraText(t *testing.T) {
	input := "Here is the analysis:\n{\"files\":[\"test.go\"],\"apis\":[],\"schemas\":[],\"components\":[],\"summary\":\"test\",\"confidence\":0.5}\nHope this helps!"

	response, err := parseImpactAnalysisResponse(input)
	if err != nil {
		t.Fatalf("parseImpactAnalysisResponse with extra text failed: %v", err)
	}
	if len(response.Files) != 1 {
		t.Errorf("expected 1 file, got %d", len(response.Files))
	}
}

func TestParseImpactAnalysisResponse_ClampConfidence(t *testing.T) {
	input := `{"files":[],"apis":[],"schemas":[],"components":[],"summary":"test","confidence":1.5}`

	response, err := parseImpactAnalysisResponse(input)
	if err != nil {
		t.Fatalf("parseImpactAnalysisResponse failed: %v", err)
	}
	if response.Confidence != 1 {
		t.Errorf("expected confidence clamped to 1, got %f", response.Confidence)
	}

	input = `{"files":[],"apis":[],"schemas":[],"components":[],"summary":"test","confidence":-0.5}`
	response, err = parseImpactAnalysisResponse(input)
	if err != nil {
		t.Fatalf("parseImpactAnalysisResponse failed: %v", err)
	}
	if response.Confidence != 0 {
		t.Errorf("expected confidence clamped to 0, got %f", response.Confidence)
	}
}

func TestCompareImpactAnalyses_FileConflict(t *testing.T) {
	a := &models.ImpactAnalysis{
		FilesImpacted:      `["handler.go","service.go","repo.go"]`,
		APIsImpacted:       `[]`,
		SchemasImpacted:    `[]`,
		ComponentsImpacted: `[]`,
	}
	b := &models.ImpactAnalysis{
		FilesImpacted:      `["handler.go","model.go"]`,
		APIsImpacted:       `[]`,
		SchemasImpacted:    `[]`,
		ComponentsImpacted: `[]`,
	}

	results := compareImpactAnalyses(a, b)
	if len(results) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(results))
	}
	if !results[0].HasConflict {
		t.Error("expected conflict")
	}
	if results[0].ConflictType != models.ConflictTypeFile {
		t.Errorf("expected file conflict, got %q", results[0].ConflictType)
	}
	if len(results[0].OverlappingResources) != 1 {
		t.Errorf("expected 1 overlapping resource, got %d", len(results[0].OverlappingResources))
	}
}

func TestCompareImpactAnalyses_NoConflict(t *testing.T) {
	a := &models.ImpactAnalysis{
		FilesImpacted:      `["auth.go"]`,
		APIsImpacted:       `["/api/auth"]`,
		SchemasImpacted:    `["users"]`,
		ComponentsImpacted: `["AuthService"]`,
	}
	b := &models.ImpactAnalysis{
		FilesImpacted:      `["billing.go"]`,
		APIsImpacted:       `["/api/billing"]`,
		SchemasImpacted:    `["invoices"]`,
		ComponentsImpacted: `["BillingService"]`,
	}

	results := compareImpactAnalyses(a, b)
	if len(results) != 0 {
		t.Errorf("expected 0 conflicts, got %d", len(results))
	}
}

func TestCompareImpactAnalyses_SchemaConflictIsCritical(t *testing.T) {
	a := &models.ImpactAnalysis{
		FilesImpacted:      `[]`,
		APIsImpacted:       `[]`,
		SchemasImpacted:    `["tasks.status"]`,
		ComponentsImpacted: `[]`,
	}
	b := &models.ImpactAnalysis{
		FilesImpacted:      `[]`,
		APIsImpacted:       `[]`,
		SchemasImpacted:    `["tasks.status","tasks.priority"]`,
		ComponentsImpacted: `[]`,
	}

	results := compareImpactAnalyses(a, b)
	if len(results) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(results))
	}
	if results[0].Severity != models.SeverityCritical {
		t.Errorf("expected critical severity for schema conflict, got %q", results[0].Severity)
	}
}

func TestCompareImpactAnalyses_MultipleConflictTypes(t *testing.T) {
	a := &models.ImpactAnalysis{
		FilesImpacted:      `["handler.go"]`,
		APIsImpacted:       `["/api/tasks"]`,
		SchemasImpacted:    `["tasks"]`,
		ComponentsImpacted: `["TaskService"]`,
	}
	b := &models.ImpactAnalysis{
		FilesImpacted:      `["handler.go"]`,
		APIsImpacted:       `["/api/tasks"]`,
		SchemasImpacted:    `["tasks"]`,
		ComponentsImpacted: `["TaskService"]`,
	}

	results := compareImpactAnalyses(a, b)
	if len(results) != 4 {
		t.Errorf("expected 4 conflicts (file, api, schema, component), got %d", len(results))
	}
}

func TestFindOverlap(t *testing.T) {
	tests := []struct {
		name     string
		a        []string
		b        []string
		expected int
	}{
		{"no overlap", []string{"a", "b"}, []string{"c", "d"}, 0},
		{"full overlap", []string{"a", "b"}, []string{"a", "b"}, 2},
		{"partial overlap", []string{"a", "b", "c"}, []string{"b", "d"}, 1},
		{"empty a", nil, []string{"a"}, 0},
		{"empty b", []string{"a"}, nil, 0},
		{"case insensitive", []string{"Handler.go"}, []string{"handler.go"}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := findOverlap(tt.a, tt.b)
			if len(result) != tt.expected {
				t.Errorf("expected %d overlaps, got %d", tt.expected, len(result))
			}
		})
	}
}

func TestClassifySeverity(t *testing.T) {
	tests := []struct {
		name     string
		overlap  int
		total    int
		expected models.ConflictSeverity
	}{
		{"no items", 0, 0, models.SeverityLow},
		{"one of many", 1, 20, models.SeverityLow},
		{"two of ten", 2, 10, models.SeverityMedium},
		{"three of six", 3, 6, models.SeverityHigh},
		{"five of eight", 5, 8, models.SeverityCritical},
		{"high ratio", 2, 3, models.SeverityCritical},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifySeverity(tt.overlap, tt.total)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestComputeExecutionOrder_NoConflicts(t *testing.T) {
	tasks := []models.Task{
		{ID: "t1", Title: "Task 1", Priority: 2},
		{ID: "t2", Title: "Task 2", Priority: 3},
	}

	ordering, batches, reasoning := computeExecutionOrder(tasks, nil)
	if len(ordering) != 2 {
		t.Errorf("expected 2 tasks in ordering, got %d", len(ordering))
	}
	if len(batches) != 1 {
		t.Errorf("expected 1 batch (all tasks can run together), got %d", len(batches))
	}
	if len(batches) > 0 && len(batches[0]) != 2 {
		t.Errorf("expected batch of 2, got %d", len(batches[0]))
	}
	if reasoning == "" {
		t.Error("expected reasoning text")
	}
}

func TestComputeExecutionOrder_WithConflicts(t *testing.T) {
	tasks := []models.Task{
		{ID: "t1", Title: "Task 1", Priority: 3},
		{ID: "t2", Title: "Task 2", Priority: 2},
		{ID: "t3", Title: "Task 3", Priority: 1},
	}

	conflicts := []models.ConflictPrediction{
		{TaskAID: "t1", TaskBID: "t2", Severity: models.SeverityHigh},
	}

	ordering, batches, _ := computeExecutionOrder(tasks, conflicts)
	if len(ordering) != 3 {
		t.Errorf("expected 3 tasks, got %d", len(ordering))
	}

	// With t1 and t2 conflicting, they should be in different batches
	if len(batches) < 2 {
		t.Errorf("expected at least 2 batches due to conflict, got %d", len(batches))
	}

	// Verify t1 and t2 are not in the same batch
	for _, batch := range batches {
		hasT1, hasT2 := false, false
		for _, id := range batch {
			if id == "t1" {
				hasT1 = true
			}
			if id == "t2" {
				hasT2 = true
			}
		}
		if hasT1 && hasT2 {
			t.Error("t1 and t2 should not be in the same batch due to conflict")
		}
	}
}

func TestComputeExecutionOrder_EmptyTasks(t *testing.T) {
	ordering, batches, reasoning := computeExecutionOrder(nil, nil)
	if ordering != nil {
		t.Errorf("expected nil ordering, got %v", ordering)
	}
	if batches != nil {
		t.Errorf("expected nil batches, got %v", batches)
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning, got %q", reasoning)
	}
}

func TestSortByPriority(t *testing.T) {
	tasks := []models.Task{
		{ID: "low", Priority: 1},
		{ID: "urgent", Priority: 4},
		{ID: "normal", Priority: 2},
		{ID: "high", Priority: 3},
	}

	sorted := sortByPriority(tasks)
	if sorted[0].ID != "urgent" {
		t.Errorf("expected first task to be 'urgent', got %q", sorted[0].ID)
	}
	if sorted[3].ID != "low" {
		t.Errorf("expected last task to be 'low', got %q", sorted[3].ID)
	}

	// Verify original is unchanged
	if tasks[0].ID != "low" {
		t.Error("sortByPriority should not modify the original slice")
	}
}

func TestBuildImpactAnalysisPrompt(t *testing.T) {
	prompt := buildImpactAnalysisPrompt("Fix auth bug", "Users can't log in after password change", "MyProject", "/tmp/myproject")

	if prompt == "" {
		t.Error("expected non-empty prompt")
	}

	// Should contain task details
	if !containsStr(prompt, "Fix auth bug") {
		t.Error("prompt should contain task title")
	}
	if !containsStr(prompt, "Users can't log in") {
		t.Error("prompt should contain task prompt")
	}
	if !containsStr(prompt, "MyProject") {
		t.Error("prompt should contain project name")
	}
	if !containsStr(prompt, "JSON") {
		t.Error("prompt should mention JSON format")
	}
}

func containsStr(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && len(s) >= len(substr) && (s == substr || findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
