// Package webui is the local web UI (design.md §18), server-rendered REST +
// plain page reloads — no WebSocket, no client-side framework, per
// decisions.md #7 ("ship the UI from Phase 1, defer cache/WebSocket infra
// until real usage shows a need"). Styling is Tailwind (via CDN, see
// shell.go) chosen over an Electron shell after comparing both as throwaway
// POCs — kept it a single Go binary with plain page reloads rather than add
// a Node.js/npm toolchain.
package webui

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"sort"

	"github.com/syscod3/bambu-profile-manager/internal/resolver"
	"github.com/syscod3/bambu-profile-manager/internal/service"
	"github.com/syscod3/bambu-profile-manager/internal/storage"
)

type Server struct {
	Svc *service.Service
	// UserDir/SystemDirs let the "copy to another printer" and backups
	// pages scan/act on the real Bambu Studio directories directly,
	// separately from Svc.Adapter.Dir (same value in production, but kept
	// distinct so tests can point Svc at a temp dir without this Server
	// needing to know about it).
	UserDir    string
	SystemDirs []string
	// MachineDirs are the user's + system's printer (machine) profile
	// directories — a different tree from UserDir/SystemDirs (filament).
	// Used only to build the real "target printer" list on the copy form;
	// nothing here is ever staged/published (bambupm manages filament
	// profiles, not printer profiles — design.md §3).
	MachineDirs []string
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /profiles/{id}", s.handleProfile)
	mux.HandleFunc("GET /profiles/{id}/deployments", s.handleDeployments)
	mux.HandleFunc("GET /live/{name}", s.handleLiveProfile)
	mux.HandleFunc("GET /compare", s.handleComparePage)
	mux.HandleFunc("GET /import", s.handleImportForm)
	mux.HandleFunc("POST /import", s.handleImport)
	mux.HandleFunc("GET /copy", s.handleCopyForm)
	mux.HandleFunc("POST /copy/preview", s.handleCopyPreview)
	mux.HandleFunc("POST /copy/publish", s.handleCopyPublish)
	mux.HandleFunc("GET /deployments/{id}", s.handleDeploymentDetail)
	mux.HandleFunc("POST /deployments/{id}/check", s.handleCheckRecognition)
	mux.HandleFunc("GET /backups", s.handleBackupsList)
	mux.HandleFunc("POST /backups/{name}/restore", s.handleBackupRestore)
	return mux
}

var indexTmpl = template.Must(template.New("index").Parse(`
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6">
  <p class="text-sm text-zinc-600 leading-relaxed">
    bambupm tracks your Bambu Studio filament profiles, lets you <strong>copy one to another printer</strong> without
    touching dependency chains by hand, and <strong>verifies</strong> Bambu Studio actually recognized the result
    before calling it done &mdash; instead of assuming a file write means success.
  </p>
</section>

<div class="grid grid-cols-3 gap-4">
  <a href="/copy" class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5 hover:border-emerald-300 hover:shadow-md transition group">
    <svg class="w-5 h-5 text-emerald-600 mb-2" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 7h12m0 0l-4-4m4 4l-4 4M16 17H4m0 0l4 4m-4-4l4-4"/></svg>
    <p class="text-sm font-semibold group-hover:text-emerald-700">Copy to another printer</p>
    <p class="text-xs text-zinc-500 mt-1">Pick a profile and a target printer &mdash; the parent chain is matched for you.</p>
  </a>
  <a href="/import" class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5 hover:border-emerald-300 hover:shadow-md transition group">
    <svg class="w-5 h-5 text-emerald-600 mb-2" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v12m0 0l-4-4m4 4l4-4M4 20h16"/></svg>
    <p class="text-sm font-semibold group-hover:text-emerald-700">Import a bundle</p>
    <p class="text-xs text-zinc-500 mt-1">Bring in a .profilepack exported elsewhere, dependencies and all.</p>
  </a>
  <a href="/backups" class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-5 hover:border-emerald-300 hover:shadow-md transition group">
    <svg class="w-5 h-5 text-emerald-600 mb-2" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/></svg>
    <p class="text-sm font-semibold group-hover:text-emerald-700">Backups</p>
    <p class="text-xs text-zinc-500 mt-1">Every publish snapshots your directory first &mdash; restore any of them.</p>
  </a>
</div>

<div>
  <h2 class="text-sm font-medium text-zinc-500 mb-3">All profiles</h2>
  {{if .}}
  <div class="bg-white rounded-2xl border border-zinc-200 shadow-sm divide-y divide-zinc-100">
  {{range .}}<a href="{{.Link}}" class="flex items-center justify-between px-5 py-3.5 hover:bg-zinc-50 transition first:rounded-t-2xl last:rounded-b-2xl">
    <span class="text-sm font-medium text-zinc-800">{{.Name}}</span>
    <div class="flex items-center gap-2">
      {{if not .Tracked}}<span class="text-xs px-2 py-0.5 rounded-full bg-zinc-100 text-zinc-500">not tracked</span>{{end}}
      <svg class="w-4 h-4 text-zinc-300" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
    </div>
  </a>{{end}}
  </div>
  {{else}}
  <div class="text-center py-12 text-zinc-400 bg-white rounded-2xl border border-dashed border-zinc-200">
    <p class="text-sm">No profiles found. Point <code class="bg-zinc-100 px-1 rounded">bambupm serve</code> at your Bambu Studio directory with --user-dir, or Import a bundle.</p>
  </div>
  {{end}}
</div>
`))

