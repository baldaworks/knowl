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
