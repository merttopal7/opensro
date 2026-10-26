/*
===========================================================================

merge_test.go - tests for last_version merging, compaction and patch naming

Verifies that:
 1. Re-running Sync merges only live entries, so last_version holds the
    newest content and an unchanged tree leaves the archives byte-identical.
 2. A merge never overwrites bytes the committed header points at, and
    compaction reclaims the dead bytes without changing content.
 3. Patch numbers follow the highest existing number, a damaged patches.json
    is an error, and Sync waits for the cross-process lock.

===========================================================================
*/
package updates

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"opensro.online/server/internal/platform/processguard"
)

/*
================
readArchiveEntry
================
*/
func readArchiveEntry(t *testing.T, archivePath, virtualPath string) []byte {
	t.Helper()
	masterKey, toc, err := ReadArchiveTOC(archivePath, DefaultArchiveKey)
	if err != nil {
		t.Fatalf("ReadArchiveTOC(%s): %v", archivePath, err)
	}
	raw, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range toc {
		if normalizeVirtualKey(e.VirtualPath) == normalizeVirtualKey(virtualPath) {
			out := append([]byte(nil), raw[e.DataOffset:e.DataOffset+e.DataLength]...)
			ChaCha20XORAt(&masterKey, &e.FileNonce, 0, out)
			return out
		}
	}
	t.Fatalf("%s not in %s", virtualPath, archivePath)
	return nil
}

/*
================
writeFile
================
*/
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