type indexProfile struct {
	Name    string
	Tracked bool
	Link    string
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tracked, err := s.Svc.Repo.Profiles().List(ctx)
	if err != nil {
		httpError(w, err)
		return
	}
	trackedIDByName := map[string]string{}
	for _, p := range tracked {
		trackedIDByName[p.Name] = p.ID
	}

	var items []indexProfile
	if s.UserDir != "" {
		set, err := resolver.LoadDirs([]string{s.UserDir})
		if err != nil {
			httpError(w, err)
			return
		}
		names := make([]string, 0, len(set))
		for name := range set {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if id, ok := trackedIDByName[name]; ok {
				items = append(items, indexProfile{Name: name, Tracked: true, Link: "/profiles/" + id})
			} else {
				items = append(items, indexProfile{Name: name, Tracked: false, Link: "/live/" + url.PathEscape(name)})
			}
		}
	} else {
		// No live directory configured (e.g. `serve` without --user-dir) —
		// fall back to whatever's tracked in the db.
		for _, p := range tracked {
			items = append(items, indexProfile{Name: p.Name, Tracked: true, Link: "/profiles/" + p.ID})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	}

	renderPage(w, indexTmpl, items, "Profiles", "Bambu Profile Manager", "", "profiles", "")
}

var liveProfileTmpl = template.Must(template.New("liveProfile").Parse(`
<p><a href="/" class="text-sm text-zinc-500 hover:text-zinc-800">&larr; All profiles</a></p>
<div class="flex items-start gap-3 bg-blue-50 border border-blue-200 text-blue-800 rounded-xl px-4 py-3 text-sm">
  <span>&#8505;</span>
  <div>
    <p class="font-medium">Not tracked yet</p>
    <p class="text-blue-700/80">This is a live read straight from your Bambu Studio directory. Copy it to another printer or Import a bundle to start recording revision history and unlock Compare.</p>
  </div>
</div>
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-4">
  <div class="flex items-center gap-3">
    {{if .Color}}<span class="w-6 h-6 rounded-full border border-zinc-200 shrink-0" style="background:{{.Color}}"></span>{{end}}
    <h2 class="text-sm font-semibold">{{.Name}}</h2>
  </div>
  {{if .Summary}}
  <dl class="grid grid-cols-2 gap-x-6 gap-y-2 text-sm">
  {{range .Summary}}<div class="flex justify-between border-b border-zinc-50 pb-1"><dt class="text-zinc-500">{{.Label}}</dt><dd class="font-medium">{{.Value}}</dd></div>{{end}}
  </dl>
  {{end}}
  <details class="text-sm" open>
    <summary class="cursor-pointer text-zinc-500 hover:text-zinc-800">All settings</summary>
    <div class="mt-3 space-y-4">
    {{range .Groups}}
    <div>
      <h4 class="text-xs font-semibold text-zinc-400 uppercase tracking-wide mb-1.5">{{.Title}}</h4>
      <div class="rounded-xl border border-zinc-100 divide-y divide-zinc-50">
      {{range .Rows}}<div class="flex justify-between px-3 py-1.5 text-xs"><span class="text-zinc-500">{{.Key}}</span><span class="font-mono break-all text-right">{{.Value}}</span></div>{{end}}
      </div>
    </div>
    {{end}}
    </div>
  </details>
</section>
<a href="/copy" class="inline-block text-sm font-medium text-emerald-600 hover:text-emerald-700">Copy to another printer &rarr;</a>
`))

func (s *Server) handleLiveProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	set, err := resolver.LoadDirs(append([]string{s.UserDir}, s.SystemDirs...))
	if err != nil {
		httpError(w, err)
		return
	}
	leaf, ok := set[name]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	effective, _, err := resolver.Resolve(set, leaf)
	if err != nil {
		http.Error(w, "resolve: "+err.Error(), http.StatusInternalServerError)
		return
	}

	data := struct {
		Name    string
		Summary []summaryField
		Color   string
		Groups  []fieldGroup
	}{Name: name, Summary: profileSummary(effective.Fields), Color: profileColor(effective.Fields), Groups: groupedFields(effective.Fields)}
	renderPage(w, liveProfileTmpl, data, name, name, "", "profiles", "")
}

