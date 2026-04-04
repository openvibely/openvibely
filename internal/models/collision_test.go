package models

import (
	"testing"
)

func TestImpactAnalysis_ParseAndSetFiles(t *testing.T) {
	ia := &ImpactAnalysis{}

	// Set files
	files := []string{"internal/handler/handler.go", "internal/service/llm_service.go"}
	if err := ia.SetFiles(files); err != nil {
		t.Fatalf("SetFiles failed: %v", err)
	}

	// Parse files
	parsed, err := ia.ParseFiles()
	if err != nil {
		t.Fatalf("ParseFiles failed: %v", err)
	}
	if len(parsed) != 2 {
		t.Errorf("expected 2 files, got %d", len(parsed))
	}
	if parsed[0] != "internal/handler/handler.go" {
		t.Errorf("expected first file 'internal/handler/handler.go', got %q", parsed[0])
	}
}

func TestImpactAnalysis_ParseAndSetAPIs(t *testing.T) {
	ia := &ImpactAnalysis{}

	apis := []string{"/api/tasks", "/api/projects"}
	if err := ia.SetAPIs(apis); err != nil {
		t.Fatalf("SetAPIs failed: %v", err)
	}

	parsed, err := ia.ParseAPIs()
	if err != nil {
		t.Fatalf("ParseAPIs failed: %v", err)
	}
	if len(parsed) != 2 {
		t.Errorf("expected 2 APIs, got %d", len(parsed))
	}
}

func TestImpactAnalysis_ParseAndSetSchemas(t *testing.T) {
	ia := &ImpactAnalysis{}

	schemas := []string{"tasks.status", "executions"}
	if err := ia.SetSchemas(schemas); err != nil {
		t.Fatalf("SetSchemas failed: %v", err)
	}

	parsed, err := ia.ParseSchemas()
	if err != nil {
		t.Fatalf("ParseSchemas failed: %v", err)
	}
	if len(parsed) != 2 {
		t.Errorf("expected 2 schemas, got %d", len(parsed))
	}
}

func TestImpactAnalysis_ParseAndSetComponents(t *testing.T) {
	ia := &ImpactAnalysis{}

	components := []string{"WorkerService", "LLMService"}
	if err := ia.SetComponents(components); err != nil {
		t.Fatalf("SetComponents failed: %v", err)
	}

	parsed, err := ia.ParseComponents()
	if err != nil {
		t.Fatalf("ParseComponents failed: %v", err)
	}
	if len(parsed) != 2 {
		t.Errorf("expected 2 components, got %d", len(parsed))
	}
}

func TestImpactAnalysis_EmptyArrays(t *testing.T) {
	ia := &ImpactAnalysis{
		FilesImpacted:      "[]",
		APIsImpacted:       "",
		SchemasImpacted:    "[]",
		ComponentsImpacted: "",
	}

	files, err := ia.ParseFiles()
	if err != nil {
		t.Fatalf("ParseFiles failed: %v", err)
	}
	if files != nil {
		t.Errorf("expected nil for empty array, got %v", files)
	}

	apis, err := ia.ParseAPIs()
	if err != nil {
		t.Fatalf("ParseAPIs failed: %v", err)
	}
	if apis != nil {
		t.Errorf("expected nil for empty string, got %v", apis)
	}
}

func TestConflictPrediction_ParseAndSetOverlappingResources(t *testing.T) {
	cp := &ConflictPrediction{}

	resources := []string{"handler.go", "service.go"}
	if err := cp.SetOverlappingResources(resources); err != nil {
		t.Fatalf("SetOverlappingResources failed: %v", err)
	}

	parsed, err := cp.ParseOverlappingResources()
	if err != nil {
		t.Fatalf("ParseOverlappingResources failed: %v", err)
	}
	if len(parsed) != 2 {
		t.Errorf("expected 2 resources, got %d", len(parsed))
	}
}

func TestExecutionOrderRecommendation_ParseAndSetTaskIDs(t *testing.T) {
	rec := &ExecutionOrderRecommendation{}

	ids := []string{"task1", "task2", "task3"}
	if err := rec.SetTaskIDs(ids); err != nil {
		t.Fatalf("SetTaskIDs failed: %v", err)
	}

	parsed, err := rec.ParseTaskIDs()
	if err != nil {
		t.Fatalf("ParseTaskIDs failed: %v", err)
	}
	if len(parsed) != 3 {
		t.Errorf("expected 3 task IDs, got %d", len(parsed))
	}
}

func TestExecutionOrderRecommendation_ParseAndSetBatchGroups(t *testing.T) {
	rec := &ExecutionOrderRecommendation{}

	groups := [][]string{{"task1", "task2"}, {"task3"}}
	if err := rec.SetBatchGroups(groups); err != nil {
		t.Fatalf("SetBatchGroups failed: %v", err)
	}

	parsed, err := rec.ParseBatchGroups()
	if err != nil {
		t.Fatalf("ParseBatchGroups failed: %v", err)
	}
	if len(parsed) != 2 {
		t.Errorf("expected 2 batch groups, got %d", len(parsed))
	}
	if len(parsed[0]) != 2 {
		t.Errorf("expected first group to have 2 tasks, got %d", len(parsed[0]))
	}
}

func TestConflictHistory_ParseActualFiles(t *testing.T) {
	ch := &ConflictHistory{
		ActualFiles: `["file1.go","file2.go"]`,
	}

	files, err := ch.ParseActualFiles()
	if err != nil {
		t.Fatalf("ParseActualFiles failed: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d", len(files))
	}
}
