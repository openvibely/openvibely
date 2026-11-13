package pageplugins

import _ "embed"

// AuthoringGuide travels with the Go binary, including when the user's project
// does not contain the OpenVibely source tree.
//
//go:embed authoring.md
var AuthoringGuide string
