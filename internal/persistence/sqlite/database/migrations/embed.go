package migrations

import "embed"

//go:embed *.sql *.go
var FS embed.FS
