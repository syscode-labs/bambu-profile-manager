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

var copyFormTmpl = template.Must(template.New("copyForm").Parse(`
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-4">
  <div class="flex items-center gap-2 text-sm font-medium text-zinc-500">
    <span class="w-5 h-5 rounded-full bg-zinc-900 text-white text-xs flex items-center justify-center shrink-0">1</span>
    Choose a profile and target printer
  </div>
  <p class="text-xs text-zinc-500 -mt-2">
    Pick a filament you already have, and the printer you want it usable under. bambupm looks for a parent profile
    that matches both the same material (ABS, PLA, ...) and your target printer &mdash; the same lookup Bambu Studio
    itself would need, done for you. Your settings (color, vendor, temps) carry over either way.
  </p>
<form method="post" action="/copy/preview" class="space-y-4">
  <div class="grid grid-cols-2 gap-4">
    <label class="block">
      <span class="text-xs font-medium text-zinc-500 mb-1 block">Profile</span>
      <select name="name" required class="w-full rounded-lg border border-zinc-300 px-3 py-2 text-sm focus:ring-2 focus:ring-emerald-500 focus:border-emerald-500 outline-none">
      {{range .Names}}<option value="{{.}}">{{.}}</option>{{end}}
      </select>
    </label>
    <label class="block">
      <span class="text-xs font-medium text-zinc-500 mb-1 block">Target printer (e.g. "P1S" or "P1S 0.4" to pin the nozzle)</span>
      <input type="text" name="printer_token" required placeholder="P1S 0.4"
        class="w-full rounded-lg border border-zinc-300 px-3 py-2 text-sm focus:ring-2 focus:ring-emerald-500 focus:border-emerald-500 outline-none">
      <span class="text-xs text-zinc-400 mt-1 block">Just the printer works ("P1S"); add the nozzle size ("P1S 0.4") if that alone leaves more than one match.</span>
    </label>
  </div>
  <button type="submit" class="px-4 py-2 rounded-lg bg-zinc-900 text-white text-sm font-medium hover:bg-zinc-800 transition">Find match</button>
</form>
</section>
`))

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
	data := struct{ Names []string }{Names: names}
	renderPage(w, copyFormTmpl, data, "Copy", "Copy a filament profile to another printer", "Rebind without touching dependency chains yourself.", "copy", s.studioWarning())
}