/*
================
TestSyncMergesOnlyLiveEntriesIntoLastVersion
================
*/
func TestSyncMergesOnlyLiveEntriesIntoLastVersion(t *testing.T) {
	tmp := t.TempDir()
	lastVerDir := filepath.Join(tmp, "last_version")
	updatesDir := filepath.Join(tmp, "updates")
	distDir := filepath.Join(tmp, "client-dist")
	indexFile := filepath.Join(distDir, "index.html")
	extraFile := filepath.Join(distDir, "assets", "extra.bin")

	writeFile(t, indexFile, "<html>v1 base</html>")
	mediaPath := filepath.Join(lastVerDir, "Media.temir")
	if err := os.MkdirAll(lastVerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PackArchiveFile(mediaPath, "Media.temir", []SourceFile{{VirtualPath: "index.html", DiskPath: indexFile}}, DefaultArchiveKey); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteBaselineFromArchives(lastVerDir, updatesDir, DefaultArchiveKey); err != nil {
		t.Fatal(err)
	}
	cfg := SyncConfig{UpdatesDir: updatesDir, ClientDistDir: distDir, ArchiveKey: DefaultArchiveKey}

	// Patch001: a short index.html plus a new file.
	writeFile(t, indexFile, "<html>v2</html>")
	writeFile(t, extraFile, "extra payload only in patch one")
	if res, err := Sync(t.Context(), cfg); err != nil || res.CreatedPatch != "Patch001.temir" {
		t.Fatalf("first Sync = %+v, %v", res, err)
	}

	// Patch002: a longer index.html that supersedes Patch001's.
	writeFile(t, indexFile, "<html>v3 is the newest and longest</html>")
	if res, err := Sync(t.Context(), cfg); err != nil || res.CreatedPatch != "Patch002.temir" {
		t.Fatalf("second Sync = %+v, %v", res, err)
	}

	if got := string(readArchiveEntry(t, mediaPath, "index.html")); got != "<html>v3 is the newest and longest</html>" {
		t.Fatalf("last_version index.html = %q", got)
	}
	if got := string(readArchiveEntry(t, mediaPath, "assets/extra.bin")); got != "extra payload only in patch one" {
		t.Fatalf("last_version extra.bin = %q", got)
	}

	// An unchanged tree must leave last_version byte-identical: re-merging
	// Patch001's superseded index.html would append on every pass.
	before, err := os.ReadFile(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := Sync(t.Context(), cfg); err != nil || res.CreatedPatch != "" {
		t.Fatalf("idle Sync = %+v, %v", res, err)
	}
	after, err := os.ReadFile(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("idle Sync rewrote last_version/Media.temir (%d -> %d bytes)", len(before), len(after))
	}
}

/*
================
TestMergeAppendsAndCompactionReclaimsDeadBytes
================
*/
func TestMergeAppendsAndCompactionReclaimsDeadBytes(t *testing.T) {
	tmp := t.TempDir()
	v1 := filepath.Join(tmp, "v1.html")
	v2 := filepath.Join(tmp, "v2.html")
	blob := filepath.Join(tmp, "blob.bin")
	writeFile(t, v1, "<html>v1</html>")
	writeFile(t, v2, "<html>v2 replaces v1</html>")
	writeFile(t, blob, "unchanged blob")

	mediaPath := filepath.Join(tmp, "Media.temir")
	if _, _, err := PackArchiveFile(mediaPath, "Media.temir", []SourceFile{
		{VirtualPath: "index.html", DiskPath: v1},
		{VirtualPath: "assets/blob.bin", DiskPath: blob},
	}, DefaultArchiveKey); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(mediaPath)
	if err != nil {
		t.Fatal(err)
	}

	patchPath := filepath.Join(tmp, "Patch001.temir")
	if _, _, err := PackArchiveFile(patchPath, "Patch001.temir", []SourceFile{{VirtualPath: "index.html", DiskPath: v2}}, DefaultArchiveKey); err != nil {
		t.Fatal(err)
	}
	if merged, err := MergePatchIntoBaseArchives(tmp, patchPath, DefaultArchiveKey, nil); err != nil || !merged {
		t.Fatalf("MergePatchIntoBaseArchives = %v, %v", merged, err)
	}

	// The pre-merge header over the merged file must still give the
	// pre-merge archive: nothing it pointed at was overwritten.
	merged, err := os.ReadFile(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	rollback := append([]byte(nil), merged...)
	copy(rollback[:HeaderSize], original[:HeaderSize])
	rollbackPath := filepath.Join(tmp, "rollback.temir")
	if err := os.WriteFile(rollbackPath, rollback, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := string(readArchiveEntry(t, rollbackPath, "index.html")); got != "<html>v1</html>" {
		t.Fatalf("rolled-back index.html = %q", got)
	}

	replaced, err := compactArchive(mediaPath, DefaultArchiveKey, 1)
	if err != nil || !replaced {
		t.Fatalf("compactArchive = %v, %v", replaced, err)
	}
	_, toc, err := ReadArchiveTOC(mediaPath, DefaultArchiveKey)
	if err != nil {
		t.Fatal(err)
	}
	var live uint64
	for _, e := range toc {
		live += e.DataLength
	}
	info, err := os.Stat(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := HeaderSize + live + uint64(len(encodeTOC(toc))); uint64(info.Size()) != want {
		t.Fatalf("compacted size = %d, want %d", info.Size(), want)
	}
	if got := string(readArchiveEntry(t, mediaPath, "index.html")); got != "<html>v2 replaces v1</html>" {
		t.Fatalf("compacted index.html = %q", got)
	}
	if got := string(readArchiveEntry(t, mediaPath, "assets/blob.bin")); got != "unchanged blob" {
		t.Fatalf("compacted blob.bin = %q", got)
	}
	if _, err := os.Stat(mediaPath + compactTempSuffix); !os.IsNotExist(err) {
		t.Fatalf("compaction left %s behind (%v)", mediaPath+compactTempSuffix, err)
	}
	if again, err := compactArchive(mediaPath, DefaultArchiveKey, 1); err != nil || again {
		t.Fatalf("second compactArchive = %v, %v; want a no-op", again, err)
	}
}

/*
================
TestLivePatchEntryKeysDropSupersededPathsAndBundles
================
*/
func TestLivePatchEntryKeysDropSupersededPathsAndBundles(t *testing.T) {
	entries := func(paths ...string) []PatchEntryDescriptor {
		out := make([]PatchEntryDescriptor, 0, len(paths))
		for _, p := range paths {
			out = append(out, PatchEntryDescriptor{Path: p, Size: 1, Checksum: 1})
		}
		return out
	}
	live := livePatchEntryKeys([]PatchDescriptor{
		{Name: "Media.temir"},
		{Name: "Patch001.temir", Entries: entries("index.html", "assets/bootstrap-aaa.js", "assets/packs/a.bin")},
		{Name: "Patch002.temir", Entries: entries("/Index.html", "assets/bootstrap-bbb.js")},
	})
	if got := live["Patch001.temir"]; len(got) != 1 {
		t.Fatalf("Patch001 live = %v, want only assets/packs/a.bin", got)
	}
	if _, ok := live["Patch001.temir"]["assets/packs/a.bin"]; !ok {
		t.Fatalf("Patch001 live = %v, want assets/packs/a.bin", live["Patch001.temir"])
	}
	if got := live["Patch002.temir"]; len(got) != 2 {
		t.Fatalf("Patch002 live = %v, want both entries", got)
	}
	if _, ok := live["Media.temir"]; ok {
		t.Fatal("base archives must not get a live set")
	}
}

/*
================
TestNextPatchNameFollowsHighestNumber
================
*/
func TestNextPatchNameFollowsHighestNumber(t *testing.T) {
	updatesDir := t.TempDir()
	manifest := PatchManifest{Patches: []PatchDescriptor{
		{Name: "Media.temir"},
		{Name: "Patch001.temir"},
		{Name: "Patch003.temir"},
	}}
	// Patch002 was deleted: counting would hand out Patch003 again.
	if got := nextPatchName(updatesDir, manifest); got != "Patch004.temir" {
		t.Fatalf("nextPatchName = %s, want Patch004.temir", got)
	}
	writeFile(t, filepath.Join(updatesDir, "Patch007.temir"), "uploaded, not yet in the manifest")
	if got := nextPatchName(updatesDir, manifest); got != "Patch008.temir" {
		t.Fatalf("nextPatchName = %s, want Patch008.temir", got)
	}
}

/*
================
TestDamagedManifestIsAnError
================
*/
func TestDamagedManifestIsAnError(t *testing.T) {
	updatesDir := t.TempDir()
	damaged := `{"version": 13, "patches": [`
	writeFile(t, filepath.Join(updatesDir, ManifestFileName), damaged)

	if _, err := LoadPatchManifest(updatesDir); err == nil {
		t.Fatal("LoadPatchManifest accepted a damaged patches.json")
	}
	if _, err := Sync(t.Context(), SyncConfig{UpdatesDir: updatesDir}); err == nil {
		t.Fatal("Sync ran over a damaged patches.json")
	}
	raw, err := os.ReadFile(filepath.Join(updatesDir, ManifestFileName))
	if err != nil || string(raw) != damaged {
		t.Fatalf("damaged patches.json was rewritten: %q (%v)", raw, err)
	}
}

/*
================
TestSyncWaitsForTheCrossProcessLock
================
*/
func TestSyncWaitsForTheCrossProcessLock(t *testing.T) {
	updatesDir := t.TempDir()
	guard, err := processguard.Acquire(t.Context(), filepath.Join(updatesDir, syncLockFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Sync(ctx, SyncConfig{UpdatesDir: updatesDir}); err == nil {
		t.Fatal("Sync ran while another holder had the updates lock")
	}
	if _, err := os.Stat(filepath.Join(updatesDir, ManifestFileName)); !os.IsNotExist(err) {
		t.Fatalf("Sync wrote patches.json without the lock (%v)", err)
	}
}
