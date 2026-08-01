// Package service wires domain -> storage -> bundle -> rebind -> bambuadapter
// -> reconcile together into the flows described in design.md §22 (the
// primary acceptance test) and §25 (first spike deliverable). It's the
// integration layer cmd/bambupm and the future web UI both call into.
package service

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/syscod3/bambu-profile-manager/internal/bambuadapter"
	"github.com/syscod3/bambu-profile-manager/internal/bundle"
	"github.com/syscod3/bambu-profile-manager/internal/domain"
	"github.com/syscod3/bambu-profile-manager/internal/rebind"
	"github.com/syscod3/bambu-profile-manager/internal/reconcile"
	"github.com/syscod3/bambu-profile-manager/internal/resolver"
	"github.com/syscod3/bambu-profile-manager/internal/storage"
)

type Service struct {
	Repo     storage.Repository
	Adapter  *bambuadapter.LocalAdapter
	Detector reconcile.ObservationDetector
	NewID    func() (string, error) // injected so tests don't need real UUIDs
}

// ExportBundle resolves leafName against set and writes a .profilepack to w,
// recording it as the profile's next revision in storage.
func (s *Service) ExportBundle(ctx context.Context, w io.Writer, set resolver.Set, leafName string) (domain.Profile, error) {
	p, err := s.Repo.Profiles().GetByName(ctx, leafName)
	if errors.Is(err, storage.ErrNotFound) {
		p, err = s.Repo.Profiles().Create(ctx, leafName)
	}
	if err != nil {
		return domain.Profile{}, fmt.Errorf("service: export: profile: %w", err)
	}

	revision := 1
	if latest, err := s.Repo.Versions().Latest(ctx, p.ID); err == nil {
		revision = latest.Revision + 1
	} else if !errors.Is(err, storage.ErrNotFound) {
		return domain.Profile{}, fmt.Errorf("service: export: latest version: %w", err)
	}

	var buf bytes.Buffer
	if err := bundle.Export(&buf, set, leafName, p.ID, revision); err != nil {
		return domain.Profile{}, fmt.Errorf("service: export: %w", err)
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return domain.Profile{}, fmt.Errorf("service: export: write: %w", err)
	}
	return p, nil
}

// ImportBundle re-resolves a .profilepack (see bundle.Import's guarantees)
// and records it as an immutable ProfileVersion.
func (s *Service) ImportBundle(ctx context.Context, r *zip.Reader) (domain.Profile, domain.ProfileVersion, error) {
	imported, err := bundle.Import(r)
	if err != nil {
		return domain.Profile{}, domain.ProfileVersion{}, fmt.Errorf("service: import: %w", err)
	}

	p, err := s.Repo.Profiles().GetByName(ctx, imported.Manifest.LeafName)
	if errors.Is(err, storage.ErrNotFound) {
		p, err = s.Repo.Profiles().Create(ctx, imported.Manifest.LeafName)
	}
	if err != nil {
		return domain.Profile{}, domain.ProfileVersion{}, fmt.Errorf("service: import: profile: %w", err)
	}

	sourceJSON, err := jsonMarshal(imported.Leaf.Fields)
	if err != nil {
		return domain.Profile{}, domain.ProfileVersion{}, err
	}
	resolvedJSON, err := jsonMarshal(imported.Effective.Fields)
	if err != nil {
		return domain.Profile{}, domain.ProfileVersion{}, err
	}

	v, err := s.Repo.Versions().Create(ctx, domain.ProfileVersion{
		ProfileID:    p.ID,
		Revision:     imported.Manifest.Revision,
		SourceJSON:   sourceJSON,
		ResolvedJSON: resolvedJSON,
		SemanticHash: imported.Manifest.SemanticHash,
	})
	if err != nil {
		return domain.Profile{}, domain.ProfileVersion{}, fmt.Errorf("service: import: version: %w", err)
	}
	return p, v, nil
}

// PublishResult is the outcome of RebindAndPublish.
type PublishResult struct {
	Rebind     *rebind.Result
	Deployment *reconcile.Deployment
}

