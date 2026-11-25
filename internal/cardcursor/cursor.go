// Package cardcursor implements stateless, per-source card continuation tokens.
// Tokens are positions, never authorization: every query still applies its
// normal project permissions and search predicates.
package cardcursor

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/remoteid"
)

var ErrInvalid = errors.New("invalid card cursor; refresh the list")

const MaxTokenSize = 128 << 10

type Key []string
type Token struct {
	Version int            `json:"v"`
	Scope   string         `json:"q"`
	Sources map[string]Key `json:"s"`
}
type State struct {
	Limit int `json:"limit,omitempty"`
	Token Token
	Kind  string
	Sort  string
	Err   error `json:"-"`
}
type contextKey struct{}

func With(ctx context.Context, s *State) context.Context {
	return context.WithValue(ctx, contextKey{}, s)
}
func From(ctx context.Context) *State { s, _ := ctx.Value(contextKey{}).(*State); return s }
func New(kind, order, scope, encoded string) *State {
	sum := sha256.Sum256([]byte(scope))
	s := &State{Kind: kind, Sort: order, Token: Token{Version: 1, Scope: fmt.Sprintf("%x", sum[:]), Sources: map[string]Key{}}}
	if encoded == "" {
		return s
	}
	if len(encoded) > MaxTokenSize {
		s.Err = ErrInvalid
		return s
	}
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	var t Token
	if err != nil || json.Unmarshal(b, &t) != nil || t.Version != 1 || t.Scope != s.Token.Scope || len(t.Sources) > 1024 {
		s.Err = ErrInvalid
		return s
	}
	for source, k := range t.Sources {
		if len(source) > 128 || len(k) > 8 {
			s.Err = ErrInvalid
			return s
		}
		for _, v := range k {
			if len(v) > 8192 {
				s.Err = ErrInvalid
				return s
			}
		}
	}
	if t.Sources == nil {
		t.Sources = map[string]Key{}
	}
	s.Token = t
	return s
}
func Encoded(ctx context.Context) string {
	s := From(ctx)
	if s == nil || s.Err != nil {
		return ""
	}
	b, _ := json.Marshal(s.Token)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ForSource sends only the owning server's position, not other servers' state.
func (s *State) ForSource(source string) *State {
	return &State{Kind: s.Kind, Sort: s.Sort, Err: s.Err, Limit: s.Limit, Token: Token{Version: 1, Sources: map[string]Key{"": s.Token.Sources[source]}}}
}

// Term describes identical Go and SQLite ordering. Fields are trusted constants,
// not SQL supplied by a browser. ASCII folding matches SQLite NOCASE.
type Term struct {
	Field      string
	Fold, Desc bool
}

func Terms(kind, order string) []Term {
	id := Term{Field: "ID"}
	name := []Term{{Field: "Name", Fold: true}, {Field: "Name"}, id}
	switch kind {
	case "alerts":
		date := Term{Field: "CreatedAt", Desc: order != "oldest"}
		id.Desc = date.Desc
		switch order {
		case "severity":
			return []Term{{Field: "SeverityRank"}, date, id}
		case "unread_first":
			return []Term{{Field: "IsRead"}, date, id}
		}
		return []Term{date, id}
	case "automations", "agents":
		if order == "updated_desc" || order == "updated_asc" || order == "created_desc" {
			f := "UpdatedAt"
			if order == "created_desc" {
				f = "CreatedAt"
			}
			id.Desc = kind == "agents" && order != "updated_asc"
			return []Term{{Field: f, Desc: order != "updated_asc"}, id}
		}
	case "models":
		if order == "provider" {
			return append([]Term{{Field: "Provider", Fold: true}, {Field: "Provider"}}, name...)
		}
	case "skills":
		name[2].Field = "SkillKey"
		if order == "scope" {
			return append([]Term{{Field: "Scope"}}, name...)
		}
		if order == "source" {
			return append([]Term{{Field: "Source"}}, name...)
		}
	case "personalities":
		name[2].Field = "Key"
	}
	if order == "name_desc" {
		for i := range name {
			name[i].Desc = true
		}
	}
	return name
}
func Fold(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return r
	}, s)
}
func value(v reflect.Value, field string) string {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		v = v.Elem()
	}
	if a := v.FieldByName("Automation"); a.IsValid() {
		v = a
	}
	if field == "SkillKey" {
		return value(v, "ProjectID") + ":" + value(v, "Scope") + ":" + value(v, "Handle")
	}
	if field == "SeverityRank" {
		switch value(v, "Severity") {
		case "error":
			return "0"
		case "warning":
			return "1"
		default:
			return "2"
		}
	}
	f := v.FieldByName(field)
	if !f.IsValid() {
		return ""
	}
	if t, ok := f.Interface().(time.Time); ok {
		return t.UTC().Format("2006-01-02T15:04:05.000000000Z")
	}
	if f.Kind() == reflect.Bool {
		if f.Bool() {
			return "1"
		}
		return "0"
	}
	return fmt.Sprint(f.Interface())
}
func RowKey(row any, terms []Term) (string, Key) {
	v := reflect.ValueOf(row)
	id := value(v, "ID")
	source, _, _ := remoteid.Split(id)
	k := make(Key, len(terms))
	for i, t := range terms {
		k[i] = value(v, t.Field)
		if t.Field == "ID" {
			k[i] = remoteid.Strip(source, k[i])
		}
		if t.Fold {
			k[i] = Fold(k[i])
		}
	}
	return source, k
}
func Compare(a, b Key, terms []Term) int {
	for i, t := range terms {
		c := strings.Compare(a[i], b[i])
		if t.Desc {
			c = -c
		}
		if c != 0 {
			return c
		}
	}
	return 0
}

// Finish globally sorts candidate rows and advances positions only for emitted
// rows. The extra lookahead row and unused remote candidates never advance a cursor.
func Finish[T any](ctx context.Context, rows []T, limit int) ([]T, bool, error) {
	s := From(ctx)
	if s == nil {
		return rows, false, ErrInvalid
	}
	if s.Err != nil {
		return nil, false, s.Err
	}
	terms := Terms(s.Kind, s.Sort)
	for _, k := range s.Token.Sources {
		if len(k) != 0 && len(k) != len(terms) {
			return nil, false, ErrInvalid
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		as, a := RowKey(rows[i], terms)
		bs, b := RowKey(rows[j], terms)
		if c := Compare(a, b, terms); c != 0 {
			return c < 0
		}
		return as < bs
	})
	kept := rows[:0]
	for _, row := range rows {
		source, k := RowKey(row, terms)
		after := s.Token.Sources[source]
		if len(after) == 0 || Compare(k, after, terms) > 0 {
			kept = append(kept, row)
		}
	}
	more := len(kept) > limit
	if more {
		kept = kept[:limit]
	}
	for _, row := range kept {
		source, k := RowKey(row, terms)
		s.Token.Sources[source] = k
	}
	return kept, more, nil
}
