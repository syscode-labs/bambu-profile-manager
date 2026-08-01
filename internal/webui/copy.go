package webui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/syscod3/bambu-profile-manager/internal/bambuadapter"
	"github.com/syscod3/bambu-profile-manager/internal/domain"
	"github.com/syscod3/bambu-profile-manager/internal/rebind"
	"github.com/syscod3/bambu-profile-manager/internal/reconcile"
	"github.com/syscod3/bambu-profile-manager/internal/resolver"
	"github.com/syscod3/bambu-profile-manager/internal/service"
	"github.com/syscod3/bambu-profile-manager/internal/storage"
)

// printerModelShortNames maps a machine profile's authoritative
// "printer_model" field (e.g. "Bambu Lab X1 Carbon", confirmed by resolving
// a real machine profile's inheritance chain) to the short code Bambu uses
// inside FILAMENT profile names (e.g. "Bambu ABS @BBL X1C" — that's "X1C",
// not "X1CARBON"). Most models reduce cleanly by stripping "Bambu Lab " and
// spaces; these three don't and need an explicit exception.
var printerModelShortNames = map[string]string{
	"Bambu Lab X1 Carbon": "X1C",
	"Bambu Lab A1 mini":   "A1M",
	"Bambu Lab H2D Pro":   "H2DP",
}

func shortPrinterToken(printerModel, nozzleDiameter string) string {
	short, ok := printerModelShortNames[printerModel]
	if !ok {
		short = strings.ReplaceAll(strings.TrimPrefix(printerModel, "Bambu Lab "), " ", "")
	}
	if nozzleDiameter == "" {
		return short
	}
	return short + " " + nozzleDiameter
}

// printerOption is one selectable entry in the "target printer" dropdown:
// Name is the real machine profile's own name (what the user actually
// called their printer, e.g. "Bambu Lab X1 Carbon 0.4 nozzle - Obsidian
// HF"), Token is the short code passed to rebind.FindCandidateParents for
// filament profiles, Canonical is the exact "<printer_model> <nozzle>
// nozzle" string Bambu's own compatible_printers field uses — needed for
// process profiles, which are matched by that field, not by name (see
// profileKind.MatchByCompatiblePrinters).
type printerOption struct{ Name, Token, Canonical string }

