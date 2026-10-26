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
	"context"
	"hash/fnv"
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

	// AppReleaseDirName is the updates/ subdirectory holding desktop
	// application releases: AppReleaseManifestName (the Tauri updater's
	// latest.json) and the signed artifacts it points at.
	AppReleaseDirName      = "app"
	AppReleaseManifestName = "latest.json"
)

/*
================
Handler

Serves /updates/patches.json and /updates/PatchNNN.temir and watches the
configured client asset directories for changes. Sync runs on a background
goroutine: packing a large patch and merging it into last_version can take
minutes, and clients fetch patches.json with a 3 second timeout, so requests
always get the last published manifest immediately.
================
*/
type Handler struct {
	cfg         SyncConfig
	now         func() time.Time
	mu          sync.Mutex
	lastChecked time.Time
	lastStamp   uint64
	hasStamp    bool
	syncing     bool
	syncDone    chan struct{}
}

/*
================
NewHandler

Creates an updates HTTP handler and starts an initial differential sync in the
background.
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
	h.mu.Lock()
	h.startSyncLocked()
	h.mu.Unlock()
	return h, nil
}

/*
================
currentStamp

Fingerprints the path, size and modification time of every source file the
sync packs, plus the PatchNNN.temir files in updates/ (a deploy may upload
one). Any change to any of them triggers a sync, not only a changed
manifest.json or index.html.
================
*/
func (h *Handler) currentStamp() uint64 {
	digest := fnv.New64a()
	write := func(path string) {
		info, err := os.Stat(path)
		if err != nil {
			return
		}
		digest.Write([]byte(path))
		digest.Write([]byte(strconv.FormatInt(info.Size(), 10)))
		digest.Write([]byte(strconv.FormatInt(info.ModTime().UnixNano(), 10)))
	}
	if files, err := CollectSourceFiles(h.cfg.ClientDistDir, h.cfg.ClientPublicDir); err == nil {
		for _, sf := range files {
			write(sf.DiskPath)
		}
	}
	if entries, err := os.ReadDir(h.cfg.UpdatesDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && isDifferentialPatchName(entry.Name()) {
				write(filepath.Join(h.cfg.UpdatesDir, entry.Name()))
			}
		}
	}
	return digest.Sum64()
}

/*
================
startSyncLocked

Starts one background Sync unless one is already running. The stamp is taken
before the pass, so a change made while it runs is seen by the next check.
Callers hold h.mu.
================
*/
func (h *Handler) startSyncLocked() {
	if h.syncing {
		return
	}
	h.lastChecked = h.now()
	h.lastStamp = h.currentStamp()
	h.hasStamp = true
	h.syncing = true
	done := make(chan struct{})
	h.syncDone = done

	go func() {
		defer close(done)
		// The pass outlives any request, so it is not tied to one.
		res, err := Sync(context.Background(), h.cfg)
		h.mu.Lock()
		h.syncing = false
		h.mu.Unlock()
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
	}()
}

/*
================
waitForSync

Blocks until the running background Sync (if any) finishes. Tests use it to
observe the result of a pass deterministically.
================
*/
func (h *Handler) waitForSync() {
	h.mu.Lock()
	done := h.syncDone
	h.mu.Unlock()
	if done != nil {
		<-done
	}
}

/*
================
maybeSync

Checks at most once per syncCheckInterval whether any source file changed on
disk, and if so starts a background Sync. Never blocks the request on it.
================
*/
func (h *Handler) maybeSync() {
	now := h.now()
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.syncing || (h.hasStamp && now.Sub(h.lastChecked) < syncCheckInterval) {
		return
	}
	h.lastChecked = now
	if !h.hasStamp || h.currentStamp() != h.lastStamp {
		h.startSyncLocked()
	}
}

/*
================
resolveUpdateTargetPath

Maps a relative path under `/updates/` (such as `patches.json`, `Patch001.temir`,
or `last_version/Media.temir`) to its validated file path on disk.
================
*/
func (h *Handler) resolveUpdateTargetPath(rel string) (string, string, bool) {
	clean := strings.TrimPrefix(strings.ReplaceAll(rel, "\\", "/"), "/")
	if clean == "" || strings.Contains(clean, "..") {
		return "", "", false
	}
	if clean == ManifestFileName {
		return filepath.Join(h.cfg.UpdatesDir, ManifestFileName), ManifestFileName, true
	}
	if after, ok := strings.CutPrefix(clean, AppReleaseDirName+"/"); ok {
		// Desktop application releases: latest.json plus the signed
		// per-platform artifacts it names, flat inside updates/app/.
		if after == "" || strings.Contains(after, "/") || strings.HasPrefix(after, ".") {
			return "", "", false
		}
		return filepath.Join(h.cfg.UpdatesDir, AppReleaseDirName, after), after, true
	}
	if after, ok := strings.CutPrefix(clean, "last_version/"); ok {
		for _, baseName := range ArchiveNames {
			if strings.EqualFold(after, baseName) {
				lastVerDir := ResolveLastVersionDir(h.cfg)
				candidate := filepath.Join(lastVerDir, baseName)
				if _, err := os.Stat(candidate); err == nil {
					return candidate, baseName, true
				}
				return filepath.Join(h.cfg.UpdatesDir, baseName), baseName, true
			}
		}
		return "", "", false
	}
	if strings.EqualFold(clean, "Silkroad.exe") {
		return filepath.Join(h.cfg.UpdatesDir, "Silkroad.exe"), "Silkroad.exe", true
	}
	if strings.Contains(clean, "/") || !strings.HasSuffix(strings.ToLower(clean), ".temir") {
		return "", "", false
	}
	for _, baseName := range ArchiveNames {
		if strings.EqualFold(clean, baseName) {
			lastVerDir := ResolveLastVersionDir(h.cfg)
			candidate := filepath.Join(lastVerDir, baseName)
			if _, err := os.Stat(candidate); err == nil {
				return candidate, baseName, true
			}
			return filepath.Join(h.cfg.UpdatesDir, baseName), baseName, true
		}
	}
	return filepath.Join(h.cfg.UpdatesDir, clean), clean, true
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

	rel := strings.TrimPrefix(r.URL.Path, "/updates/")
	filePath, baseName, ok := h.resolveUpdateTargetPath(rel)
	if !ok {
		http.NotFound(w, r)
		return
	}

	if baseName == ManifestFileName {
		h.maybeSync()
		raw, err := os.ReadFile(filePath)
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
	contentType := "application/octet-stream"
	if strings.HasSuffix(strings.ToLower(baseName), ".json") {
		contentType = "application/json"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, baseName, info.ModTime(), f)
}
