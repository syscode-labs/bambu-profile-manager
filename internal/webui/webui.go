// Package webui is the local web UI (design.md §18), server-rendered REST +
// plain page reloads — no WebSocket, no client-side framework, per
// decisions.md #7 ("ship the UI from Phase 1, defer cache/WebSocket infra
// until real usage shows a need"). Pages are plain html/template, not
// templ/HTMX: with page reloads instead of partial updates there's nothing
// HTMX would add yet, so it's deferred along with the rest of the deferred
// infra rather than added unused.
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

var indexTmpl = template.Must(template.New("index").Parse(`<!doctype html>
<html><head><title>Bambu Profile Manager</title></head><body>
<h1>Profiles</h1>
<p>
<a href="/import">Import a .profilepack bundle</a> &middot;
<a href="/copy">Copy to another printer</a> &middot;
<a href="/backups">Backups</a>
</p>
<ul>
{{range .}}<li><a href="/profiles/{{.ID}}">{{.Name}}</a></li>{{else}}<li>No profiles yet.</li>{{end}}
</ul>
</body></html>`))

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.Svc.Repo.Profiles().List(r.Context())
	if err != nil {
		httpError(w, err)
		return
	}
	renderOrError(w, indexTmpl, profiles)
}

var profileTmpl = template.Must(template.New("profile").Parse(`<!doctype html>
<html><head><title>{{.Profile.Name}}</title></head><body>
<p><a href="/">&larr; All profiles</a></p>
<h1>{{.Profile.Name}}</h1>
<h2>Latest version</h2>
<p>Revision {{.Latest.Revision}} &middot; semantic hash <code>{{.Latest.SemanticHash}}</code></p>
<h3>Resolved (effective) profile</h3>
<pre>{{.ResolvedJSON}}</pre>
<h3>All revisions</h3>
<ul>
{{range .All}}<li>Revision {{.Revision}}: <code>{{.SemanticHash}}</code></li>{{end}}
</ul>
<p><a href="/profiles/{{.Profile.ID}}/deployments">Deployment history &rarr;</a></p>
</body></html>`))

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
	renderOrError(w, profileTmpl, data)
}

var deploymentsTmpl = template.Must(template.New("deployments").Parse(`<!doctype html>
<html><head><title>Deployments: {{.Profile.Name}}</title></head><body>
<p><a href="/profiles/{{.Profile.ID}}">&larr; {{.Profile.Name}}</a></p>
<h1>Deployment history</h1>
{{range .Deployments}}
<h3>Revision {{.Revision}} &mdash; {{.State}}</h3>
<ul>
{{range .History}}<li>{{.At.Format "2006-01-02 15:04:05"}}: {{.From}} &rarr; {{.To}}{{if .Reason}} ({{.Reason}}){{end}}</li>{{end}}
</ul>
{{else}}<p>No deployments yet.</p>{{end}}
</body></html>`))

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
	renderOrError(w, deploymentsTmpl, data)
}

var importFormTmpl = template.Must(template.New("import").Parse(`<!doctype html>
<html><head><title>Import a bundle</title></head><body>
<p><a href="/">&larr; All profiles</a></p>
<h1>Import a .profilepack bundle</h1>
<form method="post" action="/import" enctype="multipart/form-data">
<input type="file" name="bundle" accept=".profilepack,.zip" required>
<button type="submit">Import</button>
</form>
</body></html>`))

func (s *Server) handleImportForm(w http.ResponseWriter, r *http.Request) {
	renderOrError(w, importFormTmpl, nil)
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

func renderOrError(w http.ResponseWriter, tmpl *template.Template, data any) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func httpError(w http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	http.Error(w, fmt.Sprintf("error: %v", err), http.StatusInternalServerError)
}
