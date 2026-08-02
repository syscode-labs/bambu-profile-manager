package webui

import (
	"html/template"
	"net/http"
)

// helpTmpl is a static, comprehensive walkthrough of the whole app — no
// data binding needed, so handleHelp executes it with nil.
var helpTmpl = template.Must(template.New("help").Parse(`
<section class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-3">
  <p class="text-sm text-zinc-600 leading-relaxed">
    bambupm manages your Bambu Studio <strong>filament</strong> and <strong>process (print)</strong> profiles: it copies
    one to another printer without you having to hand-edit an <code class="bg-zinc-100 px-1 rounded">inherits</code>
    chain, and it <strong>verifies</strong> Bambu Studio actually picked the result up before calling anything done
    &mdash; a file landing on disk is never treated as success by itself.
  </p>
</section>

<nav class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-4">
  <p class="text-xs font-semibold text-zinc-400 uppercase tracking-wide mb-2">On this page</p>
  <div class="grid grid-cols-2 gap-1 text-sm">
    <a href="#copy" class="text-emerald-700 hover:underline">Copying a profile</a>
    <a href="#lifecycle" class="text-emerald-700 hover:underline">Deployment lifecycle</a>
    <a href="#compare" class="text-emerald-700 hover:underline">Compare</a>
    <a href="#backups" class="text-emerald-700 hover:underline">Backups</a>
    <a href="#filament-vs-process" class="text-emerald-700 hover:underline">Filament vs process</a>
    <a href="#studio" class="text-emerald-700 hover:underline">The Bambu Studio requirement</a>
    <a href="#docker" class="text-emerald-700 hover:underline">Running with Docker</a>
    <a href="#cli" class="text-emerald-700 hover:underline">CLI reference</a>
  </div>
</nav>

<section id="copy" class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-3">
  <h2 class="text-base font-semibold">Copying a profile to another printer</h2>
  <ol class="list-decimal list-inside text-sm text-zinc-600 space-y-2 leading-relaxed">
    <li>Open <a href="/copy" class="text-emerald-700 hover:underline">Copy to Printer</a>, pick a profile you already have and the printer you want it usable under.</li>
    <li>bambupm looks for a real base ("parent") profile that already applies to that printer &mdash; for filament, by
      material + printer name; for process, by Bambu's own <code class="bg-zinc-100 px-1 rounded">compatible_printers</code>
      list, since one process profile is often shared across several printer models.</li>
    <li><strong>Exactly one match</strong>: the copy is pre-filled and ready to publish.</li>
    <li><strong>Several matches</strong>: you pick one. If more than one is plausible, bambupm ranks them by how close
      each is to your source profile's own settings and marks the closest "Suggested" &mdash; it never silently guesses
      for you.</li>
    <li><strong>No verified match</strong>: bambupm offers every profile in the same family as a fallback, with a
      settings diff for each, and a checkbox to explicitly add the target printer to whichever one you pick. Nothing
      is published without that explicit confirmation.</li>
    <li>Publishing takes an automatic backup first (see <a href="#backups" class="text-emerald-700 hover:underline">Backups</a>)
      and refuses outright if Bambu Studio looks like it's currently running.</li>
  </ol>
</section>

<section id="lifecycle" class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-3">
  <h2 class="text-base font-semibold">The deployment lifecycle</h2>
  <p class="text-sm text-zinc-600 leading-relaxed">
    Every publish is tracked as a <strong>deployment</strong> that only ever moves forward through real, checked
    states &mdash; it never claims a later state than what's actually been confirmed:
  </p>
  <div class="space-y-2 text-sm">
    <div class="flex items-start gap-3"><span class="text-xs font-medium px-2 py-0.5 rounded-full bg-amber-100 text-amber-700 shrink-0 mt-0.5">STAGED</span><p class="text-zinc-600">Written to a staging area, not yet in Bambu Studio's live directory.</p></div>
    <div class="flex items-start gap-3"><span class="text-xs font-medium px-2 py-0.5 rounded-full bg-amber-100 text-amber-700 shrink-0 mt-0.5">AWAITING SAVE</span><p class="text-zinc-600">The file is live in Bambu Studio's directory, but Studio hasn't been reopened and saved it yet &mdash; see <a href="#studio" class="text-emerald-700 hover:underline">below</a>.</p></div>
    <div class="flex items-start gap-3"><span class="text-xs font-medium px-2 py-0.5 rounded-full bg-emerald-100 text-emerald-700 shrink-0 mt-0.5">VERIFIED</span><p class="text-zinc-600">Bambu Studio saved it, and its settings still match what bambupm published &mdash; the round trip is proven, not assumed.</p></div>
    <div class="flex items-start gap-3"><span class="text-xs font-medium px-2 py-0.5 rounded-full bg-red-100 text-red-700 shrink-0 mt-0.5">SEMANTIC_MISMATCH / REJECTED_BY_STUDIO / etc.</span><p class="text-zinc-600">Something genuinely went wrong &mdash; Studio's saved copy has different effective settings, or it refused the publish. Named plainly rather than folded into a generic error.</p></div>
  </div>
  <p class="text-sm text-zinc-600 leading-relaxed">
    A deployment page polls itself every few seconds while awaiting save, so once you save in Studio it flips to
    VERIFIED (or flags a mismatch) without needing a manual reload or a "Check" click.
  </p>
</section>

<section id="compare" class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-3">
  <h2 class="text-base font-semibold">Compare</h2>
  <p class="text-sm text-zinc-600 leading-relaxed">
    Pick 2&ndash;3 profiles &mdash; any profiles, any mix of filament and process, tracked revisions or live ones
    straight off disk &mdash; and see every setting side by side, grouped the way Bambu Studio's own panels group
    them (filament and process use different groupings), with differing rows highlighted. Useful before a copy
    ("what would actually change?") or just to sanity-check two profiles you suspect drifted apart.
  </p>
</section>

<section id="backups" class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-3">
  <h2 class="text-base font-semibold">Backups</h2>
  <p class="text-sm text-zinc-600 leading-relaxed">
    Every publish snapshots your whole profile directory first, automatically &mdash; no separate step to remember.
    Open <a href="/backups" class="text-emerald-700 hover:underline">Backups</a> to see every snapshot and restore
    one. A restore is non-destructive: it overwrites files present in the snapshot but never deletes files you've
    added since.
  </p>
</section>

<section id="filament-vs-process" class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-3">
  <h2 class="text-base font-semibold">Filament vs process (print) profiles</h2>
  <p class="text-sm text-zinc-600 leading-relaxed">
    These are two separate Bambu Studio catalogs, tracked in two separate local databases so their deployment
    histories never mix. Filament profiles get one system leaf per exact printer model, so bambupm matches by
    material + printer name. Process profiles instead share one leaf across a whole printer family via Bambu's own
    <code class="bg-zinc-100 px-1 rounded">compatible_printers</code> field &mdash; there's often no separate
    "P1S" process profile at all, X1C's leaf just lists P1S as compatible too. Switch between them with the
    Filament / Process (print) tabs at the top of Copy, Compare, and the profile list.
  </p>
</section>

<section id="studio" class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-3">
  <h2 class="text-base font-semibold">The one manual step: reopen Bambu Studio and Save</h2>
  <p class="text-sm text-zinc-600 leading-relaxed">
    bambupm can write a correct profile file straight into Bambu Studio's own directory, but it can't make Studio
    itself notice and adopt it &mdash; that's confirmed to require an explicit <strong>Save</strong> on the profile
    inside Studio's UI. Reopening Studio, selecting the profile, or slicing with it do <em>not</em> trigger this;
    only Save does. Use the <strong>Open Bambu Studio</strong> button on an awaiting-save deployment page to launch
    the app, then Save the profile once &mdash; everything after that (detecting the save, verifying the settings
    still match) is automatic.
  </p>
  <p class="text-sm text-zinc-600 leading-relaxed">
    Publishing itself also refuses outright while Studio looks like it's running, since there's no confirmed-safe way
    to write into its live directory while it's open.
  </p>
</section>

<section id="docker" class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-3">
  <h2 class="text-base font-semibold">Running with Docker</h2>
  <p class="text-sm text-zinc-600 leading-relaxed">
    <code class="bg-zinc-100 px-1 rounded">docker compose up -d</code> starts the web UI on
    <code class="bg-zinc-100 px-1 rounded">localhost:8080</code> using the bundled <code class="bg-zinc-100 px-1 rounded">docker-compose.yml</code>.
    Its database lives in a named volume by default (list/import/detail work immediately); uncomment the bind mounts
    for your real Bambu Studio directories to enable Copy and Backups too. One real limitation: the container can't
    see processes running on your host, so the "Bambu Studio is running" safety check can't see a host-side Studio
    &mdash; close Studio yourself before publishing when running this way.
  </p>
</section>

<section id="cli" class="bg-white rounded-2xl border border-zinc-200 shadow-sm p-6 space-y-3">
  <h2 class="text-base font-semibold">CLI reference</h2>
  <p class="text-sm text-zinc-600 leading-relaxed">Everything the web UI does is also a <code class="bg-zinc-100 px-1 rounded">bambupm</code> subcommand, for scripting or headless use:</p>
  <div class="rounded-lg border border-zinc-100 divide-y divide-zinc-50 text-sm">
    <div class="px-3 py-2"><code class="font-mono text-xs">scan --dir &lt;dir&gt;</code><p class="text-zinc-500 mt-0.5">List profiles found in a directory.</p></div>
    <div class="px-3 py-2"><code class="font-mono text-xs">resolve --dir &lt;dir&gt; [...] --name "&lt;name&gt;"</code><p class="text-zinc-500 mt-0.5">Print a profile's full resolved (flattened) settings as JSON.</p></div>
    <div class="px-3 py-2"><code class="font-mono text-xs">serve --db &lt;path&gt; [--user-dir ...] [--process-user-dir ...]</code><p class="text-zinc-500 mt-0.5">Start this web UI.</p></div>
    <div class="px-3 py-2"><code class="font-mono text-xs">copy --db &lt;path&gt; --user-dir ... --name "&lt;name&gt;" --to-printer &lt;token&gt;</code><p class="text-zinc-500 mt-0.5">Copy a filament profile without the UI; previews unless <code>--confirm-name</code> is given.</p></div>
    <div class="px-3 py-2"><code class="font-mono text-xs">publish --db &lt;path&gt; ... --name "&lt;name&gt;" --target &lt;candidate&gt;</code><p class="text-zinc-500 mt-0.5">Lower-level: rebind and publish onto an explicit target parent.</p></div>
    <div class="px-3 py-2"><code class="font-mono text-xs">check-recognition --db &lt;path&gt; --deployment-id &lt;id&gt; --user-dir &lt;dir&gt;</code><p class="text-zinc-500 mt-0.5">Resume a deployment awaiting save, after you've reopened and saved in Studio.</p></div>
    <div class="px-3 py-2"><code class="font-mono text-xs">backups list / restore</code><p class="text-zinc-500 mt-0.5">List or restore point-in-time snapshots.</p></div>
  </div>
  <p class="text-xs text-zinc-400">Run <code class="bg-zinc-100 px-1 rounded">bambupm</code> with no arguments for the full flag-by-flag usage text.</p>
</section>
`))

func (s *Server) handleHelp(w http.ResponseWriter, r *http.Request) {
	renderPage(w, helpTmpl, nil, "Help", "Help", "How bambupm works, end to end.", "help", "", false)
}