// discoverRealPrinters lists selectable printer options limited to MODELS
// the user actually owns — machineSet must include the full user+system
// tree so inheritance resolves, but every Bambu-made model exists as a
// system template regardless of what the user owns (found live: a profile
// copied to "X1" published fine but never appeared in Studio, because the
// user has no X1 — only X1 Carbon — configured).
//
// Ownership is at the MODEL level, not the exact nozzle profile: owning an
// X1 Carbon in any nozzle size means every nozzle size Bambu offers for an
// X1 Carbon is a real, selectable option in Studio too — Studio's printer
// picker isn't gated per nozzle the way filament profiles are gated on
// having a "User" copy (corrected after an earlier, too-strict pass
// required a "User" entry for every single nozzle size). System profiles
// have `"from": "system"`, the user's own have `"from": "User"`, confirmed
// on real files (findings.md) — used only to establish which MODELS are
// owned; every matching nozzle variant (user or system) is then included.
func discoverRealPrinters(machineSet resolver.Set) []printerOption {
	ownedModels := map[string]bool{}
	for _, leaf := range machineSet {
		from, _ := leaf.Fields["from"].(string)
		if from != "User" {
			continue
		}
		effective, _, err := resolver.Resolve(machineSet, leaf)
		if err != nil {
			continue
		}
		if model, _ := effective.Fields["printer_model"].(string); model != "" {
			ownedModels[model] = true
		}
	}

	var out []printerOption
	for name, leaf := range machineSet {
		effective, _, err := resolver.Resolve(machineSet, leaf)
		if err != nil {
			continue
		}
		model, _ := effective.Fields["printer_model"].(string)
		if model == "" || !ownedModels[model] {
			continue
		}
		nozzle := ""
		if arr, ok := effective.Fields["nozzle_diameter"].([]any); ok && len(arr) > 0 {
			nozzle = fmt.Sprint(arr[0])
		}
		out = append(out, printerOption{
			Name: name, Token: shortPrinterToken(model, nozzle),
			Canonical: strings.TrimSpace(model + " " + nozzle + " nozzle"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// studioWarning checks whether Bambu Studio looks like it's running right
// now (the same best-effort pgrep check bambuadapter.Publish enforces) and
// returns a callout banner if so, or if the check itself failed (decisions
// #4: an unknown result must not be silently treated as "not running").
// Empty when Studio is confirmed closed. svc is explicit (not always s.Svc)
// since the process copy flow checks its own Adapter/Service, not
// filament's.
func studioWarning(svc *service.Service) template.HTML {
	if svc == nil || svc.Adapter == nil {
		return ""
	}
	checker := svc.Adapter.IsStudioRunning
	if checker == nil {
		checker = bambuadapter.PgrepStudioRunning
	}
	running, err := checker()
	if err != nil {
		return template.HTML(`<div class="flex items-start gap-3 bg-amber-50 border border-amber-200 text-amber-800 rounded-xl px-4 py-3 text-sm">` +
			`<span>&#9888;</span><div><p class="font-medium">Could not determine whether Bambu Studio is running</p>` +
			`<p class="text-amber-700/80">` + template.HTMLEscapeString(err.Error()) + `. Publishing will refuse on its own if it turns out to be open.</p></div></div>`)
	}
	if running {
		return template.HTML(`<div class="flex items-start gap-3 bg-red-50 border border-red-200 text-red-800 rounded-xl px-4 py-3 text-sm">` +
			`<span>&#9888;</span><div><p class="font-medium">Bambu Studio appears to be running</p>` +
			`<p class="text-red-700/80">Publishing requires it closed first. Close Studio, then continue.</p></div></div>`)
	}
	return ""
}

// handleStudioStatus re-checks whether Bambu Studio is running and returns
// the same warning-banner fragment studioWarning renders server-side on
// page load — polled client-side (see shell.go's script) so the banner
// reflects Studio actually being closed/reopened without a page reload.
// The underlying check (bambuadapter.PgrepStudioRunning, in production) is
// one OS-level process check regardless of which copy flow the page is on,
// so this always uses the filament Service; it would give an identical
// answer via ProcessSvc.
func (s *Server) handleStudioStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, string(studioWarning(s.Svc)))
}

var copyFormTmpl = template.Must(template.New("copyForm").Parse(`
<div class="inline-flex rounded-full bg-zinc-100 p-1 text-sm font-medium">
  <a href="/copy" class="px-4 py-1.5 rounded-full transition {{if eq .Kind "filament"}}bg-zinc-900 text-white shadow-sm{{else}}text-zinc-500 hover:text-zinc-800{{end}}">Filament</a>
  <a href="/copy/process" class="px-4 py-1.5 rounded-full transition {{if eq .Kind "process"}}bg-zinc-900 text-white shadow-sm{{else}}text-zinc-500 hover:text-zinc-800{{end}}">Process (print)</a>
</div>
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-4">
  <div class="flex items-center gap-2 text-sm font-medium text-zinc-500">
    <span class="w-5 h-5 rounded-full bg-zinc-900 text-white text-xs flex items-center justify-center shrink-0">1</span>
    Choose a profile and target printer
  </div>
  {{if eq .Kind "filament"}}
  <p class="text-xs text-zinc-500 -mt-2">
    Pick a filament you already have, and the printer you want it usable under. bambupm looks for a parent profile
    that matches both the same material (ABS, PLA, ...) and your target printer &mdash; the same lookup Bambu Studio
    itself would need, done for you. Your settings (color, vendor, temps) carry over either way.
  </p>
  {{else}}
  <p class="text-xs text-zinc-500 -mt-2">
    Pick a process (print) profile you already have, and the printer you want it usable under. Process profiles work
    differently from filament: Bambu often already lists your target printer as compatible on the profile you have
    (many printers share tuning) &mdash; bambupm checks that first and tells you if there's nothing to copy at all.
  </p>
  {{end}}
<form method="post" action="{{if eq .Kind "process"}}/copy/process/preview{{else}}/copy/preview{{end}}" class="space-y-4">
  <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
    <label class="block min-w-0">
      <span class="text-xs font-medium text-zinc-500 mb-1 block">Profile</span>
      <div class="flex items-center gap-2 min-w-0">
        <div id="filament-preview-wrap" class="relative shrink-0">
          <button type="button" id="filament-preview-btn"
            class="w-8 h-8 rounded-lg border border-zinc-300 text-zinc-400 hover:text-zinc-700 hover:border-zinc-400 text-xs flex items-center justify-center"
            aria-label="Preview profile properties">&#9432;</button>
          <div id="filament-preview-popup" class="hidden absolute left-0 top-8 pt-2 z-20 w-96 max-h-[32rem] overflow-y-auto bg-white border border-zinc-200 rounded-xl shadow-lg p-4 text-xs"></div>
        </div>
        <select id="copy-profile-select" name="name" required class="flex-1 min-w-0 rounded-lg border border-zinc-300 px-3 py-2 text-sm focus:ring-2 focus:ring-emerald-500 focus:border-emerald-500 outline-none">
        {{range .Names}}<option value="{{.}}">{{.}}</option>{{end}}
        </select>
      </div>
    </label>
    <label class="block">
      <span class="text-xs font-medium text-zinc-500 mb-1 block">Target printer</span>
      <select name="printer_token" required class="w-full rounded-lg border border-zinc-300 px-3 py-2 text-sm focus:ring-2 focus:ring-emerald-500 focus:border-emerald-500 outline-none">
      {{range .Printers}}<option value="{{.Token}}|{{.Canonical}}">{{.Name}}</option>{{end}}
      </select>
      <span class="text-xs text-zinc-400 mt-1 block">Your actual printer profiles, resolved to their real model/nozzle.</span>
    </label>
  </div>
  <button type="submit" class="px-4 py-2 rounded-lg bg-zinc-900 text-white text-sm font-medium hover:bg-zinc-800 transition">Find match</button>
</form>
</section>
<script>
(function() {
  var select = document.getElementById('copy-profile-select');
  var wrap = document.getElementById('filament-preview-wrap');
  var btn = document.getElementById('filament-preview-btn');
  var popup = document.getElementById('filament-preview-popup');
  if (!select || !wrap || !btn || !popup) return;
  var previewKind = {{if eq .Kind "process"}}'&kind=process'{{else}}''{{end}};
  var cache = {};
  var hideTimer = null;
  function load(name) {
    if (Object.prototype.hasOwnProperty.call(cache, name)) {
      popup.innerHTML = cache[name];
      return;
    }
    popup.innerHTML = '<p class="text-zinc-400">Loading…</p>';
    fetch('/api/profile-preview?name=' + encodeURIComponent(name) + previewKind)
      .then(function(r) { return r.ok ? r.text() : Promise.reject(r.status); })
      .then(function(html) { cache[name] = html; popup.innerHTML = html; })
      .catch(function() { popup.innerHTML = '<p class="text-red-500">Could not load preview.</p>'; });
  }
  function show() {
    if (hideTimer) { clearTimeout(hideTimer); hideTimer = null; }
    if (select.value) load(select.value);
    popup.classList.remove('hidden');
  }
  function scheduleHide() {
    if (hideTimer) clearTimeout(hideTimer);
    hideTimer = setTimeout(function() { popup.classList.add('hidden'); }, 300);
  }
  wrap.addEventListener('mouseenter', show);
  wrap.addEventListener('mouseleave', scheduleHide);
  btn.addEventListener('focus', show);
  btn.addEventListener('blur', scheduleHide);
})();
</script>
`))

func (s *Server) handleCopyForm(w http.ResponseWriter, r *http.Request) {
	s.renderCopyForm(w, r, s.filamentKind())
}

func (s *Server) handleCopyFormProcess(w http.ResponseWriter, r *http.Request) {
	kind, ok := s.processKind()
	if !ok {
		http.Error(w, "process profiles are not configured (serve without --process-user-dir)", http.StatusNotFound)
		return
	}
	s.renderCopyForm(w, r, kind)
}

func (s *Server) renderCopyForm(w http.ResponseWriter, r *http.Request, kind profileKind) {
	set, err := resolver.LoadDirs([]string{kind.UserDir})
	if err != nil {
		httpError(w, err)
		return
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)

	machineSet, err := resolver.LoadDirs(s.MachineDirs)
	if err != nil {
		httpError(w, err)
		return
	}
	printers := discoverRealPrinters(machineSet)

	data := struct {
		Kind     string
		Names    []string
		Printers []printerOption
	}{Kind: kind.Key, Names: names, Printers: printers}
	title := "Copy a filament profile to another printer"
	if kind.Key == "process" {
		title = "Copy a process (print) profile to another printer"
	}
	renderPage(w, copyFormTmpl, data, "Copy", title, "Rebind without touching dependency chains yourself.", "copy", studioWarning(kind.Svc), true)
}

var copyPreviewTmpl = template.Must(template.New("copyPreview").Parse(`
{{$startOverHref := "/copy"}}{{if eq .Kind "process"}}{{$startOverHref = "/copy/process"}}{{end}}
{{$publishAction := "/copy/publish"}}{{if eq .Kind "process"}}{{$publishAction = "/copy/process/publish"}}{{end}}
<p><a href="{{$startOverHref}}" class="text-sm text-zinc-500 hover:text-zinc-800">&larr; Start over</a></p>
{{if .AlreadyCompatible}}
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6">
  <div class="flex items-center gap-3 bg-emerald-50 border border-emerald-200 rounded-xl px-4 py-3">
    <svg class="w-5 h-5 text-emerald-600 shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/></svg>
    <p class="text-sm text-emerald-900">Nothing to copy &mdash; "{{.Name}}" already lists "{{.PrinterToken}}" as a compatible printer. Select it directly in Bambu Studio when that printer is active.</p>
  </div>
</section>
{{else if not .Candidates}}
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-4">
  <div class="flex items-start gap-3 bg-amber-50 border border-amber-200 text-amber-800 rounded-xl px-4 py-3 text-sm">
    <span>&#9888;</span>
    <div>
      <p class="font-medium">"{{.PrinterToken}}" isn't listed as compatible with any base profile in this family yet</p>
      {{if .FamilyCandidates}}
      <p class="text-amber-700/80">These {{len .FamilyCandidates}} profiles share the same family as the one you're copying, but none of them have "{{.PrinterToken}}" in their compatible-printer list &mdash; so bambupm can't verify the result will actually work there, and won't pick one for you. Open a candidate's diff to see what would change, then check its box to have bambupm add "{{.PrinterToken}}" to it and publish.</p>
      {{else}}
      <p class="text-amber-700/80">No profile in your library even shares this one's family &mdash; there's nothing safe to build from.</p>
      {{end}}
    </div>
  </div>
  {{$name := .Name}}{{$token := .PrinterToken}}{{$printer := .PrinterCanonical}}
  {{range .FamilyCandidates}}
  <form method="post" action="{{$publishAction}}" class="border border-amber-200 rounded-xl p-4 space-y-3">
    <input type="hidden" name="name" value="{{$name}}">
    <input type="hidden" name="parent" value="{{.Name}}">
    <input type="hidden" name="printer_canonical" value="{{$printer}}">
    <p class="text-sm font-medium">{{.Name}}</p>
    {{if .Diff}}
    <details class="text-xs">
      <summary class="cursor-pointer text-zinc-500 hover:text-zinc-800">{{len .Diff}} setting{{if ne (len .Diff) 1}}s{{end}} would change from your current profile</summary>
      <div class="mt-2 rounded-lg border border-zinc-100 divide-y divide-zinc-50">
      {{range .Diff}}<div class="flex justify-between gap-3 px-3 py-1.5"><span class="text-zinc-500">{{.Label}}</span><span class="font-mono text-right"><span class="text-zinc-400 line-through">{{.Source}}</span> &rarr; {{.Target}}</span></div>{{end}}
      </div>
    </details>
    {{else}}<p class="text-xs text-zinc-400">No settings would change &mdash; identical to your current profile.</p>{{end}}
    <label class="flex items-center gap-2 text-xs text-amber-800">
      <input type="checkbox" name="confirm_unverified" required class="shrink-0">
      Add "{{$token}}" to this profile and publish
    </label>
    <label class="block">
      <span class="text-xs font-medium text-zinc-500 mb-1 block">New profile name</span>
      <input type="text" name="confirm_name" value="{{$name}} @{{$token}}" required
        class="w-full rounded-lg border border-zinc-300 px-3 py-2 text-sm focus:ring-2 focus:ring-emerald-500 focus:border-emerald-500 outline-none">
    </label>
    <button type="submit" class="px-4 py-2 rounded-lg border border-amber-300 bg-amber-100 text-amber-900 text-sm font-medium hover:bg-amber-200 transition">Publish with this parent, add compatibility</button>
  </form>
  {{end}}
</section>
{{else if gt (len .Candidates) 1}}
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-4">
  <div class="flex items-center gap-2 text-sm font-medium text-zinc-500">
    <span class="w-5 h-5 rounded-full bg-zinc-900 text-white text-xs flex items-center justify-center shrink-0">2</span>
    Pick a parent
  </div>
  <p class="text-sm text-zinc-500">In Bambu Studio, every profile inherits its settings from a base "parent" profile &mdash; that's how "@P1S" or "@X1C" variants share most of their settings. {{len .Candidates}} base profiles match both this profile's family and "{{.PrinterToken}}", so this tool won't guess which one your copy should inherit from. Add more of the printer name (e.g. the nozzle size) on the previous step to narrow it to one, or just pick the correct base profile below.</p>
  {{$name := .Name}}{{$token := .PrinterToken}}{{$suggested := .SuggestedParent}}
  {{range .Candidates}}
  <form method="post" action="{{$publishAction}}" class="rounded-xl p-4 space-y-3 {{if and $suggested (eq . $suggested)}}border-2 border-emerald-300 bg-emerald-50/50{{else}}border border-zinc-200{{end}}">
    <input type="hidden" name="name" value="{{$name}}">
    <input type="hidden" name="parent" value="{{.}}">
    <p class="text-sm font-medium">{{.}}{{if and $suggested (eq . $suggested)}} <span class="text-xs font-medium px-2 py-0.5 rounded-full bg-emerald-600 text-white align-middle">Suggested &mdash; closest match</span>{{end}}</p>
    <label class="block">
      <span class="text-xs font-medium text-zinc-500 mb-1 block">New profile name</span>
      <input type="text" name="confirm_name" value="{{$name}} @{{$token}}" required
        class="w-full rounded-lg border border-zinc-300 px-3 py-2 text-sm focus:ring-2 focus:ring-emerald-500 focus:border-emerald-500 outline-none">
    </label>
    <button type="submit" class="px-4 py-2 rounded-lg bg-zinc-900 text-white text-sm font-medium hover:bg-zinc-800 transition">Publish with this parent</button>
  </form>
  {{end}}
</section>
{{else}}
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-4">
  <div class="flex items-center gap-2 text-sm font-medium text-zinc-500">
    <span class="w-5 h-5 rounded-full bg-zinc-900 text-white text-xs flex items-center justify-center shrink-0">2</span>
    Confirm and publish
  </div>
  <div class="flex items-center justify-between bg-emerald-50 border border-emerald-200 rounded-xl px-4 py-3">
    <div class="flex items-center gap-3">
      <svg class="w-5 h-5 text-emerald-600" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"/></svg>
      <div>
        <p class="text-sm font-medium text-emerald-900">{{index .Candidates 0}}</p>
        <p class="text-xs text-emerald-700">Single unambiguous match</p>
      </div>
    </div>
    <span class="text-xs font-medium px-2 py-1 rounded-full bg-emerald-600 text-white">auto-matched</span>
  </div>
  <form method="post" action="{{$publishAction}}" class="space-y-4">
    <input type="hidden" name="name" value="{{.Name}}">
    <input type="hidden" name="parent" value="{{index .Candidates 0}}">
    <label class="block">
      <span class="text-xs font-medium text-zinc-500 mb-1 block">New profile name</span>
      <input type="text" name="confirm_name" value="{{.Name}} @{{.PrinterToken}}" required
        class="w-full rounded-lg border border-zinc-300 px-3 py-2 text-sm focus:ring-2 focus:ring-emerald-500 focus:border-emerald-500 outline-none">
    </label>
    <button type="submit" class="px-5 py-2.5 rounded-lg bg-emerald-600 text-white text-sm font-semibold hover:bg-emerald-500 transition shadow-sm shadow-emerald-600/30">Publish</button>
  </form>
</section>
{{end}}
`))

func (s *Server) handleCopyPreview(w http.ResponseWriter, r *http.Request) {
	s.renderCopyPreview(w, r, s.filamentKind())
}

func (s *Server) handleCopyPreviewProcess(w http.ResponseWriter, r *http.Request) {
	kind, ok := s.processKind()
	if !ok {
		http.Error(w, "process profiles are not configured (serve without --process-user-dir)", http.StatusNotFound)
		return
	}
	s.renderCopyPreview(w, r, kind)
}

// splitPrinterTarget separates the copy form's composite select value
// ("<short token>|<canonical compatible_printers string>") back into its
// two halves. A value with no "|" (e.g. a test or script posting a bare
// token directly) falls back to using it for both, so filament's existing
// plain-token behavior is unaffected.
func splitPrinterTarget(raw string) (token, canonical string) {
	token, canonical, ok := strings.Cut(raw, "|")
	if !ok {
		return raw, raw
	}
	return token, canonical
}

// diffRow is one changed field shown on a family-fallback candidate card
// (see renderCopyPreview) — reuses compare.go's formatValue/prettyLabel so
// values read the same way here as everywhere else in the app.
type diffRow struct{ Label, Source, Target string }

type familyCandidate struct {
	Name string
	Diff []diffRow
}

func diffValueOrNotSet(key string, v any) string {
	if v == nil {
		return "(not set)"
	}
	return formatValue(key, v)
}

// familyCandidatesWithDiff computes, for each name FindCandidateParentsBySameFamily
// returned, what rebind.Rebind would actually change relative to leaf's
// current effective fields — so the "not verified compatible, pick anyway"
// UI can show a real diff instead of a bare name list (user asked to see
// this rather than just publish unverified/standalone).
// unresolvedDiffCount sorts a candidate whose diff couldn't be computed to
// the very end, rather than treating an error as "0 differences" (best).
const unresolvedDiffCount = 1 << 30

// rankCandidatesByCloseness orders candidates so the one closest to leaf's
// own effective settings comes first, and returns that top pick as the
// suggested one (empty if candidates is empty). "Closest" is two signals,
// applied in order: (1) same resolved layer_height as leaf — real gap found
// live: FindCandidateParentsByCompatiblePrinters can return every
// layer-height variant for a nozzle as equally "compatible", but a profile
// built at 0.10mm should rebind onto a 0.10mm base, not 0.06mm or 0.14mm;
// (2) within that, fewest fields rebind.Rebind would actually change — e.g.
// a nozzle's "Standard" and "High Quality" bases share the same layer
// height but differ in speed/quality tuning throughout, and the fewer
// changes a base requires, the more it resembles what leaf was built on.
func rankCandidatesByCloseness(set resolver.Set, leaf *domain.RawProfile, candidates []string) (string, []string) {
	var sourceLH any
	if sourceEff, _, err := resolver.Resolve(set, leaf); err == nil {
		sourceLH = sourceEff.Fields["layer_height"]
	}

	type scored struct {
		name            string
		sameLayerHeight bool
		diffCount       int
	}
	scores := make([]scored, len(candidates))
	for i, name := range candidates {
		sc := scored{name: name, diffCount: unresolvedDiffCount}
		if p, ok := set[name]; ok {
			if eff, _, err := resolver.Resolve(set, p); err == nil && sourceLH != nil {
				if lh, ok := eff.Fields["layer_height"]; ok && lh == sourceLH {
					sc.sameLayerHeight = true
				}
			}
			if rb, err := rebind.Rebind(set, set, leaf, []string{name}); err == nil {
				sc.diffCount = len(rb.Diff)
			}
		}
		scores[i] = sc
	}
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].sameLayerHeight != scores[j].sameLayerHeight {
			return scores[i].sameLayerHeight
		}
		return scores[i].diffCount < scores[j].diffCount
	})

	ranked := make([]string, len(scores))
	for i, s := range scores {
		ranked[i] = s.name
	}
	if len(ranked) == 0 {
		return "", ranked
	}
	return ranked[0], ranked
}

