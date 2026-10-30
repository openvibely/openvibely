package sqlite_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/transcript"
)

var marshals atomic.Int64

type countedItem struct{ Text string }

func (c countedItem) MarshalJSON() ([]byte, error) {
	marshals.Add(1)
	return []byte(`{"text":"` + c.Text + `"}`), nil
}

func recorderStore(t *testing.T) (*sqlite.ExecutionRepo, models.ConversationOwner) {
	t.Helper()
	db := testutil.NewTestDB(t)
	task := &models.Task{ProjectID: "default", Title: "recorder", Prompt: "test", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := sqlite.NewTaskRepo(db, nil).Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO executions(id,task_id,status,prompt_sent) VALUES('recorder',?,'running','test')`, task.ID); err != nil {
		t.Fatal(err)
	}
	return sqlite.NewExecutionRepo(db), models.ConversationOwner{ExecutionID: "recorder"}
}

func TestRecorderSkipsUnchangedEncodingAndPreservesMutableResults(t *testing.T) {
	repo, owner := recorderStore(t)
	ctx := context.Background()
	r := transcript.NewRecorder(repo, owner)
	header := transcript.ReplayHeader{Format: "envelope", ItemsKey: "responses_input"}
	tool := map[string]any{"results": []any{"one"}}
	items := []any{countedItem{Text: "large unchanged item"}, tool}
	marshals.Store(0)
	if err := r.Save(ctx, header, items); err != nil {
		t.Fatal(err)
	}
	committed := r.Recovery()
	before, _ := repo.ConversationRevision(ctx, owner, models.ConversationMetadata, transcript.ReplayGuardID, 0)
	if err := r.Save(ctx, header, items); err != nil {
		t.Fatal(err)
	}
	after, _ := repo.ConversationRevision(ctx, owner, models.ConversationMetadata, transcript.ReplayGuardID, 0)
	if marshals.Load() != 1 || before != after {
		t.Fatal("unchanged checkpoint encoded or rewrote history")
	}
	tool["results"] = append(tool["results"].([]any), "two")
	items = append(items, map[string]any{"role": "assistant", "content": "done"})
	if err := r.Save(ctx, header, items); err != nil {
		t.Fatal(err)
	}
	if marshals.Load() != 1 {
		t.Fatal("small addition encoded old item")
	}
	if len(committed[1].(map[string]any)["results"].([]any)) != 1 {
		t.Fatal("live mutation changed committed recovery")
	}
	changes, err := repo.ReadConversationChanges(ctx, owner, models.ConversationProvider, before, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected edited tool and new answer, got %d", len(changes))
	}
	saved, err := repo.ReplayMessagesByExecutionIDs(ctx, []string{owner.ExecutionID})
	if err != nil {
		t.Fatal(err)
	}
	_, restored, err := transcript.SplitReplay(saved[owner.ExecutionID][0])
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 3 {
		t.Fatalf("restore length %d", len(restored))
	}
}

type failingStore struct {
	persistence.ConversationStore
	fail              bool
	commitBeforeError bool
}

func (s *failingStore) AppendConversationChanges(ctx context.Context, o models.ConversationOwner, c []models.ConversationChange) ([]models.ConversationChange, error) {
	if s.fail {
		if s.commitBeforeError {
			if _, err := s.ConversationStore.AppendConversationChanges(ctx, o, c); err != nil {
				return nil, err
			}
		}
		return nil, errors.New("injected checkpoint failure")
	}
	return s.ConversationStore.AppendConversationChanges(ctx, o, c)
}

func TestRecorderFailedSaveDoesNotAdvanceRecovery(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "uncertain-commit"}[uncertain], func(t *testing.T) {
			repo, owner := recorderStore(t)
			store := &failingStore{ConversationStore: repo}
			ctx := context.Background()
			r := transcript.NewRecorder(store, owner)
			header := transcript.ReplayHeader{Format: "array"}
			if err := r.Save(ctx, header, []any{"before"}); err != nil {
				t.Fatal(err)
			}
			store.fail = true
			store.commitBeforeError = uncertain
			if err := r.Save(ctx, header, []any{"before", "after"}); err == nil {
				t.Fatal("expected failure")
			}
			if len(r.Recovery()) != 1 {
				t.Fatal("failed save advanced recovery")
			}
			store.fail = false
			if err := r.Save(ctx, header, []any{"before", "after"}); err != nil {
				t.Fatal(err)
			}
			if len(r.Recovery()) != 2 {
				t.Fatal("retry failed")
			}
			records, err := repo.ReadConversationChanges(ctx, owner, models.ConversationProvider, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 2 {
				t.Fatalf("retry duplicated items: %d", len(records))
			}
		})
	}
}

func TestRecorderCompactionShrinkRegrowAndRestart(t *testing.T) {
	repo, owner := recorderStore(t)
	ctx := context.Background()
	r := transcript.NewRecorder(repo, owner)
	header := transcript.ReplayHeader{Format: "array"}
	for _, items := range [][]any{{"one", "two", "three"}, {"summary"}, {"summary", "new"}} {
		if err := r.Save(ctx, header, items); err != nil {
			t.Fatal(err)
		}
	}
	r = transcript.NewRecorder(repo, owner)
	if err := r.Save(ctx, header, []any{"summary", "new", "more"}); err != nil {
		t.Fatal(err)
	}
	saved, err := repo.ReplayMessagesByExecutionIDs(ctx, []string{owner.ExecutionID})
	if err != nil {
		t.Fatal(err)
	}
	if got := saved[owner.ExecutionID][0].TranscriptJSON; got != `["summary","new","more"]` {
		t.Fatalf("bad restored context: %s", got)
	}
}

// A suffix boundary is an optimization only: failed saves, other writers, and
// replacement histories must still recover exactly the caller's latest state.
func TestRecorderChangedSuffixRecovery(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "uncertain"}[uncertain], func(t *testing.T) {
			repo, owner := recorderStore(t)
			store := &failingStore{ConversationStore: repo}
			r := transcript.NewRecorder(store, owner)
			ctx := context.Background()
			header := transcript.ReplayHeader{Format: "array"}
			save := func(items []any, first int) {
				t.Helper()
				if err := r.SaveChanged(ctx, header, items, first); err != nil {
					t.Fatal(err)
				}
			}
			save([]any{"one", "two"}, 0)
			old := r.Recovery()
			save([]any{"one", "edited", "three"}, 1)
			if old[1] != "two" || len(old) != 2 {
				t.Fatal("changed captured recovery")
			}
			store.fail, store.commitBeforeError = true, uncertain
			if err := r.SaveChanged(ctx, header, []any{"one", "edited", "three", "four"}, 3); err == nil {
				t.Fatal("expected failure")
			}
			if len(r.Recovery()) != 3 {
				t.Fatal("failed save advanced recovery")
			}
			store.fail = false
			save([]any{"one", "edited", "three", "four"}, 3)
			other := transcript.NewRecorder(repo, owner)
			if err := other.Save(ctx, header, []any{"external", "edited", "three", "four"}); err != nil {
				t.Fatal(err)
			}
			save([]any{"one", "edited", "three", "four", "five"}, 4)
			save([]any{"one", "edited"}, 2)
			saved, err := repo.ReplayMessagesByExecutionIDs(ctx, []string{owner.ExecutionID})
			if err != nil {
				t.Fatal(err)
			}
			if got := saved[owner.ExecutionID][0].TranscriptJSON; got != `["one","edited"]` {
				t.Fatalf("unexpected recovery %s", got)
			}
			if err := r.SaveChanged(ctx, header, nil, 1); err == nil {
				t.Fatal("invalid boundary accepted")
			}
		})
	}
}
