package graph

import (
	"errors"
	"fmt"
	"time"

	"github.com/arham09/jejak/internal/repository"
)

// GenerationID identifies one candidate or active analysis generation.
type GenerationID int64

// CommitSHA identifies the Git commit represented by a generation.
type CommitSHA string

// GraphStatus describes the worktree's latest usable/attempted graph state.
type GraphStatus string

const (
	StatusUnindexed GraphStatus = "unindexed"
	StatusBuilding  GraphStatus = "building"
	StatusReady     GraphStatus = "ready"
	StatusStale     GraphStatus = "stale"
	StatusFailed    GraphStatus = "failed"
)

// GenerationState describes a generation's lifecycle independently from the
// worktree's latest status.
type GenerationState string

const (
	GenerationBuilding   GenerationState = "building"
	GenerationValidated  GenerationState = "validated"
	GenerationActive     GenerationState = "active"
	GenerationRetired    GenerationState = "retired"
	GenerationSuperseded GenerationState = "superseded"
	GenerationFailed     GenerationState = "failed"
)

// Generation is a validated or in-progress analysis result for one worktree.
type Generation struct {
	RepoID           repository.RepoID
	WorktreeID       repository.WorktreeID
	ID               GenerationID
	Commit           CommitSHA
	BuildFingerprint string
	AnalyzerVersion  string
	SchemaVersion    int
	State            GenerationState
	Error            string
	CreatedAt        time.Time
	ValidatedAt      *time.Time
	RetiredAt        *time.Time
}

// State is the durable worktree graph state shown by status and consumed by
// later freshness checks.
type State struct {
	RepoID           repository.RepoID
	WorktreeID       repository.WorktreeID
	WorktreePath     string
	Branch           string
	CurrentHead      CommitSHA
	HeadKnown        bool
	IndexedHead      CommitSHA
	ActiveGeneration *GenerationID
	Status           GraphStatus
	LastError        string
	GraphSchema      int
	AnalyzerVersion  string
	UpdatedAt        time.Time
}

// Validate checks the invariants that can be established without inspecting
// source-derived graph records.
func (s State) Validate() error {
	if s.RepoID == "" {
		return errors.New("graph state repository ID is empty")
	}
	if s.WorktreeID == "" {
		return errors.New("graph state worktree ID is empty")
	}
	switch s.Status {
	case StatusUnindexed, StatusBuilding, StatusReady, StatusStale, StatusFailed:
	default:
		return fmt.Errorf("unknown graph status %q", s.Status)
	}
	if s.Status == StatusReady && s.ActiveGeneration == nil {
		return errors.New("ready graph state has no active generation")
	}
	if s.ActiveGeneration != nil && *s.ActiveGeneration <= 0 {
		return errors.New("active generation must be positive")
	}
	if s.Status == StatusReady && s.IndexedHead == "" {
		return errors.New("ready graph state has no indexed HEAD")
	}
	return nil
}

// Validate checks the lifecycle fields for a generation candidate.
func (g Generation) Validate() error {
	if g.RepoID == "" {
		return errors.New("generation repository ID is empty")
	}
	if g.WorktreeID == "" {
		return errors.New("generation worktree ID is empty")
	}
	if g.ID <= 0 {
		return errors.New("generation ID must be positive")
	}
	if g.Commit == "" {
		return errors.New("generation commit is empty")
	}
	switch g.State {
	case GenerationBuilding, GenerationValidated, GenerationActive, GenerationRetired, GenerationSuperseded, GenerationFailed:
	default:
		return fmt.Errorf("unknown generation state %q", g.State)
	}
	return nil
}