func familyCandidatesWithDiff(sourceSet, targetSet resolver.Set, leaf *domain.RawProfile, names []string) []familyCandidate {
	out := make([]familyCandidate, 0, len(names))
	for _, name := range names {
		rb, err := rebind.Rebind(sourceSet, targetSet, leaf, []string{name})
		if err != nil || rb.Strategy != rebind.StrategyMapToTargetParent {
			continue // shouldn't happen (name came from the same targetSet) — skip defensively
		}
		rows := make([]diffRow, 0, len(rb.Diff))
		for k, d := range rb.Diff {
			rows = append(rows, diffRow{Label: prettyLabel(k), Source: diffValueOrNotSet(k, d.Source), Target: diffValueOrNotSet(k, d.Target)})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Label < rows[j].Label })
		out = append(out, familyCandidate{Name: name, Diff: rows})
	}
	return out
}

func (s *Server) renderCopyPreview(w http.ResponseWriter, r *http.Request, kind profileKind) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "copy: parse form: "+err.Error(), http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	raw := r.FormValue("printer_token")
	if name == "" || raw == "" {
		http.Error(w, "copy: name and printer_token are required", http.StatusBadRequest)
		return
	}
	token, canonical := splitPrinterTarget(raw)

	set, err := resolver.LoadDirs(append([]string{kind.UserDir}, kind.SystemDirs...))
	if err != nil {
		httpError(w, err)
		return
	}
	leaf, ok := set[name]
	if !ok {
		http.Error(w, fmt.Sprintf("copy: %q not found", name), http.StatusNotFound)
		return
	}

	var alreadyCompatible bool
	var candidates []string
	if kind.MatchByCompatiblePrinters {
		alreadyCompatible, err = rebind.IsAlreadyCompatible(set, leaf, canonical)
		if err != nil {
			httpError(w, err)
			return
		}
		if !alreadyCompatible {
			candidates, err = rebind.FindCandidateParentsByCompatiblePrinters(set, set, leaf, canonical)
			if err != nil {
				httpError(w, err)
				return
			}
		}
	} else {
		candidates, err = rebind.FindCandidateParents(set, set, leaf, token)
		if err != nil {
			httpError(w, err)
			return
		}
	}

	var familyCandidates []familyCandidate
	if len(candidates) == 0 && !alreadyCompatible {
		names, ferr := rebind.FindCandidateParentsBySameFamily(set, set, leaf)
		if ferr != nil {
			httpError(w, ferr)
			return
		}
		familyCandidates = familyCandidatesWithDiff(set, set, leaf, names)
	}

	var suggestedParent string
	if len(candidates) > 1 {
		suggestedParent, candidates = rankCandidatesByCloseness(set, leaf, candidates)
	}

	data := struct {
		Kind              string
		Name              string
		PrinterToken      string
		PrinterCanonical  string
		AlreadyCompatible bool
		Candidates        []string
		SuggestedParent   string
		FamilyCandidates  []familyCandidate
	}{
		Kind: kind.Key, Name: name, PrinterToken: token, PrinterCanonical: canonical,
		AlreadyCompatible: alreadyCompatible, Candidates: candidates, SuggestedParent: suggestedParent, FamilyCandidates: familyCandidates,
	}
	renderPage(w, copyPreviewTmpl, data, "Copy preview", "Copy preview: "+name, "&rarr; "+token, "copy", studioWarning(kind.Svc), true)
}