var copyPreviewTmpl = template.Must(template.New("copyPreview").Parse(`
<p><a href="/copy" class="text-sm text-zinc-500 hover:text-zinc-800">&larr; Start over</a></p>
{{if not .Candidates}}
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6">
  <p class="text-sm text-zinc-500">No matching parent found under "{{.PrinterToken}}" for this profile's material. Nothing safe to auto-map.</p>
</section>
{{else if gt (len .Candidates) 1}}
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-4">
  <div class="flex items-center gap-2 text-sm font-medium text-zinc-500">
    <span class="w-5 h-5 rounded-full bg-zinc-900 text-white text-xs flex items-center justify-center shrink-0">2</span>
    Pick a parent
  </div>
  <p class="text-sm text-zinc-500">{{len .Candidates}} profiles match both the material and "{{.PrinterToken}}" &mdash; this tool won't guess between them. Add more of the printer name (e.g. the nozzle size) on the previous step to narrow it to one, or just pick below.</p>
  {{$name := .Name}}{{$token := .PrinterToken}}
  {{range .Candidates}}
  <form method="post" action="/copy/publish" class="border border-zinc-200 rounded-xl p-4 space-y-3">
    <input type="hidden" name="name" value="{{$name}}">
    <input type="hidden" name="parent" value="{{.}}">
    <p class="text-sm font-medium">{{.}}</p>
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
  <form method="post" action="/copy/publish" class="space-y-4">
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
	renderPage(w, copyPreviewTmpl, data, "Copy preview", "Copy preview: "+name, "&rarr; "+printerToken, "copy", s.studioWarning())
}

var copyResultTmpl = template.Must(template.New("copyResult").Parse(`
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-4">
  <div class="flex items-center gap-2">
    <span class="text-xs font-medium px-2.5 py-1 rounded-full bg-blue-100 text-blue-700">{{.Deployment.State}}</span>
  </div>
  <p class="text-sm text-zinc-500">Parent: <code class="bg-zinc-100 px-1.5 py-0.5 rounded">{{.Parent}}</code> &middot; Name: <code class="bg-zinc-100 px-1.5 py-0.5 rounded">{{.Name}}</code></p>
  {{if .Snapshot}}<p class="text-sm text-zinc-500">Backup taken: <code class="bg-zinc-100 px-1.5 py-0.5 rounded">{{.Snapshot}}</code></p>{{end}}
  <ol class="relative border-l border-zinc-200 ml-2 space-y-3">
  {{range .Deployment.History}}<li class="ml-4">
    <span class="absolute -left-[5px] w-2.5 h-2.5 rounded-full bg-emerald-500 ring-4 ring-white"></span>
    <p class="text-sm font-medium">{{.From}} &rarr; {{.To}}</p>
    {{if .Reason}}<p class="text-xs text-zinc-400">{{.Reason}}</p>{{end}}
  </li>{{end}}
  </ol>
  <a href="/deployments/{{.Deployment.ID}}" class="inline-block text-sm font-medium text-emerald-600 hover:text-emerald-700">Deployment detail &rarr;</a>
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
		w.WriteHeader(http.StatusUnprocessableEntity)
		var contentBuf bytes.Buffer
		studioBlockedTmpl.Execute(&contentBuf, nil)
		shellData := struct {
			Title, HeaderTitle, HeaderSubtitle, Active string
			Warning, Content                           template.HTML
		}{Title: "Publish blocked", HeaderTitle: "Publish blocked", Active: "copy", Content: template.HTML(contentBuf.String())}
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
		Deployment any
		Parent     string
		Name       string
		Snapshot   string
	}{Deployment: result.Deployment, Parent: parent, Name: confirmName, Snapshot: result.Snapshot}
	renderPage(w, copyResultTmpl, data, "Copy result", "Copy result", "", "copy", "")
}

var deploymentDetailTmpl = template.Must(template.New("deploymentDetail").Parse(`
<p class="text-sm text-zinc-500">A deployment only reaches <code class="bg-zinc-100 px-1 rounded">ACTIVE</code> after Bambu Studio has actually
  confirmed it and the settings still match &mdash; not just because a file was written.</p>
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-4">
  <div class="flex items-center justify-between">
    <span class="text-xs font-medium px-2.5 py-1 rounded-full bg-blue-100 text-blue-700">{{.Deployment.State}}</span>
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
  <form method="post" action="/deployments/{{.Deployment.ID}}/check" class="border-t border-zinc-100 pt-4">
    <p class="text-sm text-zinc-500 mb-3">Reopen Bambu Studio, select the profile, and Save it once &mdash; the confirmed trigger (reopening/selecting/slicing alone don't bump the metadata Studio uses).</p>
    <button type="submit" class="px-4 py-2 rounded-lg border border-zinc-300 text-sm font-medium hover:bg-zinc-50 transition">Check recognition</button>
  </form>
  {{end}}
</section>
`))

func (s *Server) handleDeploymentDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dep, err := s.Svc.Repo.Deployments().Get(r.Context(), id)
	if err != nil {
		httpError(w, err)
		return
	}
	data := struct{ Deployment any }{Deployment: dep}
	renderPage(w, deploymentDetailTmpl, data, "Deployment", "Deployment "+id, "", "", "")
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
	renderPage(w, backupsTmpl, data, "Backups", "Backups", "Point-in-time snapshots taken automatically before every publish.", "backups", s.studioWarning())
}

func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.Svc.RestoreBackup(name); err != nil {
		httpError(w, err)
		return
	}
	http.Redirect(w, r, "/backups", http.StatusSeeOther)
}
