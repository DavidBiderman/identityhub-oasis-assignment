// Package api embeds the OpenAPI document so the process can serve the
// specification it was built from.
//
// Mirrors backend/db, which embeds the migrations for the same reason: a
// document that ships inside the binary cannot have moved on from the code
// that implements it.
package api

import _ "embed"

// Spec is api/openapi.yaml.
//
//go:embed openapi.yaml
var Spec []byte
