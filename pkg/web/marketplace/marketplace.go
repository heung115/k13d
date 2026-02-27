package marketplace

import _ "embed"

//go:embed mcp_market.yaml
var marketYAMLData []byte

// GetMarketplaceYAML returns the embedded marketplace catalog YAML
func GetMarketplaceYAML() []byte {
	return marketYAMLData
}
