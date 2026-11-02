package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func conversationTestRepo(t *testing.T) (*sql.DB, *ExecutionRepo, models.ConversationOwner) {
	t.Helper()
	db := testutil.NewTestDB(t)
	task := &models.Task{ProjectID: "default", Title: "conversation", Prompt: "test", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := NewTaskRepo(db, nil).Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO executions(id,task_id,status,prompt_sent) VALUES('conversation-test',?,'running','test')", task.ID); err != nil {
		t.Fatal(err)
	}
	return db, NewExecutionRepo(db), models.ConversationOwner{ExecutionID: "conversation-test"}
}

func conversationChange(id, item, payload string) models.ConversationChange {
	return models.ConversationChange{MutationID: id, ItemID: item, Lane: models.ConversationDisplay, Operation: models.ConversationPut, Payload: json.RawMessage(payload)}
}

func TestConversationChangesCursorAndRevision(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	first := conversationChange("first", "tool", `{"results":["one"]}`)
	written, err := repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{first})
	if err != nil {
		t.Fatal(err)
	}
	revision := written[0].Sequence
	next := conversationChange("second", "answer", `{"text":"waiting"}`)
	next.Position = 1
	written, err = repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{next})
	if err != nil {
		t.Fatal(err)
	}
	cursor := written[0].Sequence
	edit := conversationChange("third", "tool", `{"results":["one","two"]}`)
	edit.PreviousRevision = revision
	written, err = repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{edit})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.ReadConversationChanges(ctx, owner, models.ConversationDisplay, cursor, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ItemID != "tool" || got[0].Sequence <= cursor {
		t.Fatalf("old item edit lost on reconnect: %+v", got)
	}
	retry, err := repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{edit})
	if err != nil {
		t.Fatal(err)
	}
	if retry[0].Sequence != written[0].Sequence {
		t.Fatal("retry created a duplicate event")
	}
	edit.MutationID = "stale"
	if _, err = repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{edit}); !errors.Is(err, ErrConversationConflict) {
		t.Fatalf("stale update: %v", err)
	}
}

func TestConversationChangesBatchRollbackAndLaneIsolation(t *testing.T) {
	db, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	good := conversationChange("good", "visible", `{"text":"hello 世界"}`)
	bad := conversationChange("bad", "absent", `{"text":"oops"}`)
	bad.Operation = models.ConversationAppend
	if _, err := repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{good, bad}); err == nil {
		t.Fatal("invalid batch accepted")
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM conversation_items").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("batch partially committed")
	}
	private := conversationChange("private", "secret", `{"encrypted_content":"private"}`)
	private.Lane = models.ConversationProvider
	if _, err := repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{good, private}); err != nil {
		t.Fatal(err)
	}
	visible, err := repo.ReadConversationChanges(ctx, owner, models.ConversationDisplay, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 1 || strings.Contains(string(visible[0].Payload), "private") {
		t.Fatalf("provider state in public stream: %+v", visible)
	}
	if _, err := repo.AppendConversationChanges(ctx, models.ConversationOwner{ExecutionID: "missing"}, []models.ConversationChange{good}); err == nil {
		t.Fatal("orphan owner accepted")
	}
	if _, err := db.Exec("DELETE FROM executions WHERE id=?", owner.ExecutionID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM conversation_items").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("owner deletion leaked rows")
	}
}

