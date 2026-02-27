package marketplace

import "sigs.k8s.io/yaml"

// MarketplaceYAML represents the top-level marketplace catalog
type MarketplaceYAML struct {
	Version int               `yaml:"version" json:"version"`
	Items   []MarketplaceItem `yaml:"items" json:"items"`
}

// MarketplaceItem represents a single MCP server available for installation
type MarketplaceItem struct {
	ID          string             `yaml:"id" json:"id"`
	Name        string             `yaml:"name" json:"name"`
	Description string             `yaml:"description" json:"description"`
	Homepage    string             `yaml:"homepage" json:"homepage"`
	Verified    bool               `yaml:"verified" json:"verified"`
	Tags        []string           `yaml:"tags" json:"tags"`
	Version     string             `yaml:"version" json:"version"`
	Install     MarketplaceInstall `yaml:"install" json:"install"`
	Config      MarketplaceConfig  `yaml:"config" json:"config"`
}

// MarketplaceInstall holds installation methods (binary direct download or archive extraction)
type MarketplaceInstall struct {
	Binary  *BinaryInstall  `yaml:"binary,omitempty" json:"binary,omitempty"`
	Archive *ArchiveInstall `yaml:"archive,omitempty" json:"archive,omitempty"`
}

// BinaryInstall represents a direct binary download from GitHub Releases
type BinaryInstall struct {
	URL string `yaml:"url" json:"url"` // supports {os} and {arch} placeholders
}

// ArchiveInstall represents an archive download with binary extraction
type ArchiveInstall struct {
	URL         string `yaml:"url" json:"url"`
	Type        string `yaml:"type" json:"type"`               // "tar.gz" or "zip"
	ExtractPath string `yaml:"extractPath" json:"extractPath"` // path to binary inside archive
}

// MarketplaceConfig holds the MCP server configuration applied after installation
type MarketplaceConfig struct {
	ServerName string            `yaml:"serverName" json:"serverName"`
	Args       []string          `yaml:"args" json:"args"`
	Env        map[string]string `yaml:"env" json:"env"`
}

// LoadMarketplaceYAML parses marketplace YAML data into a structured catalog
func LoadMarketplaceYAML(data []byte) (*MarketplaceYAML, error) {
	var market MarketplaceYAML
	if err := yaml.Unmarshal(data, &market); err != nil {
		return nil, err
	}
	return &market, nil
}
