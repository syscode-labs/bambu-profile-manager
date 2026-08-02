package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/syscod3/bambu-profile-manager/internal/resolver"
)

// summaryField is one recognizable setting shown on the profile detail
// page's summary card, Bambu-Studio-style (vendor/type/temps up front)
// instead of a raw JSON dump. Field names confirmed against real resolved
// profile fixtures (see openspec/changes/init-profile-manager/findings.md).
type summaryField struct{ Label, Value string }

// formatValue renders a decoded JSON field the way Bambu Studio's own panel
// would show it, not Go's default formatting. Confirmed against real
// profiles (findings.md): most numeric settings are stored as an array with
// one entry per extruder (e.g. nozzle_temperature: ["250","250"]) — Go's
// fmt.Sprint renders that as the literal "[250 250]", which is not what
// Studio displays. Unwraps a single distinct value; keeps distinct
// per-extruder values joined with " / " rather than hiding them.
func formatValue(key string, v any) string {
	var s string
	if arr, ok := v.([]any); ok {
		parts := make([]string, len(arr))
		for i, e := range arr {
			parts[i] = fmt.Sprint(e)
		}
		allSame := true
		for _, p := range parts {
			if p != parts[0] {
				allSame = false
				break
			}
		}
		if allSame && len(parts) > 0 {
			s = parts[0]
		} else {
			s = strings.Join(parts, " / ")
		}
	} else {
		s = fmt.Sprint(v)
	}
	if s != "" && isTemperatureKey(key) {
		s += " °C"
	}
	return s
}

// isTemperatureKey is a name-based heuristic (no authoritative units schema
// exists — see findings.md) for which fields are temperatures worth a °C
// suffix. "temperature_type" and similar non-numeric fields would false
// -positive on a bare "temp" match, so this requires the fuller "temp"
// substring in a numeric-looking context; kept simple and conservative.
func isTemperatureKey(key string) bool {
	return strings.Contains(key, "_temp") || strings.HasPrefix(key, "temp")
}

func fieldString(fields map[string]any, key string) string {
	v, ok := fields[key]
	if !ok {
		return ""
	}
	return formatValue(key, v)
}

func profileSummary(fields map[string]any) []summaryField {
	var out []summaryField
	add := func(label, key string) {
		if v := fieldString(fields, key); v != "" {
			out = append(out, summaryField{label, v})
		}
	}
	add("Vendor", "filament_vendor")
	add("Type", "filament_type")
	add("Nozzle temp", "nozzle_temperature")
	add("Nozzle temp (first layer)", "nozzle_temperature_initial_layer")
	add("Bed temp", "hot_plate_temp")
	add("Bed temp (first layer)", "hot_plate_temp_initial_layer")
	return out
}

// profileColor extracts a hex color from default_filament_colour (observed
// shape: a single-element array, e.g. ["#DC0C26"]) for a visual swatch.
func profileColor(fields map[string]any) string {
	v, ok := fields["default_filament_colour"].([]any)
	if !ok || len(v) == 0 {
		return ""
	}
	s, _ := v[0].(string)
	return s
}

// --- Bambu-Studio-style grouping ---
//
// Bambu Studio's own filament settings UI groups fields into tabs (Basic,
// Cooling, Advanced, ...). We don't have an authoritative field->tab map, so
// this is a heuristic approximation from field-name patterns, in a fixed
// display order — good enough to stop the page reading like a JSON dump.
//
// Process (print) profiles use a completely different set of tabs in real
// Bambu Studio (Quality, Strength, Speed, Support, ...) — real bug found
// live: reusing the filament scheme for process profiles put fields like
// wall_loops/layer_height under a filament-shaped "Advanced" bucket and
// left "Temperature"/"Cooling"/"Flow & Retraction" sitting there empty or
// misleadingly matching on unrelated substrings, making a process
// comparison read as if it were comparing filaments.

