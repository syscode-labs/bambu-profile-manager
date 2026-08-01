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
	"path/filepath"

	"github.com/syscod3/bambu-profile-manager/internal/backup"
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

	// BackupsDir, if set, gets a point-in-time snapshot of Adapter.Dir
	// (backup.Take) before every publish attempt reaches Adapter.Publish.
	// This is the safety net that replaces a manual export/review step: no
	// separate review checkpoint, but every publish is undoable via
	// backup.Restore. Empty means no snapshot is taken (e.g. in tests that
	// don't care about it).
	BackupsDir string
}

// nextRevision returns the revision number a new ProfileVersion for
// profileID should use: 1 if none exist yet, otherwise the latest stored
// revision + 1. Computing this fresh (rather than trusting a caller- or
// bundle-supplied number) is what makes retries and re-exports safe: if
// nothing was actually persisted since the last call, the same number comes
// back, instead of drifting ahead or colliding with profile_versions'
// UNIQUE(profile_id, revision) constraint.
func (s *Service) nextRevision(ctx context.Context, profileID string) (int, error) {
	latest, err := s.Repo.Versions().Latest(ctx, profileID)
	if errors.Is(err, storage.ErrNotFound) {
		return 1, nil
	}
	if err != nil {
		return 0, fmt.Errorf("service: next revision: %w", err)
	}
	return latest.Revision + 1, nil
}

