package cardcursor

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/stretchr/testify/require"
)

func TestMultipleSourcesAdvanceOnlyDisplayedCards(t *testing.T) {
	for _, order := range []string{"name_asc", "name_desc", "updated_desc", "updated_asc"} {
		t.Run(order, func(t *testing.T) {
			sources := map[string][]models.AutomationCard{}
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			for si, source := range []string{"", "0123456789abcdef", "fedcba9876543210"} {
				for i := 0; i < 105; i++ {
					id := fmt.Sprintf("%032x", i+1)
					if source != "" {
						id = remoteid.ID(source, id)
					}
					// Remote 1 dominates ascending names, including equal-name ties.
					name := fmt.Sprintf("%c%03d", 'B'+si, i/2)
					if si == 1 {
						name = fmt.Sprintf("A%03d", i/2)
					}
					sources[source] = append(sources[source], models.AutomationCard{Automation: models.Automation{ID: id, Name: name, UpdatedAt: now.Add(time.Duration(i/2) * time.Second)}})
				}
			}
			terms := Terms("automations", order)
			var expected []models.AutomationCard
			for source, rows := range sources {
				sort.Slice(rows, func(i, j int) bool {
					_, a := RowKey(rows[i], terms)
					_, b := RowKey(rows[j], terms)
					return Compare(a, b, terms) < 0
				})
				sources[source] = rows
				expected = append(expected, rows...)
			}
			sort.Slice(expected, func(i, j int) bool {
				as, a := RowKey(expected[i], terms)
				bs, b := RowKey(expected[j], terms)
				if c := Compare(a, b, terms); c != 0 {
					return c < 0
				}
				return as < bs
			})
			token := ""
			var got []models.AutomationCard
			for n := 0; n < 30; n++ {
				state := New("automations", order, "search/sort/project", token)
				require.NoError(t, state.Err)
				var candidates []models.AutomationCard
				for source, rows := range sources {
					after := state.Token.Sources[source]
					count := 0
					for _, row := range rows {
						_, key := RowKey(row, terms)
						if len(after) > 0 && Compare(key, after, terms) <= 0 {
							continue
						}
						candidates = append(candidates, row)
						count++
						if count == 21 {
							break
						}
					}
					if _, ok := state.Token.Sources[source]; !ok {
						state.Token.Sources[source] = nil
					}
				}
				ctx := With(context.Background(), state)
				page, more, err := Finish(ctx, candidates, 20)
				require.NoError(t, err)
				got = append(got, page...)
				if n == 0 && order == "name_asc" {
					require.Empty(t, state.Token.Sources[""])
					require.Empty(t, state.Token.Sources["fedcba9876543210"])
				}
				token = Encoded(ctx)
				if !more {
					break
				}
			}
			require.Equal(t, expected, got)
		})
	}
}
func TestSkillsWithSameHandleAcrossProjectsContinue(t *testing.T) {
	type skillCard struct {
		ProjectID, Name, Scope, Source, Handle string
	}
	cards := []skillCard{
		{ProjectID: "project1", Name: "Backup", Scope: "project", Source: "project", Handle: "backup"},
		{ProjectID: "project2", Name: "Backup", Scope: "project", Source: "project", Handle: "backup"},
		{ProjectID: remoteid.ID("0123456789abcdef", "project1"), Name: "Backup", Scope: "project", Source: "project", Handle: "backup"},
		{ProjectID: remoteid.ID("fedcba9876543210", "project1"), Name: "Backup", Scope: "project", Source: "project", Handle: "backup"},
		{Name: "Backup", Scope: "global", Source: "global", Handle: "backup"},
	}
	for _, order := range []string{"name_asc", "name_desc", "scope", "source"} {
		t.Run(order, func(t *testing.T) {
			var got []skillCard
			token := ""
			for i := 0; i < len(cards); i++ {
				ctx := With(context.Background(), New("skills", order, "all projects", token))
				page, more, err := Finish(ctx, append([]skillCard(nil), cards...), 1)
				require.NoError(t, err)
				require.Len(t, page, 1, "each remaining skill must be reachable")
				got = append(got, page...)
				require.Equal(t, i < len(cards)-1, more)
				token = Encoded(ctx)
			}
			require.ElementsMatch(t, cards, got)
		})
	}
}

func TestCursorValidationAndScope(t *testing.T) {
	s := New("models", "name_asc", "project 1", "")
	s.Token.Sources[""] = Key{"name", "Name", "id"}
	s.Token.Sources["0123456789abcdef"] = nil
	token := Encoded(With(context.Background(), s))
	require.NoError(t, New("models", "name_asc", "project 1", token).Err)
	require.ErrorIs(t, New("models", "name_asc", "project 2", token).Err, ErrInvalid)
	require.ErrorIs(t, New("models", "name_asc", "project 1", "not a token").Err, ErrInvalid)
	require.Equal(t, "Éclair", Fold("ÉCLAIR"), "match SQLite's ASCII-only NOCASE")
}
