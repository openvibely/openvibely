package attachmentcontext

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type message struct{ role, text string }

func read(m message) (string, string) { return m.role, m.text }
func makeMessage(s string) message    { return message{"user", s} }

func countContexts(items []message) int {
	n := 0
	for _, m := range items {
		if isContext(m.role, m.text) {
			n++
		}
	}
	return n
}

func TestRestoreAndUpdate(t *testing.T) {
	ctx := With(context.Background(), "/uploads/report.docx")
	history := []message{{"assistant", "Compacted summary"}, {"user", "Edit it"}}
	got := Ensure(ctx, history, read, makeMessage)
	if len(got) != 3 || got[1].text != Text(ctx) || got[2].text != "Edit it" {
		t.Fatalf("%+v", got)
	}
	if len(Ensure(ctx, got, read, makeMessage)) != 3 {
		t.Fatal("duplicated context")
	}
	// Repeated compaction drops the context; it is restored each time.
	for i := 0; i < 3; i++ {
		got = Ensure(ctx, []message{{"assistant", "New summary"}}, read, makeMessage)
		if len(got) != 2 || got[1].text != Text(ctx) {
			t.Fatalf("%+v", got)
		}
	}
	next := With(ctx, "/uploads/report.docx\n/uploads/new.docx")
	updated := Ensure(next, got, read, makeMessage)
	if len(updated) != 3 || updated[1].text != Text(ctx) || updated[2].text != Text(next) {
		t.Fatalf("changed context must be appended after the old one: %+v", updated)
	}
	if len(Ensure(next, updated, read, makeMessage)) != 3 {
		t.Fatal("repeated updated references")
	}
	if len(Ensure(context.Background(), history, read, makeMessage)) != 2 {
		t.Fatal("empty context changed history")
	}
}

func TestEnsureAppendsChangesWithoutRewritingHistory(t *testing.T) {
	old := Wrap("/uploads/old.pdf")
	current := With(context.Background(), "/uploads/new.pdf")
	items := []message{{"user", old}, {"assistant", "a"}, {"user", "go"}}
	got := Ensure(current, items, read, makeMessage)
	want := []message{{"user", old}, {"assistant", "a"}, {"user", Text(current)}, {"user", "go"}}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("history prefix rewritten: %+v", got)
		}
	}

	empty := With(context.Background(), "")
	got = Ensure(empty, items, read, makeMessage)
	if len(got) != len(items) {
		t.Fatalf("empty file list must add nothing: %+v", got)
	}
	for i := range items {
		if got[i] != items[i] {
			t.Fatalf("empty file list rewrote history: %+v", got)
		}
	}
	if got := Ensure(empty, []message{{"user", "go"}}, read, makeMessage); len(got) != 1 {
		t.Fatalf("no files ever attached must add nothing: %+v", got)
	}
}

func TestEnsureKeepsToolPairsIntact(t *testing.T) {
	ctx := With(context.Background(), "/uploads/a.pdf")
	items := []message{{"user", "q"}, {"assistant", "tool_call"}, {"tool", "tool_result"}}
	got := Ensure(ctx, items, read, makeMessage)
	if len(got) != 4 || got[1].text != "tool_call" || got[2].text != "tool_result" || got[3].text != Text(ctx) {
		t.Fatalf("%+v", got)
	}
}

func TestSourceReloadsOnlyAfterInvalidate(t *testing.T) {
	loads := 0
	refs := "/uploads/a.pdf"
	source := NewSource(func(context.Context) (string, error) {
		loads++
		return refs, nil
	}, "task-source-test")
	ctx := WithSource(context.Background(), source)
	if err := source.Load(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if !strings.Contains(Text(ctx), "a.pdf") {
			t.Fatal("missing reference")
		}
	}
	if loads != 1 {
		t.Fatalf("loads = %d, want 1", loads)
	}
	refs = "/uploads/a.pdf\n/uploads/b.pdf"
	Invalidate("task-source-test")
	if !strings.Contains(Text(ctx), "b.pdf") || loads != 2 {
		t.Fatalf("not refreshed after invalidate: loads=%d", loads)
	}
	Invalidate("other-key")
	_ = Text(ctx)
	if loads != 2 {
		t.Fatal("unrelated invalidation reloaded")
	}
}

func TestSourceKeepsLastGoodOnError(t *testing.T) {
	fail := false
	source := NewSource(func(context.Context) (string, error) {
		if fail {
			return "", errors.New("boom")
		}
		return "/uploads/a.pdf", nil
	}, "task-source-error")
	ctx := WithSource(context.Background(), source)
	_ = source.Load(ctx)
	fail = true
	Invalidate("task-source-error")
	if err := source.Load(ctx); err == nil {
		t.Fatal("expected load error")
	}
	if !strings.Contains(Text(ctx), "a.pdf") {
		t.Fatal("lost last good references")
	}
}

func TestSeedReplayPreservesContextUpdatesAndPosition(t *testing.T) {
	ctx := With(context.Background(), "/files/report.pdf")
	for _, tail := range []message{{"user", "Read it"}, {"user", ""}, {"tool", "result"}} {
		visible := []message{{"assistant", "earlier"}, tail}
		replay := []message{tail}
		seeded := SeedReplay(ctx, visible, replay, read, makeMessage)
		wire := Ensure(ctx, visible, read, makeMessage)
		if !reflect.DeepEqual(wire[1:], seeded) {
			t.Fatalf("replay differs from wire: wire=%+v replay=%+v", wire, seeded)
		}
		if got := SeedReplay(ctx, wire, replay, read, makeMessage); !reflect.DeepEqual(got, replay) {
			t.Fatalf("unchanged list copied again: %+v", got)
		}
		changed := With(ctx, "/files/report.pdf\n/files/new.pdf")
		followup := append(append([]message(nil), wire...), message{"assistant", "done"}, message{"user", "next"})
		got := SeedReplay(changed, followup, []message{{"user", "next"}}, read, makeMessage)
		want := []message{makeMessage(Text(changed)), {"user", "next"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("missing changed list: %+v", got)
		}
		for _, empty := range []context.Context{context.Background(), With(ctx, "")} {
			if got := SeedReplay(empty, followup, replay, read, makeMessage); !reflect.DeepEqual(got, replay) {
				t.Fatalf("empty source changed replay: %+v", got)
			}
		}
	}
}
