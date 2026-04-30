package api

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed ui/index.html
var uiFiles embed.FS

// UIHandler serves the embedded single-page dashboard.
// It strips the "ui/" prefix so /index.html resolves correctly.
func UIHandler() http.Handler {
	sub, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		// Should never happen — the embed path is static.
		panic("absia: failed to sub ui embed FS: " + err.Error())
	}
	return http.FileServer(http.FS(sub))
}
