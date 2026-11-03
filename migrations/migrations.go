// Package migrations embeds the SQL migration manifest so the migrate runner
// and the test harness share one source of truth (P-007: file + registration
// are a single change).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
