package knowl

// RetirementOwnerAuthority is the exact retiring-owner fence carried by one
// page-scoped publication unit. Equal source IDs under different adapters are
// deliberately distinct owners.
type RetirementOwnerAuthority struct {
	Owner                SourceOwner `json:"owner" yaml:"owner"`
	ConfigDigest         string      `json:"config_digest" yaml:"config_digest"`
	RetirementGeneration string      `json:"retirement_generation" yaml:"retirement_generation"`
	RetirementEpoch      uint64      `json:"retirement_epoch" yaml:"retirement_epoch"`
}

// RetirementPlanContext confines lineage removal and explicit deletion to one
// exact current page under one lifecycle authority generation.
type RetirementPlanContext struct {
	Scope               ScopeRef                   `json:"scope" yaml:"scope"`
	PageID              PageID                     `json:"page_id" yaml:"page_id"`
	PagePath            string                     `json:"page_path" yaml:"page_path"`
	PageDigest          string                     `json:"page_digest" yaml:"page_digest"`
	AuthorityGeneration string                     `json:"authority_generation" yaml:"authority_generation"`
	AuthorityEpoch      uint64                     `json:"authority_epoch" yaml:"authority_epoch"`
	Owners              []RetirementOwnerAuthority `json:"owners" yaml:"owners"`
}

// RetirementAction is the complete result for one canonical page.
type RetirementAction string

const (
	// RetirementReplace publishes one complete replacement of the current page.
	RetirementReplace RetirementAction = "replace"
	// RetirementDelete removes the current page because no supported page remains.
	RetirementDelete RetirementAction = "delete"
)

// RetirementEvidence is one exact accepted active revision made available to
// page-scoped source-retirement recomputation.
type RetirementEvidence struct {
	SourceRef string         `json:"source_ref"`
	Source    AcceptedSource `json:"source"`
	Text      string         `json:"text"`
}

// RetirementInput is the bounded page-scoped request supplied to an optional
// retirement-aware maintainer. Evidence contains active accepted revisions
// only and is sorted by SourceRef.
type RetirementInput struct {
	ContractVersion string                 `json:"contract_version"`
	InputLimits     MaintenanceInputLimits `json:"input_limits"`
	CatalogLimits   CatalogLimits          `json:"catalog_limits"`
	Scope           ScopeRef               `json:"scope"`
	Schema          SchemaDocument         `json:"schema"`
	Page            PageSnapshot           `json:"page"`
	Evidence        []RetirementEvidence   `json:"evidence"`
	Catalogs        []HierarchyCatalog     `json:"catalogs,omitempty"`
	Limits          ReadLimits             `json:"limits"`
}

// RetirementModelPlan is untrusted provider output. Delete requires no Pages;
// replacement requires exactly one complete page with its current preimage.
type RetirementModelPlan struct {
	SchemaDigest string     `json:"schema_digest"`
	Delete       bool       `json:"delete"`
	SourceRefs   []string   `json:"source_refs"`
	Pages        []FileEdit `json:"pages"`
	Rationale    string     `json:"rationale,omitempty"`
}

// RetirementPlanResult is the application-validated, page-scoped decision.
// It contains no staging or publication authority.
type RetirementPlanResult struct {
	Action      RetirementAction        `json:"action"`
	Page        PageSnapshot            `json:"page"`
	SourceRefs  []string                `json:"source_refs"`
	Diagnostics []MaintenanceDiagnostic `json:"diagnostics,omitempty"`
}
