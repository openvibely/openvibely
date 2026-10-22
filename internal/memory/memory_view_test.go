package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectedMemoryRuntimeToolsLoadsOnlySelectedMemory(t *testing.T) {
	repo := t.TempDir()
	memoryDir := filepath.Join(repo, ".openvibely", MemoryDirName)
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "selected.md"), []byte("selected memory body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "unselected.md"), []byte("unselected memory body"), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := SelectedMemoryRuntimeTools(repo, []SelectedMemory{{File: "selected.md", Topic: "Route Topic", Summary: "Selected summary.", Snippet: "Route snippet."}})
	if rt == nil || !rt.HasDefinition("memory_view") {
		t.Fatalf("expected memory_view runtime tool, got %#v", rt)
	}
	out, handled, isErr, err := rt.Executor(context.Background(), "memory_view", json.RawMessage(`{"handle":"selected.md"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "selected memory body") || !strings.Contains(out, `"handle": "selected.md"`) {
		t.Fatalf("selected memory_view failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	for _, unwanted := range []string{"Route Topic", "Selected summary.", "Route snippet.", `"topic"`, `"summary"`, `"snippet"`} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("memory_view leaked route metadata %q in output:\n%s", unwanted, out)
		}
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "memory_view", json.RawMessage(`{"handle":"unselected.md"}`))
	if !handled || err != nil || !isErr || !strings.Contains(out, "not in this turn's authorized memory index") {
		t.Fatalf("unselected memory_view should be rejected handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	for _, badHandle := range []string{"../selected.md", "nested/.."} {
		params, _ := json.Marshal(map[string]string{"handle": badHandle})
		out, handled, isErr, err = rt.Executor(context.Background(), "memory_view", params)
		if !handled || err != nil || !isErr {
			t.Fatalf("traversal memory_view %q should be rejected handled=%v isErr=%v err=%v out=%q", badHandle, handled, isErr, err, out)
		}
	}
}

func TestIndexedMemoryHandlesExtractsOnlySafeIndexedMarkdownFiles(t *testing.T) {
	indexed := IndexedMemoryHandles("# Project Memory\n\n- managed_memory.md: useful and mentions hidden.md in the description.\n- [provider](provider_architecture.md): useful.\n- [Realtime and Frontend Patterns](realtime_and_frontend_patterns.md) - SSE/diff streaming and frontend UI patterns.\n1. numbered_memory.md: useful.\n- ../secret.md: bad.\n- notes.txt: not a memory file.\n")
	for _, want := range []string{"managed_memory.md", "provider_architecture.md", "realtime_and_frontend_patterns.md", "numbered_memory.md"} {
		if _, ok := indexed[want]; !ok {
			t.Fatalf("expected indexed handle %q in %#v", want, indexed)
		}
	}
	for _, unwanted := range []string{"../secret.md", "secret.md", "notes.txt", "hidden.md"} {
		if _, ok := indexed[unwanted]; ok {
			t.Fatalf("unexpected indexed handle %q in %#v", unwanted, indexed)
		}
	}
}
