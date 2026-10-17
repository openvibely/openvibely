package attachmentcontext

import (
	"context"
	"testing"
)

type message struct{ role, text string }

func TestRestoreAndUpdate(t *testing.T) {
	read := func(m message) (string, string) { return m.role, m.text }
	makeMessage := func(s string) message { return message{"user", s} }
	ctx := With(context.Background(), "/uploads/report.docx")
	history := []message{{"assistant", "Compacted summary"}, {"user", "Edit it"}}
	got := Ensure(ctx, history, read, makeMessage)
	if len(got) != 3 || got[1].text != Text(ctx) || got[2].text != "Edit it" {
		t.Fatalf("%+v", got)
	}
	if len(Ensure(ctx, got, read, makeMessage)) != 3 {
		t.Fatal("duplicated context")
	}
	// A later compaction drops the context; restore it again.
	got = Ensure(ctx, []message{{"assistant", "New summary"}}, read, makeMessage)
	if len(got) != 2 || got[1].text != Text(ctx) {
		t.Fatalf("%+v", got)
	}
	next := With(ctx, "/uploads/new.docx")
	updated := Ensure(next, got, read, makeMessage)
	if len(updated) != 3 {
		t.Fatal("missing updated references")
	}
	if len(Ensure(next, updated, read, makeMessage)) != 3 {
		t.Fatal("repeated updated references")
	}
	if len(Ensure(context.Background(), history, read, makeMessage)) != 2 {
		t.Fatal("empty context changed history")
	}
}