var basicKeys = map[string]bool{
	"name": true, "filament_vendor": true, "filament_type": true,
	"default_filament_colour": true, "filament_diameter": true,
	"filament_density": true, "filament_cost": true, "filament_notes": true,
}

var categoryOrder = []string{"Basic", "Temperature", "Cooling", "Flow & Retraction", "Advanced"}

var processBasicKeys = map[string]bool{
	"name": true, "print_settings_id": true, "inherits": true, "from": true,
	"version": true, "compatible_printers": true, "layer_height": true,
}

var processCategoryOrder = []string{"Basic", "Layers & Perimeters", "Infill", "Support", "Speed", "Advanced"}

func categoryOf(kind, key string) string {
	if kind == "process" {
		return processCategoryOf(key)
	}
	if basicKeys[key] {
		return "Basic"
	}
	switch {
	case strings.Contains(key, "temp"):
		return "Temperature"
	case strings.Contains(key, "fan") || strings.Contains(key, "cooling"):
		return "Cooling"
	case strings.Contains(key, "flow") || strings.Contains(key, "retract") || strings.Contains(key, "pressure_advance") || strings.Contains(key, "extrusion"):
		return "Flow & Retraction"
	default:
		return "Advanced"
	}
}

func processCategoryOf(key string) string {
	if processBasicKeys[key] {
		return "Basic"
	}
	switch {
	// Checked first: real field names like "outer_wall_speed" or
	// "sparse_infill_speed" also match the wall/infill checks below, but
	// they're Speed-tab settings in Bambu Studio's own UI, not
	// Layers/Infill ones — the "_speed"/"_acceleration" suffix wins.
	case strings.Contains(key, "speed") || strings.Contains(key, "acceleration") || strings.Contains(key, "travel") || strings.Contains(key, "jerk"):
		return "Speed"
	case strings.Contains(key, "support"):
		return "Support"
	case strings.Contains(key, "infill"):
		return "Infill"
	case strings.Contains(key, "wall") || strings.Contains(key, "shell") || strings.Contains(key, "seam") ||
		strings.Contains(key, "skirt") || strings.Contains(key, "brim") || strings.Contains(key, "layer"):
		return "Layers & Perimeters"
	default:
		return "Advanced"
	}
}

func categoryOrderFor(kind string) []string {
	if kind == "process" {
		return processCategoryOrder
	}
	return categoryOrder
}

