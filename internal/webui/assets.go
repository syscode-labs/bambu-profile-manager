package webui

import (
	_ "embed"
	"net/http"
)

// LogoPNG is the app icon, embedded in the binary — single-binary
// distribution stays intact, no separate static file to ship or mount.
// Exported so cmd/bpm-tray can reuse it as the menu bar icon without
// duplicating the asset (go:embed patterns can't reach outside their own
// package's directory tree).
//
//go:embed assets/logo.png
var LogoPNG []byte

// LogoTemplatePNG is a monochrome (black-on-transparent) silhouette derived
// from LogoPNG, cropped and downsized for use as a macOS menu bar "template"
// image — those get tinted/inverted automatically for the light/dark menu
// bar, but only if they're a flat black shape with real alpha, not a full
// colour icon (real gap found live: SetIcon with the full-colour app icon
// rendered as a tiny, illegible blob at menu bar size).
//
//go:embed assets/logo-template.png
var LogoTemplatePNG []byte

// handleLogo serves LogoPNG over HTTP. Aggressively cacheable: the file
// only changes on a new build, and go:embed gives every build a fresh byte
// slice anyway.
func handleLogo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.Write(LogoPNG)
}
