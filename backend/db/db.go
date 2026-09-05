// Package db embeds the SQL migrations so that the API and migrate binaries
// carry their schema with them and no files need to be mounted at runtime.
package db

import "embed"

//go:embed migrations/*.sql
var Migrations embed.FS