var profileTmpl = template.Must(template.New("profile").Parse(`
<p><a href="/" class="text-sm text-zinc-500 hover:text-zinc-800">&larr; All profiles</a></p>

<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-4">
  <div class="flex items-center justify-between">
    <div class="flex items-center gap-3">
      {{if .Color}}<span class="w-6 h-6 rounded-full border border-zinc-200 shrink-0" style="background:{{.Color}}"></span>{{end}}
      <h2 class="text-sm font-semibold">{{.Profile.Name}}</h2>
    </div>
    <span class="text-xs font-medium px-2.5 py-1 rounded-full bg-zinc-100 text-zinc-600">rev {{.Latest.Revision}}</span>
  </div>
  {{if .Summary}}
  <dl class="grid grid-cols-2 gap-x-6 gap-y-2 text-sm">
  {{range .Summary}}<div class="flex justify-between border-b border-zinc-50 pb-1"><dt class="text-zinc-500">{{.Label}}</dt><dd class="font-medium">{{.Value}}</dd></div>{{end}}
  </dl>
  {{end}}
  <p class="text-xs text-zinc-400">semantic hash <code class="bg-zinc-100 px-1.5 py-0.5 rounded">{{.Latest.SemanticHash}}</code> &middot; saved {{.Latest.CreatedAt.Format "Jan 2, 2006 15:04"}}</p>
  <details class="text-sm" open>
    <summary class="cursor-pointer text-zinc-500 hover:text-zinc-800">All settings</summary>
    <div class="mt-3 space-y-4">
    {{range .Groups}}
    <div>
      <h4 class="text-xs font-semibold text-zinc-400 uppercase tracking-wide mb-1.5">{{.Title}}</h4>
      <div class="rounded-xl border border-zinc-100 divide-y divide-zinc-50">
      {{range .Rows}}<div class="flex justify-between px-3 py-1.5 text-xs"><span class="text-zinc-500">{{.Key}}</span><span class="font-mono break-all text-right">{{.Value}}</span></div>{{end}}
      </div>
    </div>
    {{end}}
    </div>
  </details>
</section>

<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6">
  <h2 class="text-sm font-medium text-zinc-500 mb-3">All revisions</h2>
  <p class="text-xs text-zinc-400 mb-3">Every publish or import creates a new, immutable revision &mdash; nothing is overwritten.</p>
  <ul class="space-y-1.5 text-sm mb-4">
  {{range .All}}<li class="flex items-center justify-between">
    <span>Revision {{.Revision}} &middot; <span class="text-zinc-400 text-xs">{{.CreatedAt.Format "Jan 2, 2006 15:04"}}</span></span>
    <code class="text-xs text-zinc-400">{{.SemanticHash}}</code>
  </li>{{end}}
  </ul>
  <a href="/compare?item={{.Profile.ID}}:{{.Latest.Revision}}" class="inline-block text-xs font-medium text-emerald-600 hover:text-emerald-700 border-t border-zinc-100 pt-3 block">Compare with another revision or profile &rarr;</a>
</section>

<a href="/profiles/{{.Profile.ID}}/deployments" class="inline-block text-sm font-medium text-emerald-600 hover:text-emerald-700">Deployment history &rarr;</a>
`))

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	p, err := s.Svc.Repo.Profiles().Get(ctx, id)
	if err != nil {
		httpError(w, err)
		return
	}
	latest, err := s.Svc.Repo.Versions().Latest(ctx, id)
	if err != nil {
		httpError(w, err)
		return
	}
	all, err := s.Svc.Repo.Versions().List(ctx, id)
	if err != nil {
		httpError(w, err)
		return
	}
	var fields map[string]any
	if err := json.Unmarshal(latest.ResolvedJSON, &fields); err != nil {
		httpError(w, err)
		return
	}

	data := struct {
		Profile any
		Latest  any
		All     any
		Summary []summaryField
		Color   string
		Groups  []fieldGroup
	}{Profile: p, Latest: latest, All: all, Summary: profileSummary(fields), Color: profileColor(fields), Groups: groupedFields(fields)}
	renderPage(w, profileTmpl, data, p.Name, p.Name, "", "profiles", "")
}

