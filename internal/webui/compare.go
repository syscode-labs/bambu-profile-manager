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
)

// summaryField is one recognizable setting shown on the profile detail
// page's summary card, Bambu-Studio-style (vendor/type/temps up front)
// instead of a raw JSON dump. Field names confirmed against real resolved
// profile fixtures (see openspec/changes/init-profile-manager/findings.md).
type summaryField struct{ Label, Value string }

func fieldString(fields map[string]any, key string) string {
	v, ok := fields[key]
	if !ok {
		return ""
	}
	return fmt.Sprint(v)
}

func profileSummary(fields map[string]any) []summaryField {
	var out []summaryField
	add := func(label, key string) {
		if v := fieldString(fields, key); v != "" && v != "[]" {
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

var basicKeys = map[string]bool{
	"name": true, "filament_vendor": true, "filament_type": true,
	"default_filament_colour": true, "filament_diameter": true,
	"filament_density": true, "filament_cost": true, "filament_notes": true,
}

var categoryOrder = []string{"Basic", "Temperature", "Cooling", "Flow & Retraction", "Advanced"}

func categoryOf(key string) string {
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

// groupedFields buckets every field into categoryOrder's Bambu-Studio-like
// sections, alphabetized within each, skipping empty sections.
func groupedFields(fields map[string]any) []fieldGroup {
	byCategory := map[string][]kv{}
	for k, v := range fields {
		byCategory[categoryOf(k)] = append(byCategory[categoryOf(k)], kv{Key: prettyLabel(k), Value: fmt.Sprint(v)})
	}
	var groups []fieldGroup
	for _, title := range categoryOrder {
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
// Differ set so the template can highlight rows that don't match.
func compareItems(itemsFields []map[string]any) []compareGroup {
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
				s = fmt.Sprint(v)
			}
			values[i] = s
			seen[s] = true
		}
		row := compareRow{Label: prettyLabel(k), Values: values, Differ: len(seen) > 1}
		cat := categoryOf(k)
		byCategory[cat] = append(byCategory[cat], row)
	}
	var groups []compareGroup
	for _, title := range categoryOrder {
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

type compareOption struct{ Value, Label string }

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
			})
		}
	}
	return opts, nil
}

type comparedItem struct {
	ProfileName string
	Revision    int
}

var comparePageTmpl = template.Must(template.New("comparePage").Parse(`
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-4">
  <p class="text-sm text-zinc-500">Pick 2 or 3 profile revisions &mdash; any profiles, not just different revisions of the same one.</p>
  <form method="get" action="/compare" class="grid grid-cols-3 gap-4">
    <label class="block">
      <span class="text-xs font-medium text-zinc-500 mb-1 block">Item 1</span>
      <select name="item" class="w-full rounded-lg border border-zinc-300 px-3 py-2 text-sm">
        <option value="">&mdash; none &mdash;</option>
        {{$sel := index .Items 0}}{{range .Options}}<option value="{{.Value}}" {{if eq .Value $sel}}selected{{end}}>{{.Label}}</option>{{end}}
      </select>
    </label>
    <label class="block">
      <span class="text-xs font-medium text-zinc-500 mb-1 block">Item 2</span>
      <select name="item" class="w-full rounded-lg border border-zinc-300 px-3 py-2 text-sm">
        <option value="">&mdash; none &mdash;</option>
        {{$sel := index .Items 1}}{{range .Options}}<option value="{{.Value}}" {{if eq .Value $sel}}selected{{end}}>{{.Label}}</option>{{end}}
      </select>
    </label>
    <label class="block">
      <span class="text-xs font-medium text-zinc-500 mb-1 block">Item 3 (optional)</span>
      <select name="item" class="w-full rounded-lg border border-zinc-300 px-3 py-2 text-sm">
        <option value="">&mdash; none &mdash;</option>
        {{$sel := index .Items 2}}{{range .Options}}<option value="{{.Value}}" {{if eq .Value $sel}}selected{{end}}>{{.Label}}</option>{{end}}
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
    {{range .Selected}}<div class="text-xs font-semibold text-zinc-700">{{.ProfileName}} <span class="text-zinc-400 font-normal">rev {{.Revision}}</span></div>{{end}}
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
			selected = append(selected, comparedItem{ProfileName: p.Name, Revision: rev})
			fieldsList = append(fieldsList, fields)
		}
	}

	var groups []compareGroup
	if len(selected) >= 2 {
		groups = compareItems(fieldsList)
	}

	items := make([]string, 3)
	copy(items, itemKeys)

	data := struct {
		Options  []compareOption
		Items    []string
		Selected []comparedItem
		Groups   []compareGroup
	}{Options: opts, Items: items, Selected: selected, Groups: groups}
	renderPage(w, comparePageTmpl, data, "Compare", "Compare profiles", "Pick up to 3 profile revisions to compare side by side.", "profiles", "")
}