var copyResultTmpl = template.Must(template.New("copyResult").Funcs(statusFuncs).Parse(`
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-4">
  <div class="flex items-center gap-2">
    <span title="{{.Deployment.State}}" class="text-xs font-medium px-2.5 py-1 rounded-full {{statusClasses (print .Deployment.State)}}">{{statusLabel (print .Deployment.State)}}</span>
  </div>
  <p class="text-sm text-zinc-500">Parent: <code class="bg-zinc-100 px-1.5 py-0.5 rounded">{{.Parent}}</code> &middot; Name: <code class="bg-zinc-100 px-1.5 py-0.5 rounded">{{.Name}}</code></p>
  {{if .AddedCompatibility}}<p class="text-xs text-amber-700 bg-amber-50 border border-amber-200 rounded-lg px-3 py-2">Added <code class="bg-amber-100 px-1 rounded">{{.AddedCompatibility}}</code> to this profile's own compatible printers &mdash; it wasn't previously listed there by Bambu.</p>{{end}}
  {{if .Snapshot}}<p class="text-sm text-zinc-500">Backup taken: <code class="bg-zinc-100 px-1.5 py-0.5 rounded">{{.Snapshot}}</code></p>{{end}}
  <ol class="relative border-l border-zinc-200 ml-2 space-y-3">
  {{range .Deployment.History}}<li class="ml-4">
    <span class="absolute -left-[5px] w-2.5 h-2.5 rounded-full bg-emerald-500 ring-4 ring-white"></span>
    <p class="text-sm font-medium">{{.From}} &rarr; {{.To}}</p>
    {{if .Reason}}<p class="text-xs text-zinc-400">{{.Reason}}</p>{{end}}
  </li>{{end}}
  </ol>
  <a href="{{if eq .Kind "process"}}/deployments/process/{{.Deployment.ID}}{{else}}/deployments/{{.Deployment.ID}}{{end}}" class="inline-block text-sm font-medium text-emerald-600 hover:text-emerald-700">Deployment detail &rarr;</a>
</section>
`))

