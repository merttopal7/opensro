/*
===========================================================================

handler.go - HTTP handler serving /updates/patches.json and /updates/*.temir

Mounts on the Agent control plane at /updates/ so desktop and mobile clients
querying updateUrl (https://<agent>/updates) receive patches.json and any
missing PatchNNN.temir archives directly from the server. Automatically runs a
differential Sync pass whenever the server's built client assets change on disk.

===========================================================================
*/
package updates

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	syncCheckInterval = 2 * time.Second
	patchWriteTimeout = 15 * time.Minute
)

/*
================
sourceStamp

Captures the modification timestamps and sizes of the top-level client build
manifests so the HTTP handler only triggers a full differential Sync when the
server's built assets have changed.
================
*/
type sourceStamp struct {
	packManifestModNano int64
	packManifestSize    int64
	indexHTMLModNano    int64
	indexHTMLSize       int64
}

/*
================
Handler

Serves /updates/patches.json and /updates/PatchNNN.temir and watches the
configured client asset directories for changes.
================
*/
type Handler struct {
	cfg         SyncConfig
	now         func() time.Time
	mu          sync.Mutex
	lastChecked time.Time
	lastStamp   sourceStamp
	hasStamp    bool
}

/*
================
NewHandler

Creates an updates HTTP handler and performs an initial differential sync check
if a baseline and source asset directories are present.
================
*/
func NewHandler(cfg SyncConfig, now func() time.Time) (*Handler, error) {
	if strings.TrimSpace(cfg.UpdatesDir) == "" {
		return nil, nil
	}
	if cfg.ArchiveKey == "" {
		cfg.ArchiveKey = DefaultArchiveKey
	}
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(cfg.UpdatesDir, 0o755); err != nil {
		return nil, err
	}
	if err := ensurePatchManifestFile(cfg.UpdatesDir); err != nil {
		return nil, err
	}

	h := &Handler{
		cfg: cfg,
		now: now,
	}
	h.runSyncLocked()
	return h, nil
}

/*
================
currentStamp
================
*/
func (h *Handler) currentStamp() sourceStamp {
	var stamp sourceStamp
	if h.cfg.ClientPublicDir != "" {
		manifestPath := filepath.Join(h.cfg.ClientPublicDir, "assets", "packs", "manifest.json")
		if info, err := os.Stat(manifestPath); err == nil {
			stamp.packManifestModNano = info.ModTime().UnixNano()
			stamp.packManifestSize = info.Size()
		}
	}
	if h.cfg.ClientDistDir != "" {
		indexPath := filepath.Join(h.cfg.ClientDistDir, "index.html")
		if info, err := os.Stat(indexPath); err == nil {
			stamp.indexHTMLModNano = info.ModTime().UnixNano()
			stamp.indexHTMLSize = info.Size()
		}
	}
	return stamp
}

/*
================
runSyncLocked
================
*/
func (h *Handler) runSyncLocked() {
	stamp := h.currentStamp()
	h.lastStamp = stamp
	h.hasStamp = true
	h.lastChecked = h.now()

	if h.cfg.ClientPublicDir == "" && h.cfg.ClientDistDir == "" {
		return
	}
	res, err := Sync(h.cfg)
	if err != nil {
		log.Warnf("agent: updates sync: %v", err)
		return
	}
	if res != nil && res.CreatedPatch != "" {
		log.Infof(
			"agent: updates: packaged %d changed file(s) into %s (%d bytes, manifest v%d)",
			len(res.ChangedFiles),
			res.CreatedPatch,
			res.PatchBytes,
			res.Version,
		)
	}
}

/*
================
maybeSync

Checks at most once per syncCheckInterval whether the built client manifest or
index.html changed on disk, and if so runs Sync before serving patches.json.
================
*/
func (h *Handler) maybeSync() {
	now := h.now()
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.hasStamp && now.Sub(h.lastChecked) < syncCheckInterval {
		return
	}
	h.lastChecked = now
	stamp := h.currentStamp()
	if !h.hasStamp || stamp != h.lastStamp {
		h.runSyncLocked()
	}
}

/*
================
isAllowedUpdateFile
================
*/
func isAllowedUpdateFile(name string) bool {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		return false
	}
	if name == ManifestFileName {
		return true
	}
	return strings.HasSuffix(strings.ToLower(name), ".temir")
}

/*
================
ServeHTTP
================
*/
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/updates/")
	name = strings.TrimPrefix(name, "/")
	if !isAllowedUpdateFile(name) {
		http.NotFound(w, r)
		return
	}

	if name == ManifestFileName {
		h.maybeSync()
		raw, err := os.ReadFile(filepath.Join(h.cfg.UpdatesDir, ManifestFileName))
		if err != nil {
			raw = []byte("{\n\t\"version\": 0,\n\t\"patches\": []\n}\n")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(raw)
		}
		return
	}

	filePath := filepath.Join(h.cfg.UpdatesDir, name)
	f, err := os.Open(filePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	_ = http.NewResponseController(w).SetWriteDeadline(h.now().Add(patchWriteTimeout))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, info.ModTime(), f)
}
