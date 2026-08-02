package webui

import (
	_ "embed"
	"net/http"
)

//go:embed assets/logo.png
var logoPNG []byte

// handleLogo serves the app icon embedded in the binary (assets/logo.png)
// — single-binary distribution stays intact, no separate static file to
// ship or mount. Aggressively cacheable: the file only changes on a new
// build, and go:embed gives every build a fresh byte slice anyway.
func handleLogo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.Write(logoPNG)
}
