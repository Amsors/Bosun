// Package migrations exposes the versioned SQL migrations to the API binary.
package migrations

import "embed"

// Files contains every up/down migration consumed by golang-migrate.
//
//go:embed *.sql
var Files embed.FS