// ExportBundle resolves leafName against set and writes a .profilepack to w,
// stamping it with the profile's next revision number. Exporting does not
// itself persist a ProfileVersion — that number is only reserved for real
// once something (an import, a publish) actually writes it.
func (s *Service) ExportBundle(ctx context.Context, w io.Writer, set resolver.Set, leafName string) (domain.Profile, error) {
	p, err := s.Repo.Profiles().GetByName(ctx, leafName)
	if errors.Is(err, storage.ErrNotFound) {
		p, err = s.Repo.Profiles().Create(ctx, leafName)
	}
	if err != nil {
		return domain.Profile{}, fmt.Errorf("service: export: profile: %w", err)
	}

	revision, err := s.nextRevision(ctx, p.ID)
	if err != nil {
		return domain.Profile{}, fmt.Errorf("service: export: %w", err)
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

	// Assign the revision at import time rather than trusting
	// imported.Manifest.Revision: two exports taken before either is
	// imported both stamp the same "next" number (ExportBundle doesn't
	// reserve it), and importing the same bundle twice must produce a new
	// revision, not a UNIQUE(profile_id, revision) constraint error.
	revision, err := s.nextRevision(ctx, p.ID)
	if err != nil {
		return domain.Profile{}, domain.ProfileVersion{}, fmt.Errorf("service: import: %w", err)
	}

	v, err := s.Repo.Versions().Create(ctx, domain.ProfileVersion{
		ProfileID:    p.ID,
		Revision:     revision,
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
	Rebind        *rebind.Result
	Deployment    *reconcile.Deployment
	TargetProfile *domain.RawProfile // what was staged/published, for a later CheckRecognition call
	PublishedPath string              // set once Adapter.Publish succeeds
	Snapshot      string              // backup.Snapshot.Name taken just before publish, if BackupsDir was set
}

// RebindAndPublish runs rebind -> validate -> stage -> publish, then checks
// recognition immediately using whatever beforeInfo/afterInfo the caller
// already has (design.md §22's sequence, end to end, when both snapshots
// are available up front — e.g. in tests via reconcile.ManualDetector). For
// a real Bambu Studio round trip, recognition can't be checked in the same
// call: Studio must be closed to publish (decisions.md #4) and open to be
// recognized, so afterInfo doesn't exist yet when this returns. Pass the
// zero InfoFields{} for afterInfo in that case, then call CheckRecognition
// once the user has reopened Studio.
//
// targetName overrides the rebound profile's name (which otherwise defaults
// to leaf's own name — see internal/rebind). Passing a name distinct from
// leaf.Name publishes a new profile alongside the original instead of
// overwriting it in place; pass "" to keep rebind's default.
//
// The revision to publish as is computed internally via nextRevision, not
// caller-supplied: a caller retrying after a failed publish (e.g. Bambu
// Studio was open — decisions.md #4's expected "refuses, close Studio,
// retry" flow) must land on the same revision number as the failed attempt,
// since nothing was actually persisted for it yet. A caller-supplied number
// that doesn't advance with reality risks a UNIQUE(profile_id, revision)
// conflict on retry, or the reverse: two independent callers colliding on
// the same number they both computed by hand.
func (s *Service) RebindAndPublish(
	ctx context.Context,
	sourceSet, targetSet resolver.Set,
	leaf *domain.RawProfile,
	targetParentCandidates []string,
	profileID string,
	targetName string,
	beforeInfo, afterInfo reconcile.InfoFields,
) (*PublishResult, error) {
	id, err := s.NewID()
	if err != nil {
		return nil, fmt.Errorf("service: new deployment id: %w", err)
	}
	revision, err := s.nextRevision(ctx, profileID)
	if err != nil {
		return nil, fmt.Errorf("service: rebind and publish: %w", err)
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
	if targetName != "" && targetName != rb.TargetProfile.Name {
		rb.TargetProfile.Name = targetName
		rb.TargetProfile.Fields = cloneWithName(rb.TargetProfile.Fields, targetName)
	}

	return s.publishFlow(ctx, dep, result, rb.TargetProfile, beforeInfo, afterInfo)
}

func cloneWithName(fields map[string]any, name string) map[string]any {
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		out[k] = v
	}
	out["name"] = name
	// filament_settings_id is observed (findings.md) to echo the profile's
	// own name in a single-element array; keep it consistent with the
	// renamed profile rather than pointing at the old name.
	if _, ok := out["filament_settings_id"]; ok {
		out["filament_settings_id"] = []any{name}
	}
	return out
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
	newRevision, err := s.nextRevision(ctx, profileID)
	if err != nil {
		return nil, fmt.Errorf("service: rollback: %w", err)
	}
	dep := reconcile.New(id, profileID, newRevision)
	result := &PublishResult{Deployment: dep}

	return s.publishFlow(ctx, dep, result, targetProfile, beforeInfo, afterInfo)
}

// publishFlow is the shared tail of RebindAndPublish and Rollback: validate
// -> stage -> publish, then immediately attempt recognition/verify using
// whatever beforeInfo/afterInfo the caller already has. It never advances
// further than it can actually prove (decisions.md #6): it stops at
// STAGED if publish failed, at INSTALLED_LOCALLY if recognition hasn't been
// observed yet (the normal case for a real Bambu Studio round trip — see
// CheckRecognition), and at SYNC_OBSERVED (never ACTIVE) unless the
// observed semantic hash truly matches.
func (s *Service) publishFlow(
	ctx context.Context,
	dep *reconcile.Deployment,
	result *PublishResult,
	targetProfile *domain.RawProfile,
	beforeInfo, afterInfo reconcile.InfoFields,
) (*PublishResult, error) {
	result.TargetProfile = targetProfile
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

	staged, err := s.Adapter.Stage(ctx, targetProfile)
	if err != nil {
		return result, fmt.Errorf("service: stage: %w", err)
	}
	if err := dep.Advance(reconcile.StateStaged, staged); err != nil {
		return result, err
	}

	if s.BackupsDir != "" {
		snap, err := backup.Take(s.Adapter.Dir, s.BackupsDir)
		if err != nil {
			return result, fmt.Errorf("service: backup before publish: %w", err)
		}
		result.Snapshot = snap.Name
	}

	published, err := s.Adapter.Publish(ctx, staged)
	if err != nil {
		if errors.Is(err, bambuadapter.ErrStudioRunning) {
			// Not a failure state — SYNC_TIMEOUT/REJECTED_BY_STUDIO in the
			// transition table only make sense once INSTALLED_LOCALLY has
			// actually been reached (design.md §16), and this deployment
			// hasn't gotten that far. decisions.md #4's expected flow is
			// "close Studio, retry": dep just stays at STAGED, and a retry
			// lands on this same revision since nothing was persisted yet.
			return result, fmt.Errorf("service: publish: %w", err)
		}
		if advErr := dep.Advance(reconcile.StateRejectedByStudio, err.Error()); advErr != nil {
			return result, fmt.Errorf("service: publish: %w (and could not record REJECTED_BY_STUDIO: %v)", err, advErr)
		}
		return result, fmt.Errorf("service: publish: %w", err)
	}
	result.PublishedPath = published
	if err := dep.Advance(reconcile.StateInstalledLocally, published); err != nil {
		return result, err
	}

	// Only now record this revision as actually published, so Rollback can
	// find and republish it later. targetProfile.Fields is exactly what
	// Adapter.Publish just wrote to disk, so it's used as both SourceJSON
	// and ResolvedJSON here — there's no separate "flattened" form beyond
	// what's actually live. Writing this before Publish succeeded would
	// reserve dep.Revision even on a failed attempt (e.g. Studio was
	// running), permanently stranding a retry on a UNIQUE constraint.
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

	return s.checkRecognitionAndVerify(ctx, dep, result, targetProfile, published, beforeInfo, afterInfo)
}

// CheckRecognition resumes a Deployment that's sitting at INSTALLED_LOCALLY
// (from a prior RebindAndPublish/Rollback call where recognition hadn't
// happened yet) and attempts recognition/verify again with a fresh
// afterInfo snapshot. It does not re-stage or re-publish — nothing about
// the already-installed file changes here, only whether the state machine
// can now prove Studio picked it up.
func (s *Service) CheckRecognition(
	ctx context.Context,
	deploymentID string,
	targetProfile *domain.RawProfile,
	publishedPath string,
	beforeInfo, afterInfo reconcile.InfoFields,
) (*PublishResult, error) {
	dep, err := s.Repo.Deployments().Get(ctx, deploymentID)
	if err != nil {
		return nil, fmt.Errorf("service: check recognition: load deployment: %w", err)
	}
	if dep.State != reconcile.StateInstalledLocally {
		return nil, fmt.Errorf("service: check recognition: deployment %s is in state %s, want INSTALLED_LOCALLY", deploymentID, dep.State)
	}
	result := &PublishResult{Deployment: &dep, TargetProfile: targetProfile, PublishedPath: publishedPath}
	return s.checkRecognitionAndVerify(ctx, &dep, result, targetProfile, publishedPath, beforeInfo, afterInfo)
}

// checkRecognitionAndVerify is the Detector-onward tail shared by
// publishFlow (checking immediately) and CheckRecognition (checking later,
// once the user has reopened Bambu Studio — decisions.md #4).
func (s *Service) checkRecognitionAndVerify(
	ctx context.Context,
	dep *reconcile.Deployment,
	result *PublishResult,
	targetProfile *domain.RawProfile,
	published string,
	beforeInfo, afterInfo reconcile.InfoFields,
) (*PublishResult, error) {
	defer func() {
		if saveErr := s.Repo.Deployments().Save(ctx, *dep); saveErr != nil {
			fmt.Fprintf(os.Stderr, "service: save deployment %s: %v\n", dep.ID, saveErr)
		}
	}()

	if !s.Detector.Observed(beforeInfo, afterInfo) {
		// Not yet recognized — caller should call CheckRecognition again
		// later; this is not a failure state, just "hasn't happened yet".
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

// ListBackups returns every point-in-time snapshot taken before a publish,
// newest first.
func (s *Service) ListBackups() ([]backup.Snapshot, error) {
	if s.BackupsDir == "" {
		return nil, nil
	}
	return backup.List(s.BackupsDir)
}

// RestoreBackup restores a named snapshot (backup.Snapshot.Name, as
// returned by ListBackups) back into the live Bambu Studio directory.
// Non-destructive: overwrites files present in the snapshot, never deletes
// files added since (backup.Restore).
func (s *Service) RestoreBackup(name string) error {
	if s.BackupsDir == "" {
		return fmt.Errorf("service: restore backup: no BackupsDir configured")
	}
	return backup.Restore(filepath.Join(s.BackupsDir, name), s.Adapter.Dir)
}
