package knowl

// CatalogLimits bounds complete source-maintenance navigation independently
// of the selected factual-page context.
type CatalogLimits struct {
	MaxCatalogs      int `json:"max_catalogs"`
	MaxEdges         int `json:"max_edges"`
	MaxDepth         int `json:"max_depth"`
	MaxPathBytes     int `json:"max_path_bytes"`
	MaxCatalogBytes  int `json:"max_catalog_bytes"`
	MaxSnapshotBytes int `json:"max_snapshot_bytes"`
	MaxInputBytes    int `json:"max_input_bytes"`
}

// CatalogAddition adds navigation to an existing catalog or creates a new one.
// Existing catalogs require ExpectedDigest and omit Title. New catalogs require
// Title and an empty ExpectedDigest. Children are canonical wiki file paths.
type CatalogAddition struct {
	Path           string   `json:"path"`
	ExpectedDigest string   `json:"expected_digest,omitempty"`
	Title          string   `json:"title,omitempty"`
	Children       []string `json:"children"`
}