var studioBlockedTmpl = template.Must(template.New("studioBlocked").Parse(`
<div class="flex items-start gap-3 bg-red-50 border border-red-200 text-red-800 rounded-xl px-4 py-3 text-sm">
  <span>&#9888;</span>
  <div>
    <p class="font-medium">Publish refused: Bambu Studio is running</p>
    <p class="text-red-700/80">Publishing writes directly into Bambu Studio's profile directory and needs it closed first. Close Bambu Studio, then submit again &mdash; nothing was written.</p>
  </div>
</div>
`))

func (s *Server) handleCopyPublish(w http.ResponseWriter, r *http.Request) {
	s.renderCopyPublish(w, r, s.filamentKind())
}

func (s *Server) handleCopyPublishProcess(w http.ResponseWriter, r *http.Request) {
	kind, ok := s.processKind()
	if !ok {
		http.Error(w, "process profiles are not configured (serve without --process-user-dir)", http.StatusNotFound)
		return
	}
	s.renderCopyPublish(w, r, kind)
}

func (s *Server) renderCopyPublish(w http.ResponseWriter, r *http.Request, kind profileKind) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "copy: parse form: "+err.Error(), http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	parent := r.FormValue("parent")
	confirmName := r.FormValue("confirm_name")
	if name == "" || parent == "" || confirmName == "" {
		http.Error(w, "copy: name, parent, and confirm_name are required", http.StatusBadRequest)
		return
	}
	// Real bug found live: the "New profile name" field's suggested default
	// doesn't vary per candidate, so if a profile with that exact name
	// already existed on disk (e.g. left over from an earlier attempt with
	// the same source+target), it legitimately appears as a same-family
	// candidate too — picking that card while leaving the name field
	// unedited submits parent == confirm_name, publishing a profile whose
	// inherits points at itself. Bambu Studio silently drops profiles it
	// can't resolve, which is what "the copy doesn't show up" turned out
	// to actually be.
	if confirmName == parent {
		http.Error(w, "copy: the new profile name can't be the same as the parent it inherits from", http.StatusBadRequest)
		return
	}
	// confirm_unverified only appears on the family-fallback page (a parent
	// that exists but that Bambu's own catalog doesn't yet list as
	// compatible with printer_canonical) — the checkbox being present and
	// checked is the user's explicit instruction to add it, never inferred.
	var addCompatiblePrinter string
	if r.FormValue("confirm_unverified") != "" {
		addCompatiblePrinter = r.FormValue("printer_canonical")
	}

	set, err := resolver.LoadDirs(append([]string{kind.UserDir}, kind.SystemDirs...))
	if err != nil {
		httpError(w, err)
		return
	}
	leaf, ok := set[name]
	if !ok {
		http.Error(w, fmt.Sprintf("copy: %q not found", name), http.StatusNotFound)
		return
	}

	// Server-side backstop for the family-fallback flow: printer_canonical
	// is only ever submitted by that page (never the verified-match/
	// ambiguous-pick forms), so its presence here means the chosen parent
	// wasn't confirmed compatible with the target printer up front. The
	// HTML checkbox being "required" is a client-side nicety only — a raw
	// POST could skip it, which would otherwise publish onto a parent
	// that's neither verified compatible nor patched to become compatible.
	if canonicalTarget := r.FormValue("printer_canonical"); canonicalTarget != "" && addCompatiblePrinter == "" {
		if parentProfile, ok := set[parent]; ok {
			compat, cerr := rebind.IsAlreadyCompatible(set, parentProfile, canonicalTarget)
			if cerr == nil && !compat {
				http.Error(w, "copy: this parent is not verified compatible with the target printer; confirm_unverified is required", http.StatusBadRequest)
				return
			}
		}
	}

	ctx := r.Context()
	profile, err := kind.Svc.Repo.Profiles().GetByName(ctx, name)
	if errors.Is(err, storage.ErrNotFound) {
		profile, err = kind.Svc.Repo.Profiles().Create(ctx, name)
	}
	if err != nil {
		httpError(w, err)
		return
	}

	result, err := kind.Svc.RebindAndPublish(ctx, set, set, leaf, []string{parent}, profile.ID, confirmName, addCompatiblePrinter,
		reconcile.InfoFields{}, reconcile.InfoFields{})
	if errors.Is(err, bambuadapter.ErrStudioRunning) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		var contentBuf bytes.Buffer
		studioBlockedTmpl.Execute(&contentBuf, nil)
		shellData := struct {
			Title, HeaderTitle, HeaderSubtitle, Active string
			Warning, Content                           template.HTML
			WatchStudio                                bool
		}{Title: "Publish blocked", HeaderTitle: "Publish blocked", Active: "copy", Content: template.HTML(contentBuf.String()), WatchStudio: false}
		var buf bytes.Buffer
		shellTmpl.Execute(&buf, shellData)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		buf.WriteTo(w)
		return
	}
	if err != nil {
		http.Error(w, "copy: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	data := struct {
		Kind               string
		Deployment         any
		Parent             string
		Name               string
		Snapshot           string
		AddedCompatibility string
	}{
		Kind: kind.Key, Deployment: result.Deployment, Parent: parent, Name: confirmName,
		Snapshot: result.Snapshot, AddedCompatibility: addCompatiblePrinter,
	}
	renderPage(w, copyResultTmpl, data, "Copy result", "Copy result", "", "copy", "", false)
}

