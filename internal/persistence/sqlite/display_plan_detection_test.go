package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence/displayindex"
)

type planReadCounter struct {
	*sql.DB
	fullPlanReads int
}

func (a *planReadCounter) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if strings.HasPrefix(q, "SELECT * FROM (SELECT head.position,"+displayindex.ItemTextSQL) {
		a.fullPlanReads++
	}
	return a.DB.QueryContext(ctx, q, args...)
}
func TestDisplayOrdinaryMarkupDoesNotReadFullPlanHistory(t *testing.T) {
	db, repo, owner := conversationTestRepo(t)
	var cs []models.ConversationChange
	for i := 0; i < 500; i++ {
		c := conversationChange(fmt.Sprint(i), fmt.Sprint(i), `{"type":"text","text":"ordinary old text"}`)
		c.Position = int64(i)
		cs = append(cs, c)
	}
	if _, err := repo.AppendConversationChanges(t.Context(), owner, cs); err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"a < b; no plan here", "<div>HTML tool output</div>", "<proposed_other>not a plan</proposed_other>"} {
		payload, err := json.Marshal(map[string]string{"type": "text", "text": text})
		if err != nil {
			t.Fatal(err)
		}
		item := fmt.Sprintf("markup-%d", i)
		c := conversationChange(item, item, string(payload))
		c.Position = int64(500 + i)
		if _, err := repo.AppendConversationChanges(t.Context(), owner, []models.ConversationChange{c}); err != nil {
			t.Fatal(err)
		}
		a := &planReadCounter{DB: db}
		if err := displayindex.Refresh(t.Context(), a, owner.ExecutionID, c.Position, item); err != nil {
			t.Fatal(err)
		}
		if a.fullPlanReads != 0 {
			t.Fatalf("%q triggered %d full plan reads", text, a.fullPlanReads)
		}
	}

}

func TestDisplayPlanEveryOpeningTagSplit(t *testing.T) {
	const tag = "<proposed_plan>"
	for split := 1; split < len(tag); split++ {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			_, repo, owner := conversationTestRepo(t)
			parts := []string{"before " + tag[:split], tag[split:] + "approval</proposed_plan>after"}
			for i, text := range parts {
				payload, err := json.Marshal(map[string]string{"type": "text", "text": text})
				if err != nil {
					t.Fatal(err)
				}
				c := conversationChange(fmt.Sprint(i), fmt.Sprint(i), string(payload))
				c.Position = int64(i)
				if _, err := repo.AppendConversationChanges(t.Context(), owner, []models.ConversationChange{c}); err != nil {
					t.Fatal(err)
				}
			}
			page, err := repo.ReadProjectDisplayTail(t.Context(), "default", owner.ExecutionID, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			if page.Plan != tag+"approval</proposed_plan>\n" {
				t.Fatalf("missing plan: %q", page.Plan)
			}
		})
	}
}

func TestDisplayClosedLivePlanSkipsOrdinaryRefresh(t *testing.T) {
	db, repo, owner := conversationTestRepo(t)
	requirePlan := "<proposed_plan>approval</proposed_plan>"
	if err := repo.AppendOutput(t.Context(), owner.ExecutionID, requirePlan); err != nil {
		t.Fatal(err)
	}
	counter := &planReadCounter{DB: db}
	for _, text := range []string{"ordinary output", "a < b", "<div>HTML</div>"} {
		if err := displayindex.RefreshStreamPlan(t.Context(), counter, owner.ExecutionID, text); err != nil {
			t.Fatal(err)
		}
	}
	if counter.fullPlanReads != 0 {
		t.Fatalf("closed plan triggered %d full reads", counter.fullPlanReads)
	}
}
