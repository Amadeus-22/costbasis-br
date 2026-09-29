// Package rulesets embeds the YAML rulesets shipped with the binary, so the
// tool works without any files installed. COSTBASIS_RULESETS_DIR overrides it.
package rulesets

import "embed"

// FS holds every *.yaml file in this directory.
//
//go:embed *.yaml
var FS embed.FS
