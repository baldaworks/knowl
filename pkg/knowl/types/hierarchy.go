package knowl

// HierarchyLimits bounds semantic hierarchy planning and catalog rendering.
// A complete input must fit these limits; callers must not silently truncate it.
type HierarchyLimits struct {
	MaxPages             int `json:"max_pages"`
	MaxCatalogs          int `json:"max_catalogs"`
	MaxEdges             int `json:"max_edges"`
	MaxDepth             int `json:"max_depth"`
	MaxInputBytes        int `json:"max_input_bytes"`
	MaxExcerptCharacters int `json:"max_excerpt_characters"`
	MaxPlanBytes         int `json:"max_plan_bytes"`
	MaxEdits             int `json:"max_edits"`
	MaxPathBytes         int `json:"max_path_bytes"`
	MaxCatalogBytes      int `json:"max_catalog_bytes"`
	MaxManifestBytes     int `json:"max_manifest_bytes"`
}

// HierarchyPage is bounded semantic context for one ordinary canonical page.
// It intentionally excludes raw source bytes and source-native paths.
type HierarchyPage struct {
	ID          PageID   `json:"id"`
	Path        string   `json:"path"`
	Digest      string   `json:"digest"`
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Excerpt     string   `json:"excerpt,omitempty"`
	Catalogs    []string `json:"catalogs,omitempty"`
}

// HierarchyCatalog describes current catalog membership without exposing
// catalog Markdown to a semantic planner.
type HierarchyCatalog struct {
	Path     string   `json:"path"`
	Digest   string   `json:"digest"`
	Title    string   `json:"title"`
	Children []string `json:"children,omitempty"`
}

// HierarchyInput is the complete bounded context supplied for one hierarchy
// plan. MinRootCatalogs is zero for a trivial wiki and at least two when the
// caller has classified the workspace as multi-domain.
type HierarchyInput struct {
	Scope           ScopeRef           `json:"scope"`
	SchemaDigest    string             `json:"schema_digest"`
	SnapshotDigest  string             `json:"snapshot_digest"`
	MinRootCatalogs int                `json:"min_root_catalogs,omitempty"`
	Pages           []HierarchyPage    `json:"pages"`
	Catalogs        []HierarchyCatalog `json:"catalogs"`
	Limits          HierarchyLimits    `json:"limits"`
}

// HierarchyCatalogSpec is one desired managed catalog and its complete child
// membership. Paths are canonical workspace paths below wiki/.
type HierarchyCatalogSpec struct {
	Path     string   `json:"path"`
	Title    string   `json:"title"`
	Children []string `json:"children"`
}

// HierarchyModelPlan is structured provider output before application
// validation and deterministic Markdown rendering.
type HierarchyModelPlan struct {
	SchemaDigest   string                 `json:"schema_digest"`
	SnapshotDigest string                 `json:"snapshot_digest"`
	Catalogs       []HierarchyCatalogSpec `json:"catalogs"`
}

// HierarchyMutation is one application-rendered managed catalog write or
// delete. A model never supplies this type directly.
type HierarchyMutation struct {
	Action         SourceMutationAction `json:"action"`
	Path           string               `json:"path"`
	ExpectedDigest string               `json:"expected_digest,omitempty"`
	Content        []byte               `json:"content,omitempty"`
}

// ValidatedHierarchyPlan is a normalized semantic graph plus deterministic
// catalog-only mutations ready for a hierarchy content adapter.
type ValidatedHierarchyPlan struct {
	Scope          ScopeRef               `json:"scope"`
	SchemaDigest   string                 `json:"schema_digest"`
	SnapshotDigest string                 `json:"snapshot_digest"`
	Catalogs       []HierarchyCatalogSpec `json:"catalogs"`
	Mutations      []HierarchyMutation    `json:"mutations"`
}
