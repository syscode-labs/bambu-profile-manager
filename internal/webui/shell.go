package webui

import (
	"bytes"
	"html/template"
	"net/http"
)

// shellTmpl is the page chrome shared by every page: sidebar nav, header,
// and a slot for the Studio-running warning banner. Content is rendered by
// each page's own template first (so it still gets normal html/template
// escaping against its own data), then dropped in here as already-safe
// template.HTML — the same two-pass pattern renderPage below implements.
//
// Tailwind is loaded from the CDN rather than a bundled/compiled build:
// keeps this a single Go binary with no Node.js build step (the same
// constraint that ruled out templ/HTMX and Electron — see decisions.md #7
// and the "prefer 1" call after comparing a Tailwind POC against an
// Electron POC). Trade-off: needs network access to load the page and logs
// a "not for production" console warning: acceptable for a localhost tool
// used by one person, revisit with a self-hosted static build if that ever
// stops being true.
var shellTmpl = template.Must(template.New("shell").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>{{.Title}} &middot; Bambu Profile Manager</title>
<link rel="icon" type="image/png" href="/assets/logo.png">
<script src="https://cdn.tailwindcss.com"></script>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700;800&display=swap" rel="stylesheet">
<style>
  body{font-family:'Inter',system-ui,sans-serif;-webkit-font-smoothing:antialiased}
  ::-webkit-scrollbar{width:8px;height:8px}
  ::-webkit-scrollbar-thumb{background:#d4d4d8;border-radius:4px}
  ::-webkit-scrollbar-thumb:hover{background:#a1a1aa}
  .bpm-sidebar{background:linear-gradient(180deg,#18181b 0%,#0c0c0e 100%)}
  .bpm-glow{box-shadow:0 0 0 1px rgba(16,185,129,.15),0 8px 24px -8px rgba(16,185,129,.35)}
</style>
</head>
<body class="bg-zinc-50 text-zinc-900 min-h-screen flex">

<aside class="bpm-sidebar w-60 shrink-0 text-zinc-300 flex flex-col fixed inset-y-0">
  <a href="/" class="px-5 py-5 flex items-center gap-2.5 border-b border-white/5 hover:bg-white/5 transition" title="Home">
    <img src="/assets/logo.png" alt="" class="w-9 h-9 rounded-xl bpm-glow shrink-0">
    <span class="text-white font-semibold text-sm leading-tight">Bambu<br>Profile Manager</span>
  </a>
  <nav class="flex-1 px-3 py-4 space-y-1 text-sm">
    <a class="flex items-center gap-3 px-3 py-2 rounded-lg transition {{if eq .Active "profiles"}}bg-zinc-800 text-white font-medium{{else}}text-zinc-400 hover:bg-zinc-800/70 hover:text-white{{end}}" href="/">
      <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 6h16M4 12h16M4 18h7"/></svg>
      Profiles
    </a>
    <a class="flex items-center gap-3 px-3 py-2 rounded-lg transition {{if eq .Active "import"}}bg-zinc-800 text-white font-medium{{else}}text-zinc-400 hover:bg-zinc-800/70 hover:text-white{{end}}" href="/import">
      <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v12m0 0l-4-4m4 4l4-4M4 20h16"/></svg>
      Import bundle
    </a>
    <a class="flex items-center gap-3 px-3 py-2 rounded-lg transition {{if eq .Active "copy"}}bg-zinc-800 text-white font-medium{{else}}text-zinc-400 hover:bg-zinc-800/70 hover:text-white{{end}}" href="/copy">
      <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 7h12m0 0l-4-4m4 4l-4 4M16 17H4m0 0l4 4m-4-4l4-4"/></svg>
      Copy to Printer
    </a>
    <a class="flex items-center gap-3 px-3 py-2 rounded-lg transition {{if eq .Active "backups"}}bg-zinc-800 text-white font-medium{{else}}text-zinc-400 hover:bg-zinc-800/70 hover:text-white{{end}}" href="/backups">
      <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/></svg>
      Backups
    </a>
    <a class="flex items-center gap-3 px-3 py-2 rounded-lg transition {{if eq .Active "help"}}bg-zinc-800 text-white font-medium{{else}}text-zinc-400 hover:bg-zinc-800/70 hover:text-white{{end}}" href="/help">
      <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8.228 9c.549-1.165 2.03-2 3.772-2 2.21 0 4 1.343 4 3 0 1.4-1.278 2.575-3.006 2.907-.542.104-.994.54-.994 1.093m0 3h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/></svg>
      Help
    </a>
  </nav>
  <div class="px-4 py-3 border-t border-white/5 text-xs text-zinc-500 space-y-1">
    <p>bpm &middot; local &middot; MIT</p>
    <a href="https://github.com/syscode-labs/bambu-profile-manager" target="_blank" rel="noopener" class="flex items-center gap-1.5 hover:text-zinc-300 transition">
      <svg class="w-3.5 h-3.5 shrink-0" viewBox="0 0 16 16" fill="currentColor"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0016 8c0-4.42-3.58-8-8-8z"/></svg>
      <span>Source on GitHub</span>
    </a>
  </div>
</aside>

<main class="flex-1 ml-60 overflow-y-auto">
  <header class="sticky top-0 z-10 bg-zinc-50/90 backdrop-blur border-b border-zinc-200 px-8 py-4">
    <h1 class="text-lg font-semibold tracking-tight">{{.HeaderTitle}}</h1>
    {{if .HeaderSubtitle}}<p class="text-sm text-zinc-500">{{.HeaderSubtitle}}</p>{{end}}
  </header>
  <div class="px-8 py-6 max-w-3xl space-y-6">
    {{if .WatchStudio}}<div id="studio-warning">{{.Warning}}</div>{{end}}
    {{.Content}}
  </div>
</main>

{{if .WatchStudio}}
<script>
(function() {
  var el = document.getElementById('studio-warning');
  if (!el) return;
  function poll() {
    fetch('/api/studio-status')
      .then(function(r) { return r.ok ? r.text() : Promise.reject(r.status); })
      .then(function(html) { el.innerHTML = html; })
      .catch(function() {});
  }
  setInterval(poll, 5000);
})();
</script>
{{end}}
</body>
</html>`))

// renderPage renders contentTmpl with data, then wraps the result in
// shellTmpl (sidebar/header/warning banner). contentTmpl gets normal
// html/template escaping against data; the shell only ever receives
// already-escaped HTML plus a handful of Go-controlled strings.
//
// watchStudio gates both the initial banner slot AND the client-side
// live-poll script (see shell's <script>): pages where "Bambu Studio is
// running" is actually actionable information (copy, backups — publishing
// needs it closed) pass true; everywhere else passes false, most
// importantly the deployment-detail page, which tells the user to *reopen*
// Studio as its own next step — showing a "Studio is running, close it"
// warning there would directly contradict that instruction (real user
// report: the live-poll banner reappeared there a few seconds after load
// even though this handler never asked for one, because the poller used to
// run unconditionally on every page).
func renderPage(w http.ResponseWriter, contentTmpl *template.Template, data any, title, headerTitle, headerSubtitle, active string, warning template.HTML, watchStudio bool) {
	var contentBuf bytes.Buffer
	if err := contentTmpl.Execute(&contentBuf, data); err != nil {
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
		return
	}

	shellData := struct {
		Title          string
		HeaderTitle    string
		HeaderSubtitle string
		Active         string
		Warning        template.HTML
		Content        template.HTML
		WatchStudio    bool
	}{
		Title: title, HeaderTitle: headerTitle, HeaderSubtitle: headerSubtitle,
		Active: active, Warning: warning, Content: template.HTML(contentBuf.String()), WatchStudio: watchStudio,
	}

	var buf bytes.Buffer
	if err := shellTmpl.Execute(&buf, shellData); err != nil {
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}
