// Package agentskill installs Jejak's canonical agent instructions into a
// selected project without duplicating the authored skill.
package agentskill

import _ "embed"

// canonicalSkill is the single authored Jejak skill bundled with the binary.
//
//go:embed jejak/SKILL.md
var canonicalSkill []byte
