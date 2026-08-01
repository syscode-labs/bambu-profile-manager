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
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"

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
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /profiles/{id}", s.handleProfile)
	mux.HandleFunc("GET /profiles/{id}/deployments", s.handleDeployments)
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
{{if .}}
<div class="bg-white rounded-2xl border border-zinc-200 shadow-sm divide-y divide-zinc-100">
{{range .}}<a href="/profiles/{{.ID}}" class="flex items-center justify-between px-5 py-3.5 hover:bg-zinc-50 transition first:rounded-t-2xl last:rounded-b-2xl">
  <span class="text-sm font-medium text-zinc-800">{{.Name}}</span>
  <svg class="w-4 h-4 text-zinc-300" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/></svg>
</a>{{end}}
</div>
{{else}}
<div class="text-center py-16 text-zinc-400">
  <p class="text-sm">No profiles yet.</p>
  <a href="/import" class="inline-block mt-3 text-sm font-medium text-emerald-600 hover:text-emerald-700">Import a bundle &rarr;</a>
</div>
{{end}}`))

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.Svc.Repo.Profiles().List(r.Context())
	if err != nil {
		httpError(w, err)
		return
	}
	renderPage(w, indexTmpl, profiles, "Profiles", "Profiles", "Everything bambupm knows about, by canonical name.", "profiles", "")
}

var profileTmpl = template.Must(template.New("profile").Parse(`
<p><a href="/" class="text-sm text-zinc-500 hover:text-zinc-800">&larr; All profiles</a></p>
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-3">
  <div class="flex items-center justify-between">
    <h2 class="text-sm font-medium text-zinc-500">Latest version</h2>
    <span class="text-xs font-medium px-2.5 py-1 rounded-full bg-zinc-100 text-zinc-600">rev {{.Latest.Revision}}</span>
  </div>
  <p class="text-xs text-zinc-500">semantic hash <code class="bg-zinc-100 px-1.5 py-0.5 rounded">{{.Latest.SemanticHash}}</code></p>
  <details class="text-sm">
    <summary class="cursor-pointer text-zinc-500 hover:text-zinc-800">Resolved (effective) profile</summary>
    <pre class="mt-2 bg-zinc-950 text-zinc-200 text-xs p-4 rounded-xl overflow-x-auto">{{.ResolvedJSON}}</pre>
  </details>
</section>
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6">
  <h2 class="text-sm font-medium text-zinc-500 mb-3">All revisions</h2>
  <ul class="space-y-1.5 text-sm">
  {{range .All}}<li class="flex items-center justify-between"><span>Revision {{.Revision}}</span><code class="text-xs text-zinc-400">{{.SemanticHash}}</code></li>{{end}}
  </ul>
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

	data := struct {
		Profile      any
		Latest       any
		All          any
		ResolvedJSON string
	}{Profile: p, Latest: latest, All: all, ResolvedJSON: string(latest.ResolvedJSON)}
	renderPage(w, profileTmpl, data, p.Name, p.Name, "", "profiles", "")
}

var deploymentsTmpl = template.Must(template.New("deployments").Parse(`
<p><a href="/profiles/{{.Profile.ID}}" class="text-sm text-zinc-500 hover:text-zinc-800">&larr; {{.Profile.Name}}</a></p>
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
