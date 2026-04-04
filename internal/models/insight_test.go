package models

import "testing"

func TestInsight_ParseSetEvidence(t *testing.T) {
	i := &Insight{}

	data := map[string]interface{}{
		"fail_count": float64(5),
		"task_title": "Deploy task",
	}
	if err := i.SetEvidence(data); err != nil {
		t.Fatalf("SetEvidence: %v", err)
	}
	if i.Evidence == "" {
		t.Fatal("expected evidence to be set")
	}

	parsed, err := i.ParseEvidence()
	if err != nil {
		t.Fatalf("ParseEvidence: %v", err)
	}
	if parsed["task_title"] != "Deploy task" {
		t.Errorf("task_title: got %v", parsed["task_title"])
	}
}

func TestInsight_ParseEvidenceEmpty(t *testing.T) {
	i := &Insight{Evidence: ""}
	result, err := i.ParseEvidence()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}

	i.Evidence = "{}"
	result, err = i.ParseEvidence()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for {}, got %v", result)
	}
}

func TestInsightReport_ParseSetInsightIDs(t *testing.T) {
	r := &InsightReport{}

	ids := []string{"abc", "def", "ghi"}
	if err := r.SetInsightIDs(ids); err != nil {
		t.Fatalf("SetInsightIDs: %v", err)
	}

	parsed, err := r.ParseInsightIDs()
	if err != nil {
		t.Fatalf("ParseInsightIDs: %v", err)
	}
	if len(parsed) != 3 {
		t.Errorf("count: got %d, want 3", len(parsed))
	}
	if parsed[0] != "abc" {
		t.Errorf("first ID: got %q", parsed[0])
	}
}

func TestKnowledgeEntry_ParseSetTags(t *testing.T) {
	k := &KnowledgeEntry{}

	tags := []string{"architecture", "database"}
	if err := k.SetTags(tags); err != nil {
		t.Fatalf("SetTags: %v", err)
	}

	parsed, err := k.ParseTags()
	if err != nil {
		t.Fatalf("ParseTags: %v", err)
	}
	if len(parsed) != 2 {
		t.Errorf("count: got %d, want 2", len(parsed))
	}
}

func TestKnowledgeEntry_ParseTagsEmpty(t *testing.T) {
	k := &KnowledgeEntry{Tags: ""}
	tags, err := k.ParseTags()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tags != nil {
		t.Errorf("expected nil, got %v", tags)
	}

	k.Tags = "[]"
	tags, err = k.ParseTags()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tags != nil {
		t.Errorf("expected nil for [], got %v", tags)
	}
}
