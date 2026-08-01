// Package storagetest is a reusable contract test suite for any
// storage.Repository implementation (design.md §19 "Repository contract
// tests" — run it against SQLite now, Postgres later when implemented).
package storagetest

import (
	"context"
	"errors"
	"testing"

	"github.com/syscod3/bambu-profile-manager/internal/domain"
	"github.com/syscod3/bambu-profile-manager/internal/reconcile"
	"github.com/syscod3/bambu-profile-manager/internal/storage"
)

// Run exercises the storage.Repository contract against a fresh, empty
// instance returned by newRepo for every subtest.
func Run(t *testing.T, newRepo func(t *testing.T) storage.Repository) {
	t.Run("profile create and get round-trip", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		p, err := repo.Profiles().Create(ctx, "Syscode - AmazonBasics ABS 0.6")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if p.ID == "" {
			t.Fatal("Create returned empty ID")
		}

		got, err := repo.Profiles().Get(ctx, p.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got != p {
			t.Fatalf("Get returned %+v, want %+v", got, p)
		}

		byName, err := repo.Profiles().GetByName(ctx, p.Name)
		if err != nil {
			t.Fatalf("GetByName: %v", err)
		}
		if byName != p {
			t.Fatalf("GetByName returned %+v, want %+v", byName, p)
		}
	})

	t.Run("profile not found", func(t *testing.T) {
		repo := newRepo(t)
		_, err := repo.Profiles().Get(context.Background(), "does-not-exist")
		if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("Get(missing) = %v, want ErrNotFound", err)
		}
	})

	t.Run("versions are immutable and ordered", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		p, err := repo.Profiles().Create(ctx, "leaf")
		if err != nil {
			t.Fatalf("Create profile: %v", err)
		}

		v1, err := repo.Versions().Create(ctx, domain.ProfileVersion{ProfileID: p.ID, Revision: 1, SourceJSON: []byte(`{"a":1}`), ResolvedJSON: []byte(`{"a":1}`), SemanticHash: "hash1"})
		if err != nil {
			t.Fatalf("Create v1: %v", err)
		}
		v2, err := repo.Versions().Create(ctx, domain.ProfileVersion{ProfileID: p.ID, Revision: 2, SourceJSON: []byte(`{"a":2}`), ResolvedJSON: []byte(`{"a":2}`), SemanticHash: "hash2"})
		if err != nil {
			t.Fatalf("Create v2: %v", err)
		}
		if v1.ID == v2.ID {
			t.Fatal("two versions got the same ID")
		}

		latest, err := repo.Versions().Latest(ctx, p.ID)
		if err != nil {
			t.Fatalf("Latest: %v", err)
		}
		if latest.Revision != 2 || latest.SemanticHash != "hash2" {
			t.Fatalf("Latest = revision %d hash %s, want revision 2 hash hash2", latest.Revision, latest.SemanticHash)
		}

		all, err := repo.Versions().List(ctx, p.ID)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(all) != 2 || all[0].Revision != 1 || all[1].Revision != 2 {
			t.Fatalf("List returned %+v, want revisions [1,2] in order", all)
		}
	})

	t.Run("events are appended with increasing sequence and replayable since a point", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		e1, err := repo.Events().Append(ctx, domain.DomainEvent{Type: "profile.updated", EntityID: "p1", Payload: []byte(`{}`)})
		if err != nil {
			t.Fatalf("Append e1: %v", err)
		}
		e2, err := repo.Events().Append(ctx, domain.DomainEvent{Type: "profile.updated", EntityID: "p1", Payload: []byte(`{}`)})
		if err != nil {
			t.Fatalf("Append e2: %v", err)
		}
		if e2.Sequence <= e1.Sequence {
			t.Fatalf("sequence did not increase: e1=%d e2=%d", e1.Sequence, e2.Sequence)
		}

		since, err := repo.Events().ListSince(ctx, e1.Sequence)
		if err != nil {
			t.Fatalf("ListSince: %v", err)
		}
		if len(since) != 1 || since[0].ID != e2.ID {
			t.Fatalf("ListSince(%d) = %+v, want only e2", e1.Sequence, since)
		}
	})

	t.Run("WithinTransaction commits all writes together", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		err := repo.WithinTransaction(ctx, func(ctx context.Context, tx storage.Repository) error {
			p, err := tx.Profiles().Create(ctx, "tx-profile")
			if err != nil {
				return err
			}
			_, err = tx.Versions().Create(ctx, domain.ProfileVersion{ProfileID: p.ID, Revision: 1, SourceJSON: []byte("{}"), ResolvedJSON: []byte("{}"), SemanticHash: "h"})
			return err
		})
		if err != nil {
			t.Fatalf("WithinTransaction: %v", err)
		}

		p, err := repo.Profiles().GetByName(ctx, "tx-profile")
		if err != nil {
			t.Fatalf("profile not visible after commit: %v", err)
		}
		v, err := repo.Versions().Latest(ctx, p.ID)
		if err != nil || v.Revision != 1 {
			t.Fatalf("version not visible after commit: v=%+v err=%v", v, err)
		}
	})

	t.Run("WithinTransaction rolls back every write on error, leaving no partial revision", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()
		sentinel := errors.New("boom")

		err := repo.WithinTransaction(ctx, func(ctx context.Context, tx storage.Repository) error {
			if _, err := tx.Profiles().Create(ctx, "rolled-back"); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("WithinTransaction error = %v, want sentinel", err)
		}

		_, err = repo.Profiles().GetByName(ctx, "rolled-back")
		if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("profile from rolled-back tx is visible: err=%v, want ErrNotFound", err)
		}
	})

	t.Run("deployments save, get, and list by profile, upserting by ID", func(t *testing.T) {
		repo := newRepo(t)
		ctx := context.Background()

		p, err := repo.Profiles().Create(ctx, "deploy-target")
		if err != nil {
			t.Fatalf("Create profile: %v", err)
		}

		d := reconcile.New("dep-1", p.ID, 1)
		if err := d.Advance(reconcile.StateValidated, "ok"); err != nil {
			t.Fatalf("Advance: %v", err)
		}
		if err := repo.Deployments().Save(ctx, *d); err != nil {
			t.Fatalf("Save: %v", err)
		}

		got, err := repo.Deployments().Get(ctx, d.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.State != reconcile.StateValidated || len(got.History) != 1 {
			t.Fatalf("Get = %+v, want State=VALIDATED with 1 history entry", got)
		}

		// Save again with the same ID (further advanced) must upsert, not duplicate.
		if err := d.Advance(reconcile.StateStaged, "staged"); err != nil {
			t.Fatalf("Advance: %v", err)
		}
		if err := repo.Deployments().Save(ctx, *d); err != nil {
			t.Fatalf("Save (update): %v", err)
		}

		list, err := repo.Deployments().ListByProfile(ctx, p.ID)
		if err != nil {
			t.Fatalf("ListByProfile: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("ListByProfile returned %d deployments, want 1 (Save must upsert by ID)", len(list))
		}
		if list[0].State != reconcile.StateStaged || len(list[0].History) != 2 {
			t.Fatalf("ListByProfile[0] = %+v, want State=STAGED with 2 history entries", list[0])
		}
	})

	t.Run("deployment not found", func(t *testing.T) {
		repo := newRepo(t)
		_, err := repo.Deployments().Get(context.Background(), "does-not-exist")
		if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("Get(missing) = %v, want ErrNotFound", err)
		}
	})
}