// deploymentStatusTmpl is the pollable part of the deployment page: the
// status badge, timeline, and (while INSTALLED_LOCALLY) the "reopen Studio
// and Save" note. Rendered both as the fragment /api/deployment-status
// returns for polling, and embedded once inside deploymentDetailTmpl for
// the full page — same content, same template, so a poll's refreshed
// markup is byte-identical to what a page reload would show.
var deploymentStatusTmpl = template.Must(template.New("deploymentStatus").Funcs(statusFuncs).Parse(`
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-4">
  <div class="flex items-center justify-between">
    <span title="{{.Deployment.State}}" class="text-xs font-medium px-2.5 py-1 rounded-full {{statusClasses (print .Deployment.State)}}">{{statusLabel (print .Deployment.State)}}</span>
    <span class="text-xs text-zinc-400">Revision {{.Deployment.Revision}}</span>
  </div>
  <ol class="relative border-l border-zinc-200 ml-2 space-y-3">
  {{range .Deployment.History}}<li class="ml-4">
    <span class="absolute -left-[5px] w-2.5 h-2.5 rounded-full bg-emerald-500 ring-4 ring-white"></span>
    <p class="text-sm font-medium">{{.At.Format "2006-01-02 15:04:05"}}: {{.From}} &rarr; {{.To}}</p>
    {{if .Reason}}<p class="text-xs text-zinc-400 truncate">{{.Reason}}</p>{{end}}
  </li>{{end}}
  </ol>
  {{if eq (print .Deployment.State) "INSTALLED_LOCALLY"}}
  <div class="border-t border-zinc-100 pt-4 space-y-2">
    <p class="text-sm text-zinc-500">
      One step is on you and can't be automated: reopen Bambu Studio, select the profile, and Save it once.
      That's the only thing that updates Studio's own local tracking file &mdash; reopening, selecting, or
      slicing the profile alone don't, and bambupm has no way to trigger or shortcut a Save inside Studio's UI.
    </p>
    <p class="text-sm text-zinc-500">
      Everything after that is automatic: bambupm checks this deployment every few seconds in the background and
      will flip it to <code class="bg-zinc-100 px-1 rounded">ACTIVE</code> (or flag a mismatch) the moment it
      notices &mdash; this page updates itself, no refresh needed.
    </p>
  </div>
  {{end}}
</section>
`))

