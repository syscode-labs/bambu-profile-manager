package webui

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"

	"github.com/syscod3/bambu-profile-manager/internal/rebind"
	"github.com/syscod3/bambu-profile-manager/internal/reconcile"
	"github.com/syscod3/bambu-profile-manager/internal/resolver"
	"github.com/syscod3/bambu-profile-manager/internal/service"
	"github.com/syscod3/bambu-profile-manager/internal/storage"
)

var copyFormTmpl = template.Must(template.New("copyForm").Parse(`<!doctype html>
<html><head><title>Copy to another printer</title></head><body>
<p><a href="/">&larr; All profiles</a></p>
<h1>Copy a filament profile to another printer</h1>
<form method="post" action="/copy/preview">
<p>Profile:
<select name="name" required>
{{range .}}<option value="{{.}}">{{.}}</option>{{end}}
</select></p>
<p>Target printer (e.g. "P1S" or "P1S 0.4" to also pin the nozzle):
<input type="text" name="printer_token" required></p>
<button type="submit">Find match</button>
</form>
</body></html>`))

func (s *Server) handleCopyForm(w http.ResponseWriter, r *http.Request) {
	set, err := resolver.LoadDirs([]string{s.UserDir})
	if err != nil {
		httpError(w, err)
		return
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	renderOrError(w, copyFormTmpl, names)
}

var copyPreviewTmpl = template.Must(template.New("copyPreview").Parse(`<!doctype html>
<html><head><title>Copy preview</title></head><body>
<p><a href="/copy">&larr; Start over</a></p>
<h1>Copy preview: {{.Name}} &rarr; {{.PrinterToken}}</h1>
{{if not .Candidates}}
<p>No matching parent found under "{{.PrinterToken}}" for this profile's material. Nothing safe to auto-map.</p>
{{else if gt (len .Candidates) 1}}
<p>{{len .Candidates}} plausible parents found — pick one, this tool won't guess:</p>
{{range .Candidates}}
<form method="post" action="/copy/publish" style="margin-bottom:1em">
<input type="hidden" name="name" value="{{$.Name}}">
<input type="hidden" name="parent" value="{{.}}">
<p><strong>{{.}}</strong></p>
<p>New profile name: <input type="text" name="confirm_name" value="{{$.Name}} @{{$.PrinterToken}}" required></p>
<button type="submit">Publish with this parent</button>
</form>
{{end}}
{{else}}
<p>Match: <strong>{{index .Candidates 0}}</strong></p>
<form method="post" action="/copy/publish">
<input type="hidden" name="name" value="{{.Name}}">
<input type="hidden" name="parent" value="{{index .Candidates 0}}">
<p>New profile name: <input type="text" name="confirm_name" value="{{.Name}} @{{.PrinterToken}}" required></p>
<button type="submit">Publish</button>
</form>
{{end}}
</body></html>`))

func (s *Server) handleCopyPreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "copy: parse form: "+err.Error(), http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	printerToken := r.FormValue("printer_token")
	if name == "" || printerToken == "" {
		http.Error(w, "copy: name and printer_token are required", http.StatusBadRequest)
		return
	}

	set, err := resolver.LoadDirs(append([]string{s.UserDir}, s.SystemDirs...))
	if err != nil {
		httpError(w, err)
		return
	}
	leaf, ok := set[name]
	if !ok {
		http.Error(w, fmt.Sprintf("copy: %q not found", name), http.StatusNotFound)
		return
	}
	candidates, err := rebind.FindCandidateParents(set, set, leaf, printerToken)
	if err != nil {
		httpError(w, err)
		return
	}

	data := struct {
		Name         string
		PrinterToken string
		Candidates   []string
	}{Name: name, PrinterToken: printerToken, Candidates: candidates}
	renderOrError(w, copyPreviewTmpl, data)
}

var copyResultTmpl = template.Must(template.New("copyResult").Parse(`<!doctype html>
<html><head><title>Copy result</title></head><body>
<p><a href="/">&larr; All profiles</a></p>
<h1>{{.Deployment.State}}</h1>
<p>Parent: <code>{{.Parent}}</code> &middot; Name: <code>{{.Name}}</code></p>
{{if .Snapshot}}<p>Backup taken: <code>{{.Snapshot}}</code></p>{{end}}
<ul>
{{range .Deployment.History}}<li>{{.From}} &rarr; {{.To}}{{if .Reason}} ({{.Reason}}){{end}}</li>{{end}}
</ul>
<p><a href="/deployments/{{.Deployment.ID}}">Deployment detail &rarr;</a></p>
</body></html>`))

