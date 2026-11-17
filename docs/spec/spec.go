// Package spec embeds the Restore Evidence Format specification (CC0), so an auditor's evidence export carries the
// text it is verified against.
package spec

import _ "embed"

// EvidenceV1 is docs/spec/evidence-v1.md.
//
//go:embed evidence-v1.md
var EvidenceV1 []byte
