package webui

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"

	"github.com/syscod3/bambu-profile-manager/internal/bambuadapter"
	"github.com/syscod3/bambu-profile-manager/internal/rebind"
	"github.com/syscod3/bambu-profile-manager/internal/reconcile"
	"github.com/syscod3/bambu-profile-manager/internal/resolver"
	"github.com/syscod3/bambu-profile-manager/internal/service"
	"github.com/syscod3/bambu-profile-manager/internal/storage"
)

// studioWarning checks whether Bambu Studio looks like it's running right
// now (the same best-effort pgrep check bambuadapter.Publish enforces) and
// returns a callout banner if so, or if the check itself failed (decisions
// #4: an unknown result must not be silently treated as "not running").
// Empty when Studio is confirmed closed.
func (s *Server) studioWarning() template.HTML {
	if s.Svc == nil || s.Svc.Adapter == nil {
		return ""
	}
	checker := s.Svc.Adapter.IsStudioRunning
	if checker == nil {
		checker = bambuadapter.PgrepStudioRunning
	}
	running, err := checker()
	if err != nil {
		return template.HTML(`<div style="background:#fff3cd;border:1px solid #cc9a06;padding:0.75em;margin-bottom:1em">` +
			`&#9888; Could not determine whether Bambu Studio is running (` + template.HTMLEscapeString(err.Error()) + `). ` +
			`Publishing will refuse on its own if it turns out to be open.</div>`)
	}
	if running {
		return template.HTML(`<div style="background:#f8d7da;border:1px solid #dc3545;padding:0.75em;margin-bottom:1em">` +
			`<strong>&#9888; Bambu Studio appears to be running.</strong> Publishing requires it closed first. Close Studio, then continue.</div>`)
	}
	return ""
}

var copyFormTmpl = template.Must(template.New("copyForm").Parse(`<!doctype html>
<html><head><title>Copy to another printer</title></head><body>
<p><a href="/">&larr; All profiles</a></p>
{{.Warning}}
<h1>Copy a filament profile to another printer</h1>
<form method="post" action="/copy/preview">
<p>Profile:
<select name="name" required>
{{range .Names}}<option value="{{.}}">{{.}}</option>{{end}}
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
	data := struct {
		Names   []string
		Warning template.HTML
	}{Names: names, Warning: s.studioWarning()}
	renderOrError(w, copyFormTmpl, data)
}

var copyPreviewTmpl = template.Must(template.New("copyPreview").Parse(`<!doctype html>
<html><head><title>Copy preview</title></head><body>
<p><a href="/copy">&larr; Start over</a></p>
{{.Warning}}
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
		Warning      template.HTML
	}{Name: name, PrinterToken: printerToken, Candidates: candidates, Warning: s.studioWarning()}
	renderOrError(w, copyPreviewTmpl, data)
}

var studioBlockedTmpl = template.Must(template.New("studioBlocked").Parse(`<!doctype html>
<html><head><title>Bambu Studio is open</title></head><body>
<p><a href="/copy">&larr; Back</a></p>
<div style="background:#f8d7da;border:1px solid #dc3545;padding:1em">
<h1>&#9888; Publish refused: Bambu Studio is running</h1>
<p>Publishing writes directly into Bambu Studio's profile directory and needs it closed first. Close Bambu Studio, then submit again — nothing was written.</p>
</div>
</body></html>`))

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
	if errors.Is(err, bambuadapter.ErrStudioRunning) {
		var buf bytes.Buffer
		if tmplErr := studioBlockedTmpl.Execute(&buf, nil); tmplErr == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnprocessableEntity)
			buf.WriteTo(w)
			return
		}
	}
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
{{.Warning}}
<h1>Backups</h1>
<p>Point-in-time snapshots taken automatically before every publish.</p>
<ul>
{{range .List}}
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
	data := struct {
		List    any
		Warning template.HTML
	}{List: list, Warning: s.studioWarning()}
	renderOrError(w, backupsTmpl, data)
}

func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.Svc.RestoreBackup(name); err != nil {
		httpError(w, err)
		return
	}
	http.Redirect(w, r, "/backups", http.StatusSeeOther)
}