// RebindAndPublish runs the full design.md §22 acceptance-test sequence
// after import: rebind -> validate -> stage -> publish -> (caller confirms
// Studio was reopened) -> observe -> verify -> advance the state machine.
// It refuses to advance past what it can actually prove, per decisions.md
// #6: it stops at STAGED/INSTALLED_LOCALLY if rebind was ambiguous or
// publish failed, and stops at SYNC_OBSERVED (never ACTIVE) unless the
// observed semantic hash truly matches.
//
// beforeInfo/afterInfo are the .info companion snapshots the caller reads
// before staging and after the user has reopened Bambu Studio — this
// function does not itself wait for a human, since that can't happen
// unattended (decisions.md #4).
func (s *Service) RebindAndPublish(
	ctx context.Context,
	sourceSet, targetSet resolver.Set,
	leaf *domain.RawProfile,
	targetParentCandidates []string,
	profileID string,
	revision int,
	beforeInfo, afterInfo reconcile.InfoFields,
) (*PublishResult, error) {
	id, err := s.NewID()
	if err != nil {
		return nil, fmt.Errorf("service: new deployment id: %w", err)
	}
	dep := reconcile.New(id, profileID, revision)
	result := &PublishResult{Deployment: dep}

	rb, err := rebind.Rebind(sourceSet, targetSet, leaf, targetParentCandidates)
	if err != nil {
		return result, fmt.Errorf("service: rebind: %w", err)
	}
	result.Rebind = rb
	if rb.Strategy == rebind.StrategyManualRequired {
		// dep never left DRAFT (rebind couldn't even produce a candidate) —
		// nothing worth persisting yet, unlike publishFlow's states.
		return result, fmt.Errorf("service: rebind: ambiguous target parent, candidates=%v: manual selection required", rb.Candidates)
	}

	return s.publishFlow(ctx, dep, result, rb.TargetProfile, beforeInfo, afterInfo)
}

// Rollback finds profileID's most recent Deployment that reached ACTIVE and
// republishes that revision's stored resolved profile, going through the
// same publish/verify machinery as a normal publish — a rollback still has
// to prove Studio picked it up, not just write a file (design.md §4,
// decisions.md #6).
func (s *Service) Rollback(ctx context.Context, profileID string, beforeInfo, afterInfo reconcile.InfoFields) (*PublishResult, error) {
	deployments, err := s.Repo.Deployments().ListByProfile(ctx, profileID)
	if err != nil {
		return nil, fmt.Errorf("service: rollback: list deployments: %w", err)
	}
	if len(deployments) == 0 {
		return nil, fmt.Errorf("service: rollback: no deployments found for profile %s", profileID)
	}

	var lastGood *reconcile.Deployment
	for i := len(deployments) - 1; i >= 0; i-- {
		if deployments[i].State == reconcile.StateActive {
			d := deployments[i]
			lastGood = &d
			break
		}
	}
	if lastGood == nil {
		return nil, fmt.Errorf("service: rollback: no previously ACTIVE deployment found for profile %s", profileID)
	}

	versions, err := s.Repo.Versions().List(ctx, profileID)
	if err != nil {
		return nil, fmt.Errorf("service: rollback: list versions: %w", err)
	}
	var resolvedJSON []byte
	for _, v := range versions {
		if v.Revision == lastGood.Revision {
			resolvedJSON = v.ResolvedJSON
			break
		}
	}
	if resolvedJSON == nil {
		return nil, fmt.Errorf("service: rollback: no stored version for profile %s revision %d", profileID, lastGood.Revision)
	}
	var fields map[string]any
	if err := json.Unmarshal(resolvedJSON, &fields); err != nil {
		return nil, fmt.Errorf("service: rollback: decode resolved json: %w", err)
	}

	p, err := s.Repo.Profiles().Get(ctx, profileID)
	if err != nil {
		return nil, fmt.Errorf("service: rollback: profile: %w", err)
	}
	targetProfile := &domain.RawProfile{Name: p.Name, Fields: fields}

	id, err := s.NewID()
	if err != nil {
		return nil, fmt.Errorf("service: rollback: new deployment id: %w", err)
	}
	newRevision := deployments[len(deployments)-1].Revision + 1
	dep := reconcile.New(id, profileID, newRevision)
	result := &PublishResult{Deployment: dep}

	return s.publishFlow(ctx, dep, result, targetProfile, beforeInfo, afterInfo)
}

