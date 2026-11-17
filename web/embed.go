// Package web embeds the built web UI (web/dist, T20) into the gaia binary. The build is committed (PO decision
// 2026-11-17): building or releasing Gaia needs no Node; changing the UI does (`make web`).
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