// deploymentDetailTmpl embeds deploymentStatusTmpl (associated into the same
// template set below via New on the same *Template) inside a polled div,
// plus the client-side poll script — mirroring shell.go's studio-status
// live-poll pattern (real gap found live: the backend poller already
// rechecks INSTALLED_LOCALLY deployments every few seconds, but this page
// was static HTML from page load, so a user watching it saw nothing change
// until they manually reloaded or clicked into the deployment again).
var deploymentDetailTmpl = template.Must(deploymentStatusTmpl.New("deploymentDetail").Parse(`
<p class="text-sm text-zinc-500">A deployment only shows <strong>VERIFIED</strong> once Bambu Studio, on this machine, has actually
  opened and saved the profile and its settings still match &mdash; not just because bambupm wrote the file to disk. This is a local check
  (it reads the profile's own metadata file); it has nothing to do with Bambu's cloud account sync.</p>
<div id="deployment-status">{{template "deploymentStatus" .}}</div>
<script>
(function() {
  var el = document.getElementById('deployment-status');
  if (!el) return;
  function poll() {
    fetch("{{.StatusURL}}")
      .then(function(r) { return r.ok ? r.text() : Promise.reject(r.status); })
      .then(function(html) { el.innerHTML = html; })
      .catch(function() {});
  }
  setInterval(poll, 5000);
})();
</script>
`))

func (s *Server) handleDeploymentDetail(w http.ResponseWriter, r *http.Request) {
	s.renderDeploymentDetail(w, r, s.filamentKind(), "/api/deployment-status/")
}

func (s *Server) handleDeploymentDetailProcess(w http.ResponseWriter, r *http.Request) {
	kind, ok := s.processKind()
	if !ok {
		http.Error(w, "process profiles are not configured (serve without --process-user-dir)", http.StatusNotFound)
		return
	}
	s.renderDeploymentDetail(w, r, kind, "/api/deployment-status/process/")
}

func (s *Server) renderDeploymentDetail(w http.ResponseWriter, r *http.Request, kind profileKind, statusURLPrefix string) {
	id := r.PathValue("id")
	dep, err := kind.Svc.Repo.Deployments().Get(r.Context(), id)
	if err != nil {
		httpError(w, err)
		return
	}
	data := struct {
		Deployment any
		StatusURL  string
	}{Deployment: dep, StatusURL: statusURLPrefix + id}
	renderPage(w, deploymentDetailTmpl, data, "Deployment", "Deployment "+id, "", "", "", false)
}

func (s *Server) handleDeploymentStatusFragment(w http.ResponseWriter, r *http.Request) {
	s.renderDeploymentStatusFragment(w, r, s.filamentKind())
}

func (s *Server) handleDeploymentStatusFragmentProcess(w http.ResponseWriter, r *http.Request) {
	kind, ok := s.processKind()
	if !ok {
		http.Error(w, "process profiles are not configured (serve without --process-user-dir)", http.StatusNotFound)
		return
	}
	s.renderDeploymentStatusFragment(w, r, kind)
}

