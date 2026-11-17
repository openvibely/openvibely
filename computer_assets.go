package computerassets

import "embed"

// Source contains the standalone desktop image build context for packaged apps.
//
//go:embed pkg/agenttools/*.go pkg/executionenv/*.go pkg/gitidentity/*.go pkg/toolschema/*.go cmd/computer/*.go packaging/computer/Dockerfile packaging/computer/*.sh packaging/computer/*.cjs packaging/computer/SOURCE-OFFER.txt
var Source embed.FS
