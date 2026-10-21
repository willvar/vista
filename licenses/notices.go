// Package licenses embeds the notices that accompany the distributed program.
package licenses

import _ "embed"

// Text contains Vista's MIT license and the third-party license inventory.
//
//go:embed NOTICE
var Text string