func (s *Server) handleCopyPublish(w http.ResponseWriter, r *http.Request) {
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

	set, err := resolver.LoadDirs(append([]string{s.UserDir}, s.SystemDirs...))
	if err != nil {
		httpError(w, err)
		return
	}
	leaf, ok := set[name]
	if !ok {
		http.Error(w, fmt.Sprintf("copy: %q not found", name), http.StatusNotFound)
		return
	}

	ctx := r.Context()
	profile, err := s.Svc.Repo.Profiles().GetByName(ctx, name)
	if errors.Is(err, storage.ErrNotFound) {
		profile, err = s.Svc.Repo.Profiles().Create(ctx, name)
	}
	if err != nil {
		httpError(w, err)
		return
	}

	result, err := s.Svc.RebindAndPublish(ctx, set, set, leaf, []string{parent}, profile.ID, confirmName,
		reconcile.InfoFields{}, reconcile.InfoFields{})
	if err != nil {
		http.Error(w, "copy: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	renderOrError(w, copyResultTmpl, struct {
		Deployment any
		Parent     string
		Name       string
		Snapshot   string
	}{Deployment: result.Deployment, Parent: parent, Name: confirmName, Snapshot: result.Snapshot})
}

var deploymentDetailTmpl = template.Must(template.New("deploymentDetail").Parse(`<!doctype html>
<html><head><title>Deployment {{.Deployment.ID}}</title></head><body>
<p><a href="/">&larr; All profiles</a></p>
<h1>Deployment {{.Deployment.ID}}</h1>
<p>State: <strong>{{.Deployment.State}}</strong> &middot; Revision {{.Deployment.Revision}}</p>
<ul>
{{range .Deployment.History}}<li>{{.At.Format "2006-01-02 15:04:05"}}: {{.From}} &rarr; {{.To}}{{if .Reason}} ({{.Reason}}){{end}}</li>{{end}}
</ul>
{{if eq (print .Deployment.State) "INSTALLED_LOCALLY"}}
<form method="post" action="/deployments/{{.Deployment.ID}}/check">
<p>Reopen Bambu Studio, select the profile, and Save it once (that's the confirmed trigger — reopening/selecting/slicing alone don't bump the metadata Studio uses). Then:</p>
<button type="submit">Check recognition</button>
</form>
{{end}}
</body></html>`))

func (s *Server) handleDeploymentDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dep, err := s.Svc.Repo.Deployments().Get(r.Context(), id)
	if err != nil {
		httpError(w, err)
		return
	}
	renderOrError(w, deploymentDetailTmpl, struct{ Deployment any }{Deployment: dep})
}

func (s *Server) handleCheckRecognition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	dep, err := s.Svc.Repo.Deployments().Get(ctx, id)
	if err != nil {
		httpError(w, err)
		return
	}
	versions, err := s.Svc.Repo.Versions().List(ctx, dep.ProfileID)
	if err != nil {
		httpError(w, err)
		return
	}
	var resolvedJSON []byte
	for _, v := range versions {
		if v.Revision == dep.Revision {
			resolvedJSON = v.ResolvedJSON
			break
		}
	}
	if resolvedJSON == nil {
		http.Error(w, "check-recognition: no stored version for this revision", http.StatusInternalServerError)
		return
	}
	targetProfile, err := service.TargetProfileFromVersion(resolvedJSON)
	if err != nil {
		httpError(w, err)
		return
	}
	publishedPath := filepath.Join(s.UserDir, targetProfile.Name+".json")
	infoPath := filepath.Join(s.UserDir, targetProfile.Name+".info")

	afterInfo := reconcile.InfoFields{}
	if b, err := os.ReadFile(infoPath); err == nil {
		afterInfo = reconcile.ParseInfo(b)
	} else if !os.IsNotExist(err) {
		httpError(w, err)
		return
	}

	if _, err := s.Svc.CheckRecognition(ctx, id, targetProfile, publishedPath, reconcile.InfoFields{}, afterInfo); err != nil {
		// Still redirect: the deployment was saved with whatever state it
		// reached (e.g. SEMANTIC_MISMATCH is a real, informative outcome,
		// not a request failure) — surfacing it on the detail page is more
		// useful than a raw 500.
	}
	http.Redirect(w, r, "/deployments/"+id, http.StatusSeeOther)
}

var backupsTmpl = template.Must(template.New("backups").Parse(`<!doctype html>
<html><head><title>Backups</title></head><body>
<p><a href="/">&larr; All profiles</a></p>
<h1>Backups</h1>
<p>Point-in-time snapshots taken automatically before every publish.</p>
<ul>
{{range .}}
<li>{{.At.Format "2006-01-02 15:04:05 MST"}} (<code>{{.Name}}</code>)
<form method="post" action="/backups/{{.Name}}/restore" style="display:inline">
<button type="submit" onclick="return confirm('Restore this snapshot? Overwrites files present in it, does not delete anything added since.')">Restore</button>
</form>
</li>
{{else}}<li>No backups yet.</li>{{end}}
</ul>
</body></html>`))

func (s *Server) handleBackupsList(w http.ResponseWriter, r *http.Request) {
	list, err := s.Svc.ListBackups()
	if err != nil {
		httpError(w, err)
		return
	}
	renderOrError(w, backupsTmpl, list)
}

func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.Svc.RestoreBackup(name); err != nil {
		httpError(w, err)
		return
	}
	http.Redirect(w, r, "/backups", http.StatusSeeOther)
}
