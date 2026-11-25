package sqlite

import (
	"context"
	"strconv"
	"strings"

	"github.com/openvibely/openvibely/internal/cardcursor"
)

// cardCursorQuery replaces the final offset/order clause of a card query. Other
// store callers retain the existing offset API. All filters remain before the
// seek predicate, including aggregate project permission predicates.
func cardCursorQuery(ctx context.Context, kind, alias, query string, args []any) (string, []any, error) {
	s := cardcursor.From(ctx)
	if s == nil || s.Kind != kind {
		return query, args, nil
	}
	if s.Err != nil {
		return "", nil, s.Err
	}
	order := strings.LastIndex(query, " ORDER BY ")
	if order < 0 || len(args) < 2 {
		return "", nil, cardcursor.ErrInvalid
	}
	limit := args[len(args)-2]
	if s.Limit > 0 {
		if s.Limit > 501 {
			return "", nil, cardcursor.ErrInvalid
		}
		limit = s.Limit
	}
	args = append([]any(nil), args[:len(args)-2]...)
	query = query[:order]
	terms := cardcursor.Terms(kind, s.Sort)
	columns := make([]string, len(terms))
	directions := make([]string, len(terms))
	fieldColumns := map[string]string{"Name": "name", "ID": "id", "Provider": "provider", "CreatedAt": "created_at", "UpdatedAt": "updated_at", "IsRead": "is_read"}
	col := func(name string) string {
		if alias != "" {
			return alias + "." + name
		}
		return name
	}
	for i, t := range terms {
		c := col(fieldColumns[t.Field])
		switch t.Field {
		case "CreatedAt", "UpdatedAt":
			c = cardCursorTimestamp(c)
		case "SeverityRank":
			c = "CASE " + col("severity") + " WHEN 'error' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END"
		}
		if t.Fold {
			c += " COLLATE NOCASE"
		}
		columns[i] = c
		d := " ASC"
		if t.Desc {
			d = " DESC"
		}
		directions[i] = c + d
	}
	after := s.Token.Sources[""]
	if len(after) > 0 {
		if len(after) != len(terms) {
			return "", nil, cardcursor.ErrInvalid
		}
		bind := func(i int) any {
			if terms[i].Field == "IsRead" || terms[i].Field == "SeverityRank" {
				n, _ := strconv.Atoi(after[i])
				return n
			}
			return after[i]
		}
		var branches []string
		for i, t := range terms {
			var conditions []string
			for j := 0; j < i; j++ {
				conditions = append(conditions, columns[j]+" = ?")
				args = append(args, bind(j))
			}
			op := " > ?"
			if t.Desc {
				op = " < ?"
			}
			conditions = append(conditions, columns[i]+op)
			args = append(args, bind(i))
			branches = append(branches, "("+strings.Join(conditions, " AND ")+")")
		}
		query += " AND (" + strings.Join(branches, " OR ") + ")"
	}
	query += " ORDER BY " + strings.Join(directions, ", ") + " LIMIT ?"
	return query, append(args, limit), nil
}

// SQLite stores both CURRENT_TIMESTAMP strings and driver-bound Go timestamps
// (which can end in "+0000 UTC"). Normalize both without dropping nanoseconds;
// SQLite's %f alone rounds to milliseconds and rejects the Go zone suffix.
func cardCursorTimestamp(c string) string {
	zone := "CASE WHEN substr(" + c + ", -6, 1) IN ('+', '-') THEN substr(" + c + ", -6) WHEN substr(" + c + ", -9, 1) IN ('+', '-') THEN substr(" + c + ", -9, 3) || ':' || substr(" + c + ", -6, 2) ELSE '' END"
	fraction := "CASE WHEN substr(" + c + ",20,1) = '.' THEN substr(printf('%.9f', CAST('0.' || substr(" + c + ",21) AS REAL)),3,9) ELSE '000000000' END"
	return "(strftime('%Y-%m-%dT%H:%M:%S', substr(" + c + ",1,19) || (" + zone + ")) || '.' || (" + fraction + ") || 'Z')"
}