// renderDeploymentStatusFragment re-reads the deployment and returns just
// deploymentStatusTmpl's markup — polled client-side (see
// deploymentDetailTmpl's script) so the badge/timeline reflect the
// background poller's work without a page reload.
func (s *Server) renderDeploymentStatusFragment(w http.ResponseWriter, r *http.Request, kind profileKind) {
	id := r.PathValue("id")
	dep, err := kind.Svc.Repo.Deployments().Get(r.Context(), id)
	if err != nil {
		httpError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := deploymentStatusTmpl.Execute(w, struct{ Deployment any }{Deployment: dep}); err != nil {
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
	}
}

// recheckDeployment re-derives a deployment's published path/target profile
// from its stored revision and asks Service.CheckRecognition whether Bambu
// Studio has picked it up since. Shared by the manual HTTP endpoint (kept
// for tests/scripting) and the background poller — both need the identical
// lookup, only the caller differs.
func recheckDeployment(ctx context.Context, kind profileKind, id string) error {
	dep, err := kind.Svc.Repo.Deployments().Get(ctx, id)
	if err != nil {
		return err
	}
	versions, err := kind.Svc.Repo.Versions().List(ctx, dep.ProfileID)
	if err != nil {
		return err
	}
	var resolvedJSON []byte
	for _, v := range versions {
		if v.Revision == dep.Revision {
			resolvedJSON = v.ResolvedJSON
			break
		}
	}
	if resolvedJSON == nil {
		return fmt.Errorf("recheck: no stored version for revision %d of deployment %s", dep.Revision, id)
	}
	targetProfile, err := service.TargetProfileFromVersion(resolvedJSON)
	if err != nil {
		return err
	}
	publishedPath := filepath.Join(kind.UserDir, targetProfile.Name+".json")
	infoPath := filepath.Join(kind.UserDir, targetProfile.Name+".info")

	afterInfo := reconcile.InfoFields{}
	if b, err := os.ReadFile(infoPath); err == nil {
		afterInfo = reconcile.ParseInfo(b)
	} else if !os.IsNotExist(err) {
		return err
	}

	// Loaded so CheckRecognition can resolve targetProfile's (and the
	// observed file's) full inherits chain before hashing — real bug found
	// live: without this, a harmless Bambu Studio dedup-on-save (dropping a
	// leaf field that's now redundant with a rebound parent) read as a
	// SEMANTIC_MISMATCH even though nothing about the effective settings
	// changed. Best-effort: a load failure just falls back to today's
	// raw-fields comparison rather than blocking the recheck entirely.
	set, _ := resolver.LoadDirs(append([]string{kind.UserDir}, kind.SystemDirs...))

	_, err = kind.Svc.CheckRecognition(ctx, id, targetProfile, publishedPath, set, reconcile.InfoFields{}, afterInfo)
	return err
}

func (s *Server) handleCheckRecognition(w http.ResponseWriter, r *http.Request) {
	s.renderCheckRecognition(w, r, s.filamentKind(), "/deployments/")
}

func (s *Server) handleCheckRecognitionProcess(w http.ResponseWriter, r *http.Request) {
	kind, ok := s.processKind()
	if !ok {
		http.Error(w, "process profiles are not configured (serve without --process-user-dir)", http.StatusNotFound)
		return
	}
	s.renderCheckRecognition(w, r, kind, "/deployments/process/")
}

func (s *Server) renderCheckRecognition(w http.ResponseWriter, r *http.Request, kind profileKind, redirectPrefix string) {
	id := r.PathValue("id")
	if err := recheckDeployment(r.Context(), kind, id); err != nil {
		// Still redirect rather than 500: the deployment was saved with
		// whatever state it reached, and that's visible in its history
		// (e.g. SEMANTIC_MISMATCH is a real, informative outcome, not a
		// request failure). Logged so a genuine internal error (I/O, JSON)
		// isn't completely invisible.
		fmt.Fprintf(os.Stderr, "check-recognition %s: %v\n", id, err)
	}
	http.Redirect(w, r, redirectPrefix+id, http.StatusSeeOther)
}

// pollOnceForKind scans every deployment in kind's Repo sitting at
// INSTALLED_LOCALLY and rechecks it once. Called for each configured kind on
// a timer by PollRecognition; split out so tests can drive a single pass
// deterministically instead of racing a real ticker.
func pollOnceForKind(ctx context.Context, kind profileKind) {
	profiles, err := kind.Svc.Repo.Profiles().List(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "recognition poll (%s): list profiles: %v\n", kind.Key, err)
		return
	}
	for _, p := range profiles {
		deps, err := kind.Svc.Repo.Deployments().ListByProfile(ctx, p.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "recognition poll (%s): list deployments for %s: %v\n", kind.Key, p.ID, err)
			continue
		}
		for _, dep := range deps {
			if dep.State != reconcile.StateInstalledLocally {
				continue
			}
			if err := recheckDeployment(ctx, kind, dep.ID); err != nil {
				fmt.Fprintf(os.Stderr, "recognition poll (%s): deployment %s: %v\n", kind.Key, dep.ID, err)
			}
		}
	}
}

func (s *Server) pollOnce(ctx context.Context) {
	pollOnceForKind(ctx, s.filamentKind())
	if kind, ok := s.processKind(); ok {
		pollOnceForKind(ctx, kind)
	}
}

// PollRecognition runs pollOnce (both filament and, if configured, process
// deployments) on a fixed interval until ctx is canceled. Bambu Studio only
// updates a profile's local tracking metadata when the user Saves it in
// Studio's own UI (confirmed empirically — reopening, selecting, or slicing
// the profile alone do not); bambupm cannot trigger or detect that moment
// except by re-reading the file afterwards, so this is the closest thing to
// "automatic" available: the user still Saves once in Studio, but no longer
// has to come back and click a button to notice it.
func (s *Server) PollRecognition(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollOnce(ctx)
		}
	}
}

var backupsTmpl = template.Must(template.New("backups").Parse(`
<p class="text-sm text-zinc-500">Restoring overwrites files present in that snapshot but never deletes anything added since &mdash; safe to restore an old one without losing newer work.</p>
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm divide-y divide-zinc-100">
{{range .List}}
<div class="flex items-center justify-between px-5 py-3.5">
  <div>
    <p class="text-sm font-medium">{{.At.Format "2006-01-02 15:04:05 MST"}}</p>
    <code class="text-xs text-zinc-400">{{.Name}}</code>
  </div>
  <form method="post" action="/backups/{{.Name}}/restore">
    <button type="submit" onclick="return confirm('Restore this snapshot? Overwrites files present in it, does not delete anything added since.')"
      class="px-3 py-1.5 rounded-lg border border-zinc-300 text-xs font-medium hover:bg-zinc-50 transition">Restore</button>
  </form>
</div>
{{else}}<div class="text-center py-16 text-zinc-400"><p class="text-sm">No backups yet.</p></div>{{end}}
</section>
`))

func (s *Server) handleBackupsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Svc.ListBackups()
	if err != nil {
		httpError(w, err)
		return
	}
	data := struct{ List any }{List: list}
	renderPage(w, backupsTmpl, data, "Backups", "Backups", "Point-in-time snapshots taken automatically before every publish.", "backups", studioWarning(s.Svc), true)
}

func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.Svc.RestoreBackup(name); err != nil {
		httpError(w, err)
		return
	}
	http.Redirect(w, r, "/backups", http.StatusSeeOther)
}