// publishFlow is the shared tail of RebindAndPublish and Rollback: validate
// -> stage -> publish -> (caller confirms Studio was reopened) -> observe
// -> verify -> advance the state machine. It never advances further than it
// can actually prove (decisions.md #6): it stops at INSTALLED_LOCALLY if
// recognition hasn't been observed yet, and stops at SYNC_OBSERVED (never
// ACTIVE) unless the observed semantic hash truly matches.
func (s *Service) publishFlow(
	ctx context.Context,
	dep *reconcile.Deployment,
	result *PublishResult,
	targetProfile *domain.RawProfile,
	beforeInfo, afterInfo reconcile.InfoFields,
) (*PublishResult, error) {
	defer func() {
		if saveErr := s.Repo.Deployments().Save(ctx, *dep); saveErr != nil {
			fmt.Fprintf(os.Stderr, "service: save deployment %s: %v\n", dep.ID, saveErr)
		}
	}()

	if err := s.Adapter.Validate(ctx, targetProfile); err != nil {
		_ = dep.Advance(reconcile.StateDependencyMissing, err.Error())
		return result, fmt.Errorf("service: validate: %w", err)
	}
	if err := dep.Advance(reconcile.StateValidated, "candidate is valid"); err != nil {
		return result, err
	}

	// Record what's about to be published as this deployment's revision, so
	// Rollback can find and republish it later. targetProfile.Fields is
	// exactly what Adapter.Publish will write to disk, so it's used as both
	// SourceJSON and ResolvedJSON here — there's no separate "flattened"
	// form beyond what's actually live.
	fieldsJSON, err := jsonMarshal(targetProfile.Fields)
	if err != nil {
		return result, err
	}
	hash, err := hashOf(targetProfile.Fields)
	if err != nil {
		return result, err
	}
	if _, err := s.Repo.Versions().Create(ctx, domain.ProfileVersion{
		ProfileID:    dep.ProfileID,
		Revision:     dep.Revision,
		SourceJSON:   fieldsJSON,
		ResolvedJSON: fieldsJSON,
		SemanticHash: hash,
	}); err != nil {
		return result, fmt.Errorf("service: record version: %w", err)
	}

	staged, err := s.Adapter.Stage(ctx, targetProfile)
	if err != nil {
		return result, fmt.Errorf("service: stage: %w", err)
	}
	if err := dep.Advance(reconcile.StateStaged, staged); err != nil {
		return result, err
	}

	published, err := s.Adapter.Publish(ctx, staged)
	if err != nil {
		if errors.Is(err, bambuadapter.ErrStudioRunning) {
			_ = dep.Advance(reconcile.StateSyncTimeout, "Bambu Studio was running at publish time")
		} else {
			_ = dep.Advance(reconcile.StateRejectedByStudio, err.Error())
		}
		return result, fmt.Errorf("service: publish: %w", err)
	}
	if err := dep.Advance(reconcile.StateInstalledLocally, published); err != nil {
		return result, err
	}

	if !s.Detector.Observed(beforeInfo, afterInfo) {
		// Not yet recognized — caller should retry Observed later; this is
		// not a failure state, just "hasn't happened yet".
		return result, nil
	}
	if err := dep.Advance(reconcile.StateObservedByStudio, "recognition detector fired"); err != nil {
		return result, err
	}

	obs, err := s.Adapter.Observe(ctx, published)
	if err != nil {
		_ = dep.Advance(reconcile.StateCloudModified, err.Error())
		return result, fmt.Errorf("service: observe: %w", err)
	}
	if err := dep.Advance(reconcile.StateSyncObserved, "post-publish re-read succeeded"); err != nil {
		return result, err
	}

	expectedHash, err := hashOf(targetProfile.Fields)
	if err != nil {
		return result, err
	}
	verification, err := bambuadapter.Verify(obs.Fields, expectedHash)
	if err != nil {
		return result, fmt.Errorf("service: verify: %w", err)
	}
	if !verification.Match {
		_ = dep.Advance(reconcile.StateSemanticMismatch, fmt.Sprintf("expected=%s observed=%s", verification.ExpectedHash, verification.ObservedHash))
		return result, fmt.Errorf("service: verify: semantic mismatch: expected=%s observed=%s", verification.ExpectedHash, verification.ObservedHash)
	}
	if err := dep.Advance(reconcile.StateRoundTripVerified, "semantic hash matched"); err != nil {
		return result, err
	}
	if err := dep.Advance(reconcile.StateActive, "round-trip verified"); err != nil {
		return result, err
	}
	return result, nil
}
