// Package chromefixture creates synthetic external Chrome cookie databases.
package chromefixture

import "database/sql"

func Open(path string) (*sql.DB, error) { return sql.Open("sqlite", path) }
