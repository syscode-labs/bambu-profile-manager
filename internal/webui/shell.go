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
<script src="https://cdn.tailwindcss.com"></script>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
<style>body{font-family:'Inter',system-ui,sans-serif}::-webkit-scrollbar{width:8px}::-webkit-scrollbar-thumb{background:#d4d4d8;border-radius:4px}</style>
</head>
<body class="bg-zinc-50 text-zinc-900 min-h-screen flex">

<aside class="w-60 shrink-0 bg-zinc-900 text-zinc-300 flex flex-col fixed inset-y-0">
  <div class="px-5 py-5 flex items-center gap-2 border-b border-zinc-800">
    <div class="w-8 h-8 rounded-lg bg-emerald-500 flex items-center justify-center text-zinc-900 font-bold text-sm">B</div>
    <span class="text-white font-semibold text-sm">Profile Manager</span>
  </div>
  <nav class="flex-1 px-3 py-4 space-y-1 text-sm">
    <a class="flex items-center gap-3 px-3 py-2 rounded-lg transition {{if eq .Active "profiles"}}bg-zinc-800 text-white font-medium{{else}}text-zinc-400 hover:bg-zinc-800 hover:text-white{{end}}" href="/">
      <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 6h16M4 12h16M4 18h7"/></svg>
      Profiles
    </a>
    <a class="flex items-center gap-3 px-3 py-2 rounded-lg transition {{if eq .Active "import"}}bg-zinc-800 text-white font-medium{{else}}text-zinc-400 hover:bg-zinc-800 hover:text-white{{end}}" href="/import">
      <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v12m0 0l-4-4m4 4l4-4M4 20h16"/></svg>
      Import bundle
    </a>
    <a class="flex items-center gap-3 px-3 py-2 rounded-lg transition {{if eq .Active "copy"}}bg-zinc-800 text-white font-medium{{else}}text-zinc-400 hover:bg-zinc-800 hover:text-white{{end}}" href="/copy">
      <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 7h12m0 0l-4-4m4 4l-4 4M16 17H4m0 0l4 4m-4-4l4-4"/></svg>
      Copy to Printer
    </a>
    <a class="flex items-center gap-3 px-3 py-2 rounded-lg transition {{if eq .Active "compare"}}bg-zinc-800 text-white font-medium{{else}}text-zinc-400 hover:bg-zinc-800 hover:text-white{{end}}" href="/compare">
      <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 19V6l7 4-7 4M4 4v16M20 4v16"/></svg>
      Compare
    </a>
    <a class="flex items-center gap-3 px-3 py-2 rounded-lg transition {{if eq .Active "backups"}}bg-zinc-800 text-white font-medium{{else}}text-zinc-400 hover:bg-zinc-800 hover:text-white{{end}}" href="/backups">
      <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z"/></svg>
      Backups
    </a>
  </nav>
  <div class="px-4 py-3 border-t border-zinc-800 text-xs text-zinc-500">bambupm &middot; local</div>
</aside>

<main class="flex-1 ml-60 overflow-y-auto">
  <header class="sticky top-0 z-10 bg-zinc-50/90 backdrop-blur border-b border-zinc-200 px-8 py-4">
    <h1 class="text-lg font-semibold">{{.HeaderTitle}}</h1>
    {{if .HeaderSubtitle}}<p class="text-sm text-zinc-500">{{.HeaderSubtitle}}</p>{{end}}
  </header>
  <div class="px-8 py-6 max-w-3xl space-y-6">
    {{.Warning}}
    {{.Content}}
  </div>
</main>

</body>
</html>`))

// renderPage renders contentTmpl with data, then wraps the result in
// shellTmpl (sidebar/header/warning banner). contentTmpl gets normal
// html/template escaping against data; the shell only ever receives
// already-escaped HTML plus a handful of Go-controlled strings.
func renderPage(w http.ResponseWriter, contentTmpl *template.Template, data any, title, headerTitle, headerSubtitle, active string, warning template.HTML) {
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
	}{
		Title: title, HeaderTitle: headerTitle, HeaderSubtitle: headerSubtitle,
		Active: active, Warning: warning, Content: template.HTML(contentBuf.String()),
	}

	var buf bytes.Buffer
	if err := shellTmpl.Execute(&buf, shellData); err != nil {
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}
