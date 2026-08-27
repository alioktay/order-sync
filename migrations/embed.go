package migrations

import _ "embed"

// SQL contains the initial schema used by the service at startup.
//
//go:embed 001_initial.sql
var SQL string
