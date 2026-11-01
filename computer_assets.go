package computerassets

import "embed"

// Source contains the standalone desktop image build context for packaged apps.
//
//go:embed cmd/computer/*.go packaging/computer/Dockerfile packaging/computer/start.sh packaging/computer/*.cjs
var Source embed.FS
