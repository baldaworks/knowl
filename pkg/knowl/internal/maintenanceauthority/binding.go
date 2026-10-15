// Package maintenanceauthority seals configuration of the Host's ordinary
// canonical publication boundary from downstream workspace callers.
package maintenanceauthority

import knowl "github.com/baldaworks/knowl/pkg/knowl/types"

// Binding can only be supplied by packages inside the Knowl implementation.
type Binding struct{ hostOnly bool }

func NewBinding() Binding           { return Binding{hostOnly: true} }
func (binding Binding) Valid() bool { return binding.hostOnly }

type Owner struct {
	Owner          knowl.SourceOwner `yaml:"owner"`
	ConfigDigest   string            `yaml:"config_digest"`
	ActivatedEpoch uint64            `yaml:"activated_epoch"`
}
type Stamp struct {
	Scope      knowl.ScopeRef `yaml:"scope"`
	Generation string         `yaml:"generation"`
	Epoch      uint64         `yaml:"epoch"`
	Owners     []Owner        `yaml:"owners,omitempty"`
}

type StageRequest struct {
	plan  knowl.ValidatedEditPlan
	stamp *Stamp
}

func NewStageRequest(plan knowl.ValidatedEditPlan, stamp *Stamp) StageRequest {
	return StageRequest{plan: plan, stamp: stamp}
}
func (request StageRequest) Plan() knowl.ValidatedEditPlan { return request.plan }
func (request StageRequest) Stamp() *Stamp                 { return request.stamp }

type HierarchyStageRequest struct {
	id    knowl.OperationID
	plan  knowl.ValidatedHierarchyPlan
	stamp *Stamp
}

func NewHierarchyStageRequest(id knowl.OperationID, plan knowl.ValidatedHierarchyPlan, stamp *Stamp) HierarchyStageRequest {
	return HierarchyStageRequest{id: id, plan: plan, stamp: stamp}
}
func (request HierarchyStageRequest) ID() knowl.OperationID              { return request.id }
func (request HierarchyStageRequest) Plan() knowl.ValidatedHierarchyPlan { return request.plan }
func (request HierarchyStageRequest) Stamp() *Stamp                      { return request.stamp }

type CommitRequest struct {
	stage     knowl.StagedChange
	digest    string
	hierarchy bool
}

func NewCommitRequest(stage knowl.StagedChange, hierarchy bool) CommitRequest {
	return CommitRequest{stage: stage, digest: stage.Digest, hierarchy: hierarchy}
}
func (request CommitRequest) Stage() knowl.StagedChange { return request.stage }
func (request CommitRequest) Digest() string            { return request.digest }
func (request CommitRequest) Hierarchy() bool           { return request.hierarchy }

type InspectRequest struct {
	stage     knowl.StagedChange
	hierarchy bool
}

func NewInspectRequest(stage knowl.StagedChange, hierarchy bool) InspectRequest {
	return InspectRequest{stage: stage, hierarchy: hierarchy}
}
func (request InspectRequest) Stage() knowl.StagedChange { return request.stage }
func (request InspectRequest) Hierarchy() bool           { return request.hierarchy }

type Inspection struct {
	Scope     knowl.ScopeRef
	Stage     knowl.StagedChange
	Stamp     *Stamp
	Edits     []knowl.FileEdit
	Declared  []string
	Committed *knowl.ContentCommit
}