var deploymentsTmpl = template.Must(template.New("deployments").Parse(`
<p><a href="/profiles/{{.Profile.ID}}" class="text-sm text-zinc-500 hover:text-zinc-800">&larr; {{.Profile.Name}}</a></p>
<p class="text-sm text-zinc-500">Every attempt to publish this profile into Bambu Studio, in order, with the exact state-machine transitions it went through &mdash; nothing here is claimed without the app actually having checked it.</p>
{{range .Deployments}}
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6">
  <div class="flex items-center justify-between mb-3">
    <h2 class="text-sm font-medium text-zinc-500">Revision {{.Revision}}</h2>
    <a href="/deployments/{{.ID}}" class="text-xs font-medium px-2.5 py-1 rounded-full bg-blue-100 text-blue-700 hover:bg-blue-200">{{.State}}</a>
  </div>
  <ol class="relative border-l border-zinc-200 ml-2 space-y-3">
  {{range .History}}<li class="ml-4">
    <span class="absolute -left-[5px] w-2.5 h-2.5 rounded-full bg-emerald-500 ring-4 ring-white"></span>
    <p class="text-sm font-medium">{{.From}} &rarr; {{.To}}</p>
    {{if .Reason}}<p class="text-xs text-zinc-400">{{.Reason}}</p>{{end}}
  </li>{{end}}
  </ol>
</section>
{{else}}<div class="text-center py-16 text-zinc-400"><p class="text-sm">No deployments yet.</p></div>{{end}}
`))

func (s *Server) handleDeployments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	p, err := s.Svc.Repo.Profiles().Get(ctx, id)
	if err != nil {
		httpError(w, err)
		return
	}
	deployments, err := s.Svc.Repo.Deployments().ListByProfile(ctx, id)
	if err != nil {
		httpError(w, err)
		return
	}

	data := struct {
		Profile     any
		Deployments any
	}{Profile: p, Deployments: deployments}
	renderPage(w, deploymentsTmpl, data, "Deployments", "Deployment history: "+p.Name, "", "profiles", "")
}

var importFormTmpl = template.Must(template.New("import").Parse(`
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-4">
  <p class="text-sm text-zinc-500">A .profilepack is a portable export of one filament profile plus its whole dependency
    chain (design.md §11) &mdash; it works even if the machine it came from is gone. Importing records it as this
    profile's next revision; nothing is written to Bambu Studio yet.</p>
<form method="post" action="/import" enctype="multipart/form-data" class="space-y-4">
  <label class="block">
    <span class="text-xs font-medium text-zinc-500 mb-1 block">.profilepack bundle</span>
    <input type="file" name="bundle" accept=".profilepack,.zip" required
      class="w-full text-sm rounded-lg border border-dashed border-zinc-300 px-3 py-6 file:mr-4 file:py-2 file:px-3 file:rounded-lg file:border-0 file:bg-zinc-900 file:text-white file:text-sm">
  </label>
  <button type="submit" class="px-4 py-2 rounded-lg bg-zinc-900 text-white text-sm font-medium hover:bg-zinc-800 transition">Import</button>
</form>
</section>
`))

func (s *Server) handleImportForm(w http.ResponseWriter, r *http.Request) {
	renderPage(w, importFormTmpl, nil, "Import", "Import a bundle", "Bring a .profilepack exported elsewhere into this library.", "import", "")
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	file, _, err := r.FormFile("bundle")
	if err != nil {
		http.Error(w, "import: missing bundle file: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	b, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "import: read upload: "+err.Error(), http.StatusInternalServerError)
		return
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		http.Error(w, "import: not a valid .profilepack: "+err.Error(), http.StatusBadRequest)
		return
	}

	profile, _, err := s.Svc.ImportBundle(r.Context(), zr)
	if err != nil {
		http.Error(w, "import: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	http.Redirect(w, r, "/profiles/"+profile.ID, http.StatusSeeOther)
}

func httpError(w http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	http.Error(w, fmt.Sprintf("error: %v", err), http.StatusInternalServerError)
}
