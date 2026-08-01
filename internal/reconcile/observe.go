package reconcile

import (
	"bufio"
	"bytes"
	"strings"
)

// InfoFields is a parsed .info companion file. Format confirmed empirically
// against real Bambu Studio files (findings.md): plain `key = value` lines,
// not JSON — e.g. user_id, setting_id, base_id, updated_time.
type InfoFields map[string]string

// ParseInfo parses a .info file's bytes. Malformed lines are skipped rather
// than erroring — this format is undocumented and only partially understood
// (findings.md), so being permissive here is deliberate.
func ParseInfo(b []byte) InfoFields {
	out := InfoFields{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

// ObservationDetector decides whether Bambu Studio has recognized a
// published profile, i.e. whether OBSERVED_BY_STUDIO has been reached.
type ObservationDetector interface {
	Observed(before, after InfoFields) bool
}

// RewriteDetector implements decisions.md #5: infer recognition from
// Studio's own rewrite of the .info file — a bumped updated_time, or
// setting_id going from unset to set. FLAGGED UNVERIFIED (decisions.md #5):
// this behavior hasn't been empirically confirmed against a real reopen of
// Bambu Studio; validate during the Phase 0/3 spike. If it doesn't hold up,
// swap in ManualDetector below without touching callers — both satisfy the
// same ObservationDetector interface.
type RewriteDetector struct{}

func (RewriteDetector) Observed(before, after InfoFields) bool {
	if u, ok := after["updated_time"]; ok && u != "" && u != before["updated_time"] {
		return true
	}
	if before["setting_id"] == "" && after["setting_id"] != "" {
		return true
	}
	return false
}

// ManualDetector is decisions.md #5's stated fallback: the user explicitly
// confirms recognition in the UI (e.g. after visually checking Studio) when
// RewriteDetector's inference can't be trusted.
type ManualDetector struct{ Confirmed bool }

func (m ManualDetector) Observed(before, after InfoFields) bool { return m.Confirmed }
