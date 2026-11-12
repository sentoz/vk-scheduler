package static

import "embed"

// Schemas contains schemas generated from the runtime configuration types.
//
//go:embed schemas/*.json
var Schemas embed.FS
