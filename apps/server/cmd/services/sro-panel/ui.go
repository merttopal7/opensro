/*
===========================================================================

ui.go - Embedded React + Tailwind CSS + shadcn/ui SPA for sro-panel

Serves the compiled production bundle from dist/ directly out of the Go
binary and inlines dist/assets/*.css and dist/assets/*.js into index.html
at startup so reverse proxies (such as aaPanel Nginx rules that intercept
*.js and *.css paths) never return 404 for static assets.

===========================================================================
*/
package main

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"strings"
	"sync"
)

//go:embed all:dist
var panelDistFS embed.FS

var (
	inlinedIndexOnce sync.Once
	inlinedIndexHTML []byte
)

/*
================
buildInlinedIndexHTML

Reads dist/index.html and replaces external /assets/*.css and /assets/*.js
tags with inline <style> and <script type="module"> blocks from the embedded
dist/assets/ directory.
================
*/
func buildInlinedIndexHTML() []byte {
	subFS, err := fs.Sub(panelDistFS, "dist")
	if err != nil {
		return nil
	}
	rawHTML, err := fs.ReadFile(subFS, "index.html")
	if err != nil {
		return nil
	}
	entries, err := fs.ReadDir(subFS, "assets")
	if err != nil {
		return rawHTML
	}

	var cssBuf bytes.Buffer
	var jsBuf bytes.Buffer
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		data, readErr := fs.ReadFile(subFS, "assets/"+name)
		if readErr != nil {
			continue
		}
		if strings.HasSuffix(name, ".css") {
			cssBuf.WriteString("<style>\n")
			cssBuf.Write(data)
			cssBuf.WriteString("\n</style>\n")
		} else if strings.HasSuffix(name, ".js") {
			jsBuf.WriteString("<script type=\"module\">\n")
			jsBuf.Write(data)
			jsBuf.WriteString("\n</script>\n")
		}
	}

	lines := strings.Split(string(rawHTML), "\n")
	var out strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "src=\"/assets/") && strings.Contains(trimmed, ".js\"") {
			continue
		}
		if strings.Contains(trimmed, "href=\"/assets/") && strings.Contains(trimmed, ".css\"") {
			continue
		}
		if strings.Contains(trimmed, "</head>") && cssBuf.Len() > 0 {
			out.WriteString(cssBuf.String())
		}
		if strings.Contains(trimmed, "</body>") && jsBuf.Len() > 0 {
			out.WriteString(jsBuf.String())
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return []byte(out.String())
}

/*
================
handleUI

Serves static bundle assets from dist/assets/ and returns the self-contained
inlined index.html for the root console route.
================
*/
func (s *panelServer) handleUI(w http.ResponseWriter, r *http.Request) {
	inlinedIndexOnce.Do(func() {
		inlinedIndexHTML = buildInlinedIndexHTML()
	})

	subFS, err := fs.Sub(panelDistFS, "dist")
	if err != nil {
		http.Error(w, "ui bundle unavailable", http.StatusInternalServerError)
		return
	}

	cleanPath := strings.TrimPrefix(r.URL.Path, "/")
	if cleanPath != "" && cleanPath != "index.html" {
		if f, openErr := subFS.Open(cleanPath); openErr == nil {
			_ = f.Close()
			if strings.HasPrefix(cleanPath, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.FileServer(http.FS(subFS)).ServeHTTP(w, r)
			return
		}
	}

	if len(inlinedIndexHTML) == 0 {
		http.Error(w, "index.html missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(inlinedIndexHTML)
}
