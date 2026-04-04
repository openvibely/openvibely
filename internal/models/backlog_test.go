package models

import (
	"testing"
)

func TestBacklogSuggestion_ParseSubtasks(t *testing.T) {
	t.Run("empty subtasks", func(t *testing.T) {
		s := &BacklogSuggestion{SuggestedSubtasks: "[]"}
		subtasks, err := s.ParseSubtasks()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if subtasks != nil {
			t.Errorf("expected nil, got %v", subtasks)
		}
	})

	t.Run("valid subtasks", func(t *testing.T) {
		s := &BacklogSuggestion{}
		s.SetSubtasks([]string{"Fix login", "Add tests", "Update docs"})
		subtasks, err := s.ParseSubtasks()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(subtasks) != 3 {
			t.Errorf("expected 3 subtasks, got %d", len(subtasks))
		}
		if subtasks[0] != "Fix login" {
			t.Errorf("expected 'Fix login', got %q", subtasks[0])
		}
	})

	t.Run("empty string", func(t *testing.T) {
		s := &BacklogSuggestion{SuggestedSubtasks: ""}
		subtasks, err := s.ParseSubtasks()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if subtasks != nil {
			t.Errorf("expected nil, got %v", subtasks)
		}
	})
}

func TestBacklogHealthSnapshot_ParseBottleneckTags(t *testing.T) {
	t.Run("empty tags", func(t *testing.T) {
		h := &BacklogHealthSnapshot{BottleneckTags: "[]"}
		tags, err := h.ParseBottleneckTags()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tags != nil {
			t.Errorf("expected nil, got %v", tags)
		}
	})

	t.Run("valid tags", func(t *testing.T) {
		h := &BacklogHealthSnapshot{BottleneckTags: `["feature","bug"]`}
		tags, err := h.ParseBottleneckTags()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(tags) != 2 {
			t.Errorf("expected 2 tags, got %d", len(tags))
		}
	})
}

func TestBacklogAnalysisReport_SuggestionIDs(t *testing.T) {
	r := &BacklogAnalysisReport{}

	// Set IDs
	ids := []string{"abc123", "def456", "ghi789"}
	if err := r.SetSuggestionIDs(ids); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Parse back
	parsed, err := r.ParseSuggestionIDs()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed) != 3 {
		t.Errorf("expected 3 IDs, got %d", len(parsed))
	}
	if parsed[0] != "abc123" {
		t.Errorf("expected 'abc123', got %q", parsed[0])
	}
}