func TestConversationCheckpointTransaction(t *testing.T) {
	db, repo, _ := conversationTestRepo(t)
	createCheckpointTask(t, db, "test-scope")
	ctx := context.Background()
	if err := repo.UpsertChatCompactionCheckpoint(ctx, models.ChatCompactionCheckpoint{ScopeType: "task", ScopeID: "test-scope"}); err != nil {
		t.Fatal(err)
	}
	owner := models.ConversationOwner{ScopeType: "task", ScopeID: "test-scope"}
	sentinel := errors.New("injected interrupted checkpoint")
	err := withImmediateTx(ctx, db, func(tx sqlExecutor) error {
		if _, err := tx.ExecContext(ctx, "UPDATE chat_compaction_checkpoints SET source_execution_id='new-source' WHERE scope_id='test-scope'"); err != nil {
			return err
		}
		item := conversationChange("replacement", "summary", `{"type":"compaction","encrypted_content":"opaque"}`)
		item.Generation = 1
		item.Lane = models.ConversationProvider
		if _, err := appendConversationChanges(ctx, tx, owner, []models.ConversationChange{item}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	got, err := repo.GetChatCompactionCheckpoint(ctx, "task", "test-scope")
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceExecutionID != "" {
		t.Fatal("boundary survived rollback")
	}
	rows, err := repo.ReadConversationChanges(ctx, owner, models.ConversationProvider, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatal("replacement survived rollback")
	}
}

func TestConversationCatchupUsesSequenceIndex(t *testing.T) {
	db, _, owner := conversationTestRepo(t)
	rows, err := db.Query("EXPLAIN QUERY PLAN SELECT sequence,payload FROM conversation_items WHERE execution_id=? AND lane='display' AND sequence>? ORDER BY sequence LIMIT 100", owner.ExecutionID, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var a, b, c int
		var detail string
		if err := rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "conversation_execution_changes") || !strings.Contains(plan, "sequence>?") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("unexpected catchup plan: %s", plan)
	}
}

func TestConversationPagePartialAndFinalizedItems(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	c := conversationChange("create", "message", `{"type":"markdown","text":"Hello "}`)
	changes, err := repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{c})
	if err != nil {
		t.Fatal(err)
	}
	c.PreviousRevision = changes[0].Sequence
	c.MutationID = "append"
	c.Operation = models.ConversationAppend
	c.Payload = json.RawMessage(`{"text":"世界"}`)
	changes, err = repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{c})
	if err != nil {
		t.Fatal(err)
	}
	p, err := repo.ReadConversationPage(ctx, owner, models.ConversationDisplay, 0, -1, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || string(p.Items[0].Payload) != `{"text":"Hello 世界","type":"markdown"}` || p.Cursor != changes[0].Sequence {
		t.Fatalf("bad reconstructed page: %+v", p)
	}
	c.PreviousRevision = changes[0].Sequence
	c.MutationID = "finish"
	c.Operation = models.ConversationPut
	c.Payload = json.RawMessage(`{"type":"markdown","text":"Corrected"}`)
	changes, err = repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{c})
	if err != nil {
		t.Fatal(err)
	}
	p, err = repo.ReadConversationPage(ctx, owner, models.ConversationDisplay, 0, -1, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || string(p.Items[0].Payload) != string(c.Payload) {
		t.Fatalf("finalized page replayed obsolete fragments: %+v", p)
	}
	c.PreviousRevision = changes[0].Sequence
	c.MutationID = "remove"
	c.Operation = models.ConversationRemove
	c.Payload = json.RawMessage(`{}`)
	if _, err = repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{c}); err != nil {
		t.Fatal(err)
	}
	p, err = repo.ReadConversationPage(ctx, owner, models.ConversationDisplay, 0, -1, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 0 {
		t.Fatal("removed item in page")
	}
}

func TestConversationPageGenerationAndKeyset(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	a := conversationChange("a", "a", `{"text":"first"}`)
	b := conversationChange("b", "b", `{"text":"second"}`)
	replacement := conversationChange("new", "a", `{"text":"replacement"}`)
	replacement.Generation = 1
	if _, err := repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{a, b, replacement}); err != nil {
		t.Fatal(err)
	}
	p, err := repo.ReadConversationPage(ctx, owner, models.ConversationDisplay, 0, -1, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].ID != "a" {
		t.Fatalf("bad first page: %+v", p)
	}
	p, err = repo.ReadConversationPage(ctx, owner, models.ConversationDisplay, 0, p.Items[0].Position, p.Items[0].ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].ID != "b" {
		t.Fatalf("bad next page: %+v", p)
	}
	p, err = repo.ReadConversationPage(ctx, owner, models.ConversationDisplay, 1, -1, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || string(p.Items[0].Payload) != string(replacement.Payload) {
		t.Fatalf("generation mixed: %+v", p)
	}
}

func TestConversationAppendRejectsNonText(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	c := conversationChange("first", "tool", `{"type":"tool","results":[]}`)
	result, err := repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{c})
	if err != nil {
		t.Fatal(err)
	}
	c.MutationID = "bad-append"
	c.PreviousRevision = result[0].Sequence
	c.Operation = models.ConversationAppend
	c.Payload = json.RawMessage(`{"text":"bad"}`)
	if _, err := repo.AppendConversationChanges(ctx, owner, []models.ConversationChange{c}); err == nil {
		t.Fatal("accepted append to non-text item")
	}
	p, err := repo.ReadConversationPage(ctx, owner, models.ConversationDisplay, 0, -1, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].Revision != result[0].Sequence {
		t.Fatal("failed append changed head")
	}
}

func createCheckpointTask(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES(?,'default','checkpoint owner')`, id); err != nil {
		t.Fatal(err)
	}
}
