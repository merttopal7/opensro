/*
===========================================================================

updates_test.go - unit tests for TEMIR archive packing, differential sync, and
/updates/ HTTP serving

Verifies that:
 1. PackArchiveFile and ReadArchiveTOC round-trip encrypted payloads and reject
    wrong passphrases.
 2. Sync detects changed files against baseline.json + existing PatchNNN.temir
    overlays, emits incremental Patch001.temir / Patch002.temir archives, and
    updates patches.json.
 3. Handler serves /updates/patches.json and /updates/PatchNNN.temir over HTTP
    and triggers automatic re-sync when source manifests change.

===========================================================================
*/
package updates

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

/*
================
TestTemirRoundTripAndAuth
================
*/
func TestTemirRoundTripAndAuth(t *testing.T) {
	tmp := t.TempDir()
	fileA := filepath.Join(tmp, "manifest.json")
	fileB := filepath.Join(tmp, "index.html")
	payloadA := []byte(`{"packs":["table-data-1.bin"]}`)
	payloadB := []byte(`<!doctype html><html><body>Silkroad</body></html>`)

	if err := os.WriteFile(fileA, payloadA, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, payloadB, 0o644); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(tmp, "Patch001.temir")
	sources := []SourceFile{
		{VirtualPath: "assets/packs/manifest.json", DiskPath: fileA},
		{VirtualPath: "index.html", DiskPath: fileB},
	}
	count, totalBytes, err := PackArchiveFile(archivePath, "Patch001.temir", sources, DefaultArchiveKey)
	if err != nil {
		t.Fatalf("PackArchiveFile: %v", err)
	}
	if count != 2 || totalBytes <= HeaderSize {
		t.Fatalf("unexpected count=%d totalBytes=%d", count, totalBytes)
	}

	// Wrong passphrase must fail authentication tag validation.
	if _, _, err := ReadArchiveTOC(archivePath, "wrong-key"); err == nil {
		t.Fatal("expected ReadArchiveTOC to fail with wrong key")
	}

	masterKey, toc, err := ReadArchiveTOC(archivePath, DefaultArchiveKey)
	if err != nil {
		t.Fatalf("ReadArchiveTOC: %v", err)
	}
	if len(toc) != 2 {
		t.Fatalf("toc len = %d, want 2", len(toc))
	}

	rawArchive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	entry0 := toc[0]
	cipherSlice := append([]byte(nil), rawArchive[entry0.DataOffset:entry0.DataOffset+entry0.DataLength]...)
	ChaCha20XORAt(&masterKey, &entry0.FileNonce, 0, cipherSlice)
	if !bytes.Equal(cipherSlice, payloadA) {
		t.Fatalf("decrypted payload mismatch: got %q, want %q", cipherSlice, payloadA)
	}
	if FNV1a64(cipherSlice) != entry0.Checksum {
		t.Fatalf("checksum mismatch: got %d, want %d", FNV1a64(cipherSlice), entry0.Checksum)
	}
}

/*
================
TestDifferentialSyncAndHTTPHandler
================
*/
func TestDifferentialSyncAndHTTPHandler(t *testing.T) {
	tmp := t.TempDir()
	publicDir := filepath.Join(tmp, "client-public")
	distDir := filepath.Join(tmp, "client-dist")
	updatesDir := filepath.Join(tmp, "updates")

	packsDir := filepath.Join(publicDir, "assets", "packs")
	if err := os.MkdirAll(packsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		t.Fatal(err)
	}

	manifestFile := filepath.Join(packsDir, "manifest.json")
	tablePackFile := filepath.Join(packsDir, "table-data-v1.bin")
	indexFile := filepath.Join(distDir, "index.html")

	if err := os.WriteFile(manifestFile, []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tablePackFile, []byte("table-data-payload-v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexFile, []byte("<html>v1</html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Snapshot baseline from v1 sources.
	entryCount, err := WriteBaselineFromSources(distDir, publicDir, updatesDir)
	if err != nil {
		t.Fatalf("WriteBaselineFromSources: %v", err)
	}
	if entryCount != 3 {
		t.Fatalf("baseline entries = %d, want 3", entryCount)
	}

	cfg := SyncConfig{
		UpdatesDir:      updatesDir,
		ClientPublicDir: publicDir,
		ClientDistDir:   distDir,
		ArchiveKey:      DefaultArchiveKey,
	}

	// 2. Unchanged sources should produce no patch.
	res, err := Sync(cfg)
	if err != nil {
		t.Fatalf("Sync unchanged: %v", err)
	}
	if res.CreatedPatch != "" || res.Version != 0 {
		t.Fatalf("expected no patch on unchanged sources, got %+v", res)
	}

	// 3. Modify table-data and manifest.json -> should produce Patch001.temir.
	if err := os.WriteFile(manifestFile, []byte(`{"version":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tablePackFile, []byte("table-data-payload-v2-modified"), 0o644); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	handler, err := NewHandler(cfg, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/updates/", handler)

	// 4. Query GET /updates/patches.json -> should report version 1 and Patch001.temir.
	req := httptest.NewRequest(http.MethodGet, "/updates/patches.json", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /updates/patches.json status = %d", rec.Code)
	}
	var pm PatchManifest
	if err := json.Unmarshal(rec.Body.Bytes(), &pm); err != nil {
		t.Fatalf("unmarshal patches.json: %v", err)
	}
	if pm.Version != 1 || len(pm.Patches) != 1 || pm.Patches[0].Name != "Patch001.temir" {
		t.Fatalf("unexpected patches.json: %+v", pm)
	}
	if pm.Patches[0].Files != 2 {
		t.Fatalf("Patch001 files = %d, want 2", pm.Patches[0].Files)
	}

	// 5. Download GET /updates/Patch001.temir and verify its TOC and checksum.
	reqPatch := httptest.NewRequest(http.MethodGet, "/updates/Patch001.temir", nil)
	recPatch := httptest.NewRecorder()
	mux.ServeHTTP(recPatch, reqPatch)
	if recPatch.Code != http.StatusOK {
		t.Fatalf("GET /updates/Patch001.temir status = %d", recPatch.Code)
	}
	downloadedBytes, err := io.ReadAll(recPatch.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	if uint64(len(downloadedBytes)) != pm.Patches[0].Size {
		t.Fatalf("downloaded size = %d, want %d", len(downloadedBytes), pm.Patches[0].Size)
	}
	if FNV1a64(downloadedBytes) != pm.Patches[0].Checksum {
		t.Fatalf("downloaded checksum mismatch")
	}

	// 6. Modify index.html, advance clock past syncCheckInterval, and verify
	// GET /updates/patches.json automatically creates Patch002.temir.
	if err := os.WriteFile(indexFile, []byte("<html>v2-auto-synced</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Second)
	req2 := httptest.NewRequest(http.MethodGet, "/updates/patches.json", nil)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if err := json.Unmarshal(rec2.Body.Bytes(), &pm); err != nil {
		t.Fatal(err)
	}
	if pm.Version != 2 || len(pm.Patches) != 2 || pm.Patches[1].Name != "Patch002.temir" {
		t.Fatalf("expected auto-synced Patch002.temir, got %+v", pm)
	}

	// 7. Ensure internal baseline.json and traversal paths are rejected.
	for _, blocked := range []string{"/updates/baseline.json", "/updates/../baseline.json", "/updates/sub/Patch001.temir"} {
		r := httptest.NewRequest(http.MethodGet, blocked, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for %s, got %d", blocked, w.Code)
		}
	}
}
