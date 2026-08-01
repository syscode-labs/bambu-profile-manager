// Package storage defines repository interfaces built around this app's use
// cases (design.md §8), not generic SQL wrappers. Bindings repository is
// added once the rebind work needs profile-independent persistence beyond
// what internal/rebind's in-memory Result already provides.
package storage

import (
	"context"
	"errors"

	"github.com/syscod3/bambu-profile-manager/internal/domain"
	"github.com/syscod3/bambu-profile-manager/internal/reconcile"
)

// ErrNotFound is returned by Get-style methods when nothing matches.
var ErrNotFound = errors.New("storage: not found")

type ProfileRepository interface {
	Create(ctx context.Context, name string) (domain.Profile, error)
	Get(ctx context.Context, id string) (domain.Profile, error)
	GetByName(ctx context.Context, name string) (domain.Profile, error)
	List(ctx context.Context) ([]domain.Profile, error)
}

// VersionRepository stores immutable ProfileVersion revisions. There is no
// Update: a new version is always a new row.
type VersionRepository interface {
	Create(ctx context.Context, v domain.ProfileVersion) (domain.ProfileVersion, error)
	Latest(ctx context.Context, profileID string) (domain.ProfileVersion, error)
	List(ctx context.Context, profileID string) ([]domain.ProfileVersion, error)
}

// EventRepository is an append-only domain event log (design.md §10).
type EventRepository interface {
	Append(ctx context.Context, e domain.DomainEvent) (domain.DomainEvent, error)
	ListSince(ctx context.Context, sequence int64) ([]domain.DomainEvent, error)
}

// DeploymentRepository persists reconcile.Deployment (state + full
// transition history) so a deployment's progress survives past one process
// run, and so rollback can find the most recent ACTIVE revision.
type DeploymentRepository interface {
	// Save upserts by Deployment.ID: a Deployment's ID is fixed at creation,
	// but its State/History mutate as it advances (design.md §7's
	// Deployment record, unlike ProfileVersion, is not itself immutable —
	// only the sequence of Transitions in its History is).
	Save(ctx context.Context, d reconcile.Deployment) error
	Get(ctx context.Context, id string) (reconcile.Deployment, error)
	ListByProfile(ctx context.Context, profileID string) ([]reconcile.Deployment, error)
}

// Repository is the storage facade. WithinTransaction gives fn a
// transaction-scoped Repository; writes across Profiles/Versions/Events made
// through tx are only visible once fn returns nil (design.md §9 mutation
// order: update, insert version, insert event, all in one transaction).
type Repository interface {
	Profiles() ProfileRepository
	Versions() VersionRepository
	Events() EventRepository
	Deployments() DeploymentRepository

	WithinTransaction(ctx context.Context, fn func(ctx context.Context, tx Repository) error) error

	Close() error
}