func prettyLabel(key string) string {
	words := strings.Split(key, "_")
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// kv is one labeled settings row.
type kv struct{ Key, Value string }

type fieldGroup struct {
	Title string
	Rows  []kv
}

// groupedFields buckets every field into categoryOrderFor(kind)'s
// Bambu-Studio-like sections, alphabetized within each, skipping empty
// sections. kind is "filament" or "process" — they use different section
// schemes (see categoryOf's doc comment).
func groupedFields(kind string, fields map[string]any) []fieldGroup {
	byCategory := map[string][]kv{}
	for k, v := range fields {
		byCategory[categoryOf(kind, k)] = append(byCategory[categoryOf(kind, k)], kv{Key: prettyLabel(k), Value: formatValue(k, v)})
	}
	var groups []fieldGroup
	for _, title := range categoryOrderFor(kind) {
		rows := byCategory[title]
		if len(rows) == 0 {
			continue
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
		groups = append(groups, fieldGroup{Title: title, Rows: rows})
	}
	return groups
}

// --- multi-item compare (up to 3, any profiles/revisions) ---

type compareRow struct {
	Label  string
	Values []string
	Differ bool
}

type compareGroup struct {
	Title string
	Rows  []compareRow
}

// compareItems diffs 2-3 resolved-field sets side by side, grouped the same
// way as groupedFields, with every row present (not just differences) and
// Differ set so the template can highlight rows that don't match. kind
// picks the section scheme (see categoryOf) — the caller uses "process" if
// any selected item is a process profile, since process-specific fields
// need proper buckets even when compared alongside filament ones.
func compareItems(kind string, itemsFields []map[string]any) []compareGroup {
	keys := map[string]bool{}
	for _, f := range itemsFields {
		for k := range f {
			keys[k] = true
		}
	}
	byCategory := map[string][]compareRow{}
	for k := range keys {
		values := make([]string, len(itemsFields))
		seen := map[string]bool{}
		for i, f := range itemsFields {
			s := "(not set)"
			if v, ok := f[k]; ok {
				s = formatValue(k, v)
			}
			values[i] = s
			seen[s] = true
		}
		row := compareRow{Label: prettyLabel(k), Values: values, Differ: len(seen) > 1}
		cat := categoryOf(kind, k)
		byCategory[cat] = append(byCategory[cat], row)
	}
	var groups []compareGroup
	for _, title := range categoryOrderFor(kind) {
		rows := byCategory[title]
		if len(rows) == 0 {
			continue
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Label < rows[j].Label })
		groups = append(groups, compareGroup{Title: title, Rows: rows})
	}
	return groups
}

// --- /compare page: pick up to 3 profile+revision combos, any profiles ---

// Kind ("filament"/"process") is carried through so the picker can group
// options and the comparison table can badge each selected item — real gap
// found live: filament and process names can look identical out of context
// (e.g. a process profile named after its printer, same as a filament
// would be), and the old flat list only hinted at the difference via a
// cramped "(live, process)" suffix in the label text.
type compareOption struct{ Value, Label, Kind string }

// compareOptions lists both bpm-tracked profile revisions (frozen
// snapshots with a stored ProfileVersion) and live profiles read straight
// off --user-dir/--system-dir. Most of a real Bambu Studio library is never
// tracked (only profiles that went through Import/Copy get a ProfileVersion
// row — see the same gap fixed for the landing page), so without the live
// half this dropdown would be empty for nearly everyone.
func (s *Server) compareOptions(ctx context.Context) ([]compareOption, error) {
	profiles, err := s.Svc.Repo.Profiles().List(ctx)
	if err != nil {
		return nil, err
	}
	var opts []compareOption
	for _, p := range profiles {
		versions, err := s.Svc.Repo.Versions().List(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		for _, v := range versions {
			opts = append(opts, compareOption{
				Value: p.ID + ":" + strconv.Itoa(v.Revision),
				Label: p.Name + " — rev " + strconv.Itoa(v.Revision),
				Kind:  "filament",
			})
		}
	}

	liveOpts, err := liveCompareOptions(s.UserDir, s.SystemDirs, "live:", "(live)", "filament")
	if err != nil {
		return nil, err
	}
	opts = append(opts, liveOpts...)

	if s.ProcessSvc != nil {
		processOpts, err := liveCompareOptions(s.ProcessUserDir, s.ProcessSystemDirs, "liveprocess:", "(live)", "process")
		if err != nil {
			return nil, err
		}
		opts = append(opts, processOpts...)
	}
	return opts, nil
}

// liveCompareOptions lists every profile found live under userDir/systemDirs
// as a compareOption, prefixed and labeled distinctly per caller — used for
// both filament ("live:") and process ("liveprocess:") so the two can never
// collide even if a name happened to match across the two catalogs.
func liveCompareOptions(userDir string, systemDirs []string, prefix, labelSuffix, kind string) ([]compareOption, error) {
	if userDir == "" {
		return nil, nil
	}
	set, err := resolver.LoadDirs(append([]string{userDir}, systemDirs...))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	opts := make([]compareOption, 0, len(names))
	for _, name := range names {
		opts = append(opts, compareOption{Value: prefix + name, Label: name + " " + labelSuffix, Kind: kind})
	}
	return opts, nil
}

type comparedItem struct {
	ProfileName string
	Revision    int
	IsLive      bool
	Kind        string // "filament" or "process"
}

var comparePageTmpl = template.Must(template.New("comparePage").Parse(`
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-4">
  <p class="text-sm text-zinc-500">Pick 2 or 3 profile revisions &mdash; any profiles, not just different revisions of the same one.</p>
  <form method="get" action="/compare" class="grid grid-cols-3 gap-4">
    <label class="block">
      <span class="text-xs font-medium text-zinc-500 mb-1 block">Item 1</span>
      <select name="item" class="w-full rounded-lg border border-zinc-300 px-3 py-2 text-sm">
        <option value="">&mdash; none &mdash;</option>
        {{$sel := index .Items 0}}
        {{if .FilamentOptions}}<optgroup label="Filament">{{range .FilamentOptions}}<option value="{{.Value}}" {{if eq .Value $sel}}selected{{end}}>{{.Label}}</option>{{end}}</optgroup>{{end}}
        {{if .ProcessOptions}}<optgroup label="Process (print)">{{range .ProcessOptions}}<option value="{{.Value}}" {{if eq .Value $sel}}selected{{end}}>{{.Label}}</option>{{end}}</optgroup>{{end}}
      </select>
    </label>
    <label class="block">
      <span class="text-xs font-medium text-zinc-500 mb-1 block">Item 2</span>
      <select name="item" class="w-full rounded-lg border border-zinc-300 px-3 py-2 text-sm">
        <option value="">&mdash; none &mdash;</option>
        {{$sel := index .Items 1}}
        {{if .FilamentOptions}}<optgroup label="Filament">{{range .FilamentOptions}}<option value="{{.Value}}" {{if eq .Value $sel}}selected{{end}}>{{.Label}}</option>{{end}}</optgroup>{{end}}
        {{if .ProcessOptions}}<optgroup label="Process (print)">{{range .ProcessOptions}}<option value="{{.Value}}" {{if eq .Value $sel}}selected{{end}}>{{.Label}}</option>{{end}}</optgroup>{{end}}
      </select>
    </label>
    <label class="block">
      <span class="text-xs font-medium text-zinc-500 mb-1 block">Item 3 (optional)</span>
      <select name="item" class="w-full rounded-lg border border-zinc-300 px-3 py-2 text-sm">
        <option value="">&mdash; none &mdash;</option>
        {{$sel := index .Items 2}}
        {{if .FilamentOptions}}<optgroup label="Filament">{{range .FilamentOptions}}<option value="{{.Value}}" {{if eq .Value $sel}}selected{{end}}>{{.Label}}</option>{{end}}</optgroup>{{end}}
        {{if .ProcessOptions}}<optgroup label="Process (print)">{{range .ProcessOptions}}<option value="{{.Value}}" {{if eq .Value $sel}}selected{{end}}>{{.Label}}</option>{{end}}</optgroup>{{end}}
      </select>
    </label>
    <div class="col-span-3">
      <button type="submit" class="px-4 py-2 rounded-lg bg-zinc-900 text-white text-sm font-medium hover:bg-zinc-800 transition">Compare</button>
    </div>
  </form>
</section>

{{if .Groups}}
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-5">
  <div class="grid {{if eq (len .Selected) 2}}grid-cols-[180px_1fr_1fr]{{else}}grid-cols-[180px_1fr_1fr_1fr]{{end}} gap-3">
    <div></div>
    {{range .Selected}}<div class="text-xs font-semibold text-zinc-700">{{.ProfileName}}
      <span class="ml-1 px-1.5 py-0.5 rounded-full text-[10px] font-medium {{if eq .Kind "process"}}bg-indigo-100 text-indigo-700{{else}}bg-sky-100 text-sky-700{{end}}">{{if eq .Kind "process"}}process{{else}}filament{{end}}</span>
      <span class="text-zinc-400 font-normal">{{if .IsLive}}(live){{else}}rev {{.Revision}}{{end}}</span></div>{{end}}
  </div>
  {{range .Groups}}
  <div>
    <h3 class="text-xs font-semibold text-zinc-400 uppercase tracking-wide mb-2">{{.Title}}</h3>
    <div class="space-y-0.5">
    {{range .Rows}}
    {{$row := .}}
    <div class="grid {{if eq (len $.Selected) 2}}grid-cols-[180px_1fr_1fr]{{else}}grid-cols-[180px_1fr_1fr_1fr]{{end}} gap-3 text-xs px-2 py-1.5 rounded-lg {{if $row.Differ}}bg-amber-50{{end}}">
      <div class="text-zinc-500">{{$row.Label}}</div>
      {{range $row.Values}}<div class="font-mono break-all {{if $row.Differ}}text-amber-800 font-medium{{end}}">{{.}}</div>{{end}}
    </div>
    {{end}}
    </div>
  </div>
  {{end}}
</section>
{{else if ge (len .Selected) 1}}
<p class="text-sm text-zinc-500">Pick at least one more item to compare.</p>
{{end}}
`))

func (s *Server) handleComparePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	raw := r.URL.Query()["item"]
	var itemKeys []string
	for _, it := range raw {
		if it != "" {
			itemKeys = append(itemKeys, it)
		}
	}
	if len(itemKeys) > 3 {
		itemKeys = itemKeys[:3]
	}

	opts, err := s.compareOptions(ctx)
	if err != nil {
		httpError(w, err)
		return
	}

	var selected []comparedItem
	var fieldsList []map[string]any
	for _, it := range itemKeys {
		if name, ok := strings.CutPrefix(it, "liveprocess:"); ok {
			fields, err := resolveFieldsFrom(s.ProcessUserDir, s.ProcessSystemDirs, name)
			if err != nil {
				continue
			}
			selected = append(selected, comparedItem{ProfileName: name, IsLive: true, Kind: "process"})
			fieldsList = append(fieldsList, fields)
			continue
		}
		if name, ok := strings.CutPrefix(it, "live:"); ok {
			fields, err := s.resolveLiveFields(name)
			if err != nil {
				continue
			}
			selected = append(selected, comparedItem{ProfileName: name, IsLive: true, Kind: "filament"})
			fieldsList = append(fieldsList, fields)
			continue
		}

		profileID, revStr, ok := strings.Cut(it, ":")
		if !ok {
			continue
		}
		rev, err := strconv.Atoi(revStr)
		if err != nil {
			continue
		}
		p, err := s.Svc.Repo.Profiles().Get(ctx, profileID)
		if err != nil {
			continue
		}
		versions, err := s.Svc.Repo.Versions().List(ctx, profileID)
		if err != nil {
			continue
		}
		for _, v := range versions {
			if v.Revision != rev {
				continue
			}
			var fields map[string]any
			if err := json.Unmarshal(v.ResolvedJSON, &fields); err != nil {
				continue
			}
			selected = append(selected, comparedItem{ProfileName: p.Name, Revision: rev, Kind: "filament"})
			fieldsList = append(fieldsList, fields)
		}
	}

	compareKind := "filament"
	for _, it := range selected {
		if it.Kind == "process" {
			compareKind = "process"
			break
		}
	}

	var groups []compareGroup
	if len(selected) >= 2 {
		groups = compareItems(compareKind, fieldsList)
	}

	items := make([]string, 3)
	copy(items, itemKeys)

	var filamentOpts, processOpts []compareOption
	for _, o := range opts {
		if o.Kind == "process" {
			processOpts = append(processOpts, o)
		} else {
			filamentOpts = append(filamentOpts, o)
		}
	}

	data := struct {
		FilamentOptions []compareOption
		ProcessOptions  []compareOption
		Items           []string
		Selected        []comparedItem
		Groups          []compareGroup
	}{FilamentOptions: filamentOpts, ProcessOptions: processOpts, Items: items, Selected: selected, Groups: groups}
	renderPage(w, comparePageTmpl, data, "Compare", "Compare profiles", "Pick up to 3 profile revisions to compare side by side.", "compare", "", false)
}
