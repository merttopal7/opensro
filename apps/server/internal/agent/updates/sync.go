/*
===========================================================================

sync.go - differential client update detector and PatchNNN.temir builder

Compares the server's built client asset tree against the baseline archive TOC
(updates/baseline.json) and any previously published PatchNNN.temir overlays,
automatically packaging modified or newly added files into updates/PatchNNN.temir
and updating updates/patches.json for clients to download.

===========================================================================
*/
package updates

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"

	"opensro.online/server/internal/platform/processguard"
)

const (
	// BaselineFileName stores the size and FNV-1a checksum of every entry in
	// the base Media.temir, Data.temir, and World.temir containers so the
	// server can compute differential patches without holding 2.5 GB of base
	// archives on disk.
	BaselineFileName = "baseline.json"

	// ManifestFileName is the JSON manifest queried by clients at
	// GET /updates/patches.json.
	ManifestFileName = "patches.json"

	deepHashSizeThreshold uint64 = 32 * 1024 * 1024

	// syncLockFileName serializes Sync across processes: the agent's
	// automatic sync, `sro-packer sync` run by deploys and start-linux.sh all
	// write updates/ and last_version/.
	syncLockFileName = ".sync.lock"
)

/*
================
BaselineEntry

Records the byte length and 64-bit FNV-1a plaintext checksum of one virtual
file in the client archive set.
================
*/
type BaselineEntry struct {
	Size     uint64 `json:"size"`
	Checksum uint64 `json:"checksum"`
}

/*
================
BaselineManifest

Persisted in updates/baseline.json on the server.
================
*/
type BaselineManifest struct {
	Entries map[string]BaselineEntry `json:"entries"`
}

/*
================
PatchEntryDescriptor

Describes one virtual file inside a differential PatchNNN.temir container so
clients that absorb patches in-place into Media.temir / Data.temir / World.temir
can verify in RAM whether all entries are already applied.
================
*/
type PatchEntryDescriptor struct {
	Path     string `json:"path"`
	Size     uint64 `json:"size"`
	Checksum uint64 `json:"checksum"`
}

/*
================
PatchDescriptor

Describes one downloadable .temir archive or differential patch in patches.json.
================
*/
type PatchDescriptor struct {
	Name      string                 `json:"name"`
	Size      uint64                 `json:"size"`
	Checksum  uint64                 `json:"checksum"`
	Files     int                    `json:"files"`
	HeaderTag string                 `json:"headerTag,omitempty"`
	URL       string                 `json:"url,omitempty"`
	Entries   []PatchEntryDescriptor `json:"entries,omitempty"`
}

/*
================
PatchManifest

Top-level JSON document served at GET /updates/patches.json.
================
*/
type PatchManifest struct {
	Version uint32            `json:"version"`
	Patches []PatchDescriptor `json:"patches"`
}

/*
================
SyncConfig

Configures one differential update check and patch build pass on the server.
================
*/
type SyncConfig struct {
	UpdatesDir      string
	ClientPublicDir string
	ClientDistDir   string
	BaseArchivesDir string
	ArchiveKey      string
}

/*
================
SyncResult

Reports the outcome of a Sync pass.
================
*/
type SyncResult struct {
	CreatedPatch string
	ChangedFiles []string
	PatchBytes   uint64
	Version      uint32
}

/*
================
normalizeVirtualKey
================
*/
func normalizeVirtualKey(vpath string) string {
	clean := strings.ReplaceAll(vpath, "\\", "/")
	clean = strings.TrimPrefix(clean, "/")
	return strings.ToLower(clean)
}

/*
================
collectDirFiles

An absent source subtree is normal (not every build publishes every
subtree); any other stat failure is reported.
================
*/
func collectDirFiles(baseDir, curDir, virtualPrefix string, out *[]SourceFile) error {
	info, err := os.Stat(curDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(curDir)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		fullPath := filepath.Join(curDir, entry.Name())
		if entry.IsDir() {
			if err := collectDirFiles(baseDir, fullPath, virtualPrefix, out); err != nil {
				return err
			}
			continue
		}
		meta, err := entry.Info()
		if err != nil || !meta.Mode().IsRegular() {
			continue
		}
		rel, err := filepath.Rel(baseDir, fullPath)
		if err != nil {
			rel = entry.Name()
		}
		rel = strings.ReplaceAll(rel, "\\", "/")
		vpath := rel
		if virtualPrefix != "" {
			vpath = strings.TrimSuffix(virtualPrefix, "/") + "/" + rel
		}
		*out = append(*out, SourceFile{
			VirtualPath: vpath,
			DiskPath:    fullPath,
		})
	}
	return nil
}

/*
================
CollectSourceFiles

Collects all client files from the optional frontend dist directory and the
published client-public asset tree in deterministic order.
================
*/
func CollectSourceFiles(clientDistDir, clientPublicDir string) ([]SourceFile, error) {
	var all []SourceFile
	seen := make(map[string]struct{})
	addUnique := func(sf SourceFile) {
		key := normalizeVirtualKey(sf.VirtualPath)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		all = append(all, sf)
	}
	if strings.TrimSpace(clientDistDir) != "" {
		var distFiles []SourceFile
		if err := collectDirFiles(clientDistDir, clientDistDir, "", &distFiles); err != nil {
			return nil, err
		}
		for _, sf := range distFiles {
			if strings.EqualFold(sf.VirtualPath, "viewer.html") {
				continue
			}
			addUnique(sf)
		}
	}
	if strings.TrimSpace(clientPublicDir) != "" {
		subTrees := []string{
			"assets/packs",
			"assets/delivery",
			"assets/cursors",
			"assets/images/Media_extracted/interface/outer",
			"assets/images/Media_extracted/interface/loading",
		}
		for _, sub := range subTrees {
			dir := filepath.Join(clientPublicDir, filepath.FromSlash(sub))
			var subFiles []SourceFile
			if err := collectDirFiles(dir, dir, sub, &subFiles); err != nil {
				return nil, err
			}
			for _, sf := range subFiles {
				addUnique(sf)
			}
		}
	}
	return all, nil
}

/*
================
WriteBaselineFromArchives

Reads the encrypted TOCs of Media.temir, Data.temir, and World.temir from
temirDir and writes updates/baseline.json so the server can detect subsequent
asset changes without needing the base .temir files on disk.
================
*/
func WriteBaselineFromArchives(temirDir, updatesDir, passphrase string) (int, error) {
	if passphrase == "" {
		passphrase = DefaultArchiveKey
	}
	if err := os.MkdirAll(updatesDir, 0o755); err != nil {
		return 0, err
	}
	entries := make(map[string]BaselineEntry)
	for _, name := range ArchiveNames {
		archivePath := filepath.Join(temirDir, name)
		if _, err := os.Stat(archivePath); err != nil {
			continue
		}
		_, toc, err := ReadArchiveTOC(archivePath, passphrase)
		if err != nil {
			return 0, fmt.Errorf("updates: read %s TOC: %w", name, err)
		}
		for _, item := range toc {
			entries[normalizeVirtualKey(item.VirtualPath)] = BaselineEntry{
				Size:     item.DataLength,
				Checksum: item.Checksum,
			}
		}
	}
	if len(entries) == 0 {
		return 0, fmt.Errorf("updates: no base .temir archives found in %s", temirDir)
	}
	if err := writeJSONAtomic(filepath.Join(updatesDir, BaselineFileName), BaselineManifest{Entries: entries}); err != nil {
		return 0, err
	}
	if err := ensurePatchManifestFile(updatesDir); err != nil {
		return 0, err
	}
	return len(entries), nil
}

/*
================
WriteBaselineFromSources

Hashes the current clientDistDir and clientPublicDir trees and writes
updates/baseline.json as the initial baseline state.
================
*/
func WriteBaselineFromSources(clientDistDir, clientPublicDir, updatesDir string) (int, error) {
	if err := os.MkdirAll(updatesDir, 0o755); err != nil {
		return 0, err
	}
	files, err := CollectSourceFiles(clientDistDir, clientPublicDir)
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, fmt.Errorf("updates: no source files found to snapshot baseline")
	}
	entries := make(map[string]BaselineEntry, len(files))
	for _, sf := range files {
		size, hash, err := FNV1a64File(sf.DiskPath)
		if err != nil {
			return 0, err
		}
		entries[normalizeVirtualKey(sf.VirtualPath)] = BaselineEntry{
			Size:     size,
			Checksum: hash,
		}
	}
	if err := writeJSONAtomic(filepath.Join(updatesDir, BaselineFileName), BaselineManifest{Entries: entries}); err != nil {
		return 0, err
	}
	if err := ensurePatchManifestFile(updatesDir); err != nil {
		return 0, err
	}
	return len(entries), nil
}

/*
================
LoadPatchManifest
================
*/
func LoadPatchManifest(updatesDir string) (PatchManifest, error) {
	manifestPath := filepath.Join(updatesDir, ManifestFileName)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return PatchManifest{Version: 0, Patches: []PatchDescriptor{}}, nil
		}
		return PatchManifest{}, err
	}
	// A damaged manifest is an error, never an empty one: an empty manifest
	// would be written back and erase every published patch.
	var manifest PatchManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return PatchManifest{}, fmt.Errorf("updates: parse %s: %w", manifestPath, err)
	}
	if manifest.Patches == nil {
		manifest.Patches = []PatchDescriptor{}
	}
	return manifest, nil
}

/*
================
ensurePatchManifestFile
================
*/
func ensurePatchManifestFile(updatesDir string) error {
	manifestPath := filepath.Join(updatesDir, ManifestFileName)
	if _, err := os.Stat(manifestPath); err == nil {
		return nil
	}
	return writeJSONAtomic(manifestPath, PatchManifest{
		Version: 0,
		Patches: []PatchDescriptor{},
	})
}

/*
================
writeJSONAtomic
================
*/
func writeJSONAtomic(targetPath string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "\t")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	tmpPath := targetPath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(encoded); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, targetPath)
}

/*
================
loadKnownEntries

Loads updates/baseline.json (bootstrapping it from BaseArchivesDir if present)
and overlays the TOC of every existing PatchNNN.temir in updates/patches.json.
================
*/
func loadKnownEntries(cfg SyncConfig, manifest PatchManifest) (map[string]BaselineEntry, error) {
	baselinePath := filepath.Join(cfg.UpdatesDir, BaselineFileName)
	raw, err := os.ReadFile(baselinePath)
	if os.IsNotExist(err) && strings.TrimSpace(cfg.BaseArchivesDir) != "" {
		if _, writeErr := WriteBaselineFromArchives(cfg.BaseArchivesDir, cfg.UpdatesDir, cfg.ArchiveKey); writeErr == nil {
			raw, err = os.ReadFile(baselinePath)
		}
	}
	if err != nil {
		return nil, err
	}
	var baseline BaselineManifest
	if err := json.Unmarshal(raw, &baseline); err != nil {
		return nil, fmt.Errorf("updates: parse %s: %w", baselinePath, err)
	}
	known := make(map[string]BaselineEntry, len(baseline.Entries))
	for k, v := range baseline.Entries {
		known[normalizeVirtualKey(k)] = v
	}

	for _, patch := range manifest.Patches {
		if !strings.HasPrefix(patch.Name, "Patch") || !strings.HasSuffix(patch.Name, ".temir") {
			continue
		}
		patchPath := filepath.Join(cfg.UpdatesDir, patch.Name)
		_, toc, tocErr := ReadArchiveTOC(patchPath, cfg.ArchiveKey)
		if tocErr != nil {
			continue
		}
		for _, entry := range toc {
			known[normalizeVirtualKey(entry.VirtualPath)] = BaselineEntry{
				Size:     entry.DataLength,
				Checksum: entry.Checksum,
			}
		}
	}
	return known, nil
}

/*
================
ResolveLastVersionDir

Resolves the directory holding the always-merged base archives (Media.temir,
Data.temir, World.temir) used for first-time client installations. Defaults to
the sibling `last_version/` folder next to `updates/` (e.g.
`linux-server/last_version`).
================
*/
func ResolveLastVersionDir(cfg SyncConfig) string {
	if strings.TrimSpace(cfg.BaseArchivesDir) != "" {
		return cfg.BaseArchivesDir
	}
	cleanUpdates := filepath.Clean(cfg.UpdatesDir)
	sibling := filepath.Join(filepath.Dir(cleanUpdates), "last_version")
	nested := filepath.Join(cleanUpdates, "last_version")
	if _, err := os.Stat(filepath.Join(nested, "Media.temir")); err == nil {
		if _, sibErr := os.Stat(filepath.Join(sibling, "Media.temir")); sibErr != nil {
			return nested
		}
	}
	return sibling
}

/*
================
patchNumber

Returns NNN for "PatchNNN.temir", or 0 when the name does not follow it.
================
*/
func patchNumber(name string) int {
	if !isDifferentialPatchName(name) {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "Patch"), ".temir"))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

/*
================
nextPatchName

Numbers the next patch after the highest number in the manifest or on disk.
Counting patches instead would reuse a live name as soon as one older patch
was deleted, and overwrite it.
================
*/
func nextPatchName(updatesDir string, manifest PatchManifest) string {
	highest := 0
	for _, p := range manifest.Patches {
		highest = max(highest, patchNumber(p.Name))
	}
	if dirEntries, err := os.ReadDir(updatesDir); err == nil {
		for _, de := range dirEntries {
			highest = max(highest, patchNumber(de.Name()))
		}
	}
	return fmt.Sprintf("Patch%03d.temir", highest+1)
}

/*
================
Sync

Checks the server's client asset directories against updates/baseline.json plus
any existing PatchNNN.temir files. When one or more files differ, packs only
the changed files into the next updates/PatchNNN.temir, merges the new patch
into last_version/*.temir, and updates updates/patches.json. Holds the
updates/.sync.lock file lock for the whole pass, so concurrent processes run
one after another.
================
*/
func Sync(ctx context.Context, cfg SyncConfig) (*SyncResult, error) {
	if strings.TrimSpace(cfg.UpdatesDir) == "" {
		return nil, fmt.Errorf("updates: UpdatesDir is required")
	}
	if cfg.ArchiveKey == "" {
		cfg.ArchiveKey = DefaultArchiveKey
	}
	if err := os.MkdirAll(cfg.UpdatesDir, 0o755); err != nil {
		return nil, err
	}
	guard, err := processguard.Acquire(ctx, filepath.Join(cfg.UpdatesDir, syncLockFileName))
	if err != nil {
		return nil, fmt.Errorf("updates: lock %s: %w", cfg.UpdatesDir, err)
	}
	defer guard.Close()

	if err := ensurePatchManifestFile(cfg.UpdatesDir); err != nil {
		return nil, err
	}

	manifest, err := LoadPatchManifest(cfg.UpdatesDir)
	if err != nil {
		return nil, err
	}
	backfilledEntries := backfillPatchEntries(cfg, &manifest)

	known, err := loadKnownEntries(cfg, manifest)
	if err != nil {
		if os.IsNotExist(err) {
			if backfilledEntries {
				_ = writeJSONAtomic(filepath.Join(cfg.UpdatesDir, ManifestFileName), manifest)
			}
			return &SyncResult{Version: manifest.Version}, nil
		}
		return nil, err
	}

	allFiles, err := CollectSourceFiles(cfg.ClientDistDir, cfg.ClientPublicDir)
	if err != nil {
		return nil, err
	}
	if len(allFiles) == 0 {
		if backfilledEntries {
			_ = writeJSONAtomic(filepath.Join(cfg.UpdatesDir, ManifestFileName), manifest)
		}
		return &SyncResult{Version: manifest.Version}, nil
	}

	manifestChanged := detectManifestChanged(allFiles, known)
	changedFiles, err := findChangedFiles(allFiles, known, manifestChanged)
	if err != nil {
		return nil, err
	}
	if len(changedFiles) == 0 {
		if backfilledEntries {
			_ = writeJSONAtomic(filepath.Join(cfg.UpdatesDir, ManifestFileName), manifest)
		}
		return &SyncResult{Version: manifest.Version}, nil
	}

	patchName := nextPatchName(cfg.UpdatesDir, manifest)
	nextIndex := patchNumber(patchName)
	patchPath := filepath.Join(cfg.UpdatesDir, patchName)

	count, sizeBytes, err := PackArchiveFile(patchPath, patchName, changedFiles, cfg.ArchiveKey)
	if err != nil {
		return nil, fmt.Errorf("updates: pack %s: %w", patchName, err)
	}
	_, patchChecksum, err := FNV1a64File(patchPath)
	if err != nil {
		return nil, fmt.Errorf("updates: hash %s: %w", patchName, err)
	}
	_, headerTag, _ := ReadArchiveHeaderTag(patchPath)
	var patchEntries []PatchEntryDescriptor
	if _, toc, tocErr := ReadArchiveTOC(patchPath, cfg.ArchiveKey); tocErr == nil {
		patchEntries = make([]PatchEntryDescriptor, 0, len(toc))
		for _, item := range toc {
			patchEntries = append(patchEntries, PatchEntryDescriptor{
				Path:     item.VirtualPath,
				Size:     item.DataLength,
				Checksum: item.Checksum,
			})
		}
	}

	nextVersion := manifest.Version + 1
	if uint32(nextIndex) > nextVersion {
		nextVersion = uint32(nextIndex)
	}
	manifest.Version = nextVersion
	manifest.Patches = append(manifest.Patches, PatchDescriptor{
		Name:      patchName,
		Size:      sizeBytes,
		Checksum:  patchChecksum,
		Files:     count,
		HeaderTag: headerTag,
		Entries:   patchEntries,
	})

	// Immediately merge the newly created PatchNNN.temir into last_version/*.temir
	// and refresh the base archive descriptors in manifest.Patches.
	_ = backfillPatchEntries(cfg, &manifest)

	if err := writeJSONAtomic(filepath.Join(cfg.UpdatesDir, ManifestFileName), manifest); err != nil {
		return nil, fmt.Errorf("updates: write %s: %w", ManifestFileName, err)
	}

	changedPaths := make([]string, 0, len(changedFiles))
	for _, cf := range changedFiles {
		changedPaths = append(changedPaths, cf.VirtualPath)
	}
	return &SyncResult{
		CreatedPatch: patchName,
		ChangedFiles: changedPaths,
		PatchBytes:   sizeBytes,
		Version:      manifest.Version,
	}, nil
}

/*
================
backfillPatchEntries

Synchronizes `last_version/` base archives and `manifest.Patches`:
 1. Moves any base archives (`Media.temir`, `Data.temir`, `World.temir`) placed in
    `updates/` into `last_version/`.
 2. Removes any `PatchNNN.temir` descriptor whose file was deleted from `updates/`.
 3. Merges all active `PatchNNN.temir` files in-place into `last_version/*.temir`
    so first-time installers download already-merged base archives from `last_version/`.
 4. Updates the `Media.temir`, `Data.temir`, `World.temir` descriptors (`url: "last_version/<name>"`)
    and backfills `Entries` on every `PatchNNN.temir`.

================
*/
func backfillPatchEntries(cfg SyncConfig, manifest *PatchManifest) bool {
	updatesDir := cfg.UpdatesDir
	passphrase := cfg.ArchiveKey
	if passphrase == "" {
		passphrase = DefaultArchiveKey
	}
	lastVersionDir := ResolveLastVersionDir(cfg)
	_ = os.MkdirAll(lastVersionDir, 0o755)

	updated := false
	baseReplaced := false
	prunedPatches := false

	// 1. Migrate any base .temir archives placed directly in updatesDir into lastVersionDir
	if filepath.Clean(lastVersionDir) != filepath.Clean(updatesDir) {
		for _, baseName := range ArchiveNames {
			oldBase := filepath.Join(updatesDir, baseName)
			newBase := filepath.Join(lastVersionDir, baseName)
			if info, err := os.Stat(oldBase); err == nil && info.Mode().IsRegular() {
				if _, destErr := os.Stat(newBase); os.IsNotExist(destErr) {
					_ = os.Rename(oldBase, newBase)
				}
			}
		}
	}

	// 2. Remove any PatchNNN.temir entry whose file was deleted from updatesDir,
	// and discover any newly uploaded PatchNNN.temir files on disk.
	kept := make([]PatchDescriptor, 0, len(manifest.Patches))
	activePatchNames := make([]string, 0)
	knownPatchNames := make(map[string]struct{}, len(manifest.Patches))
	for _, p := range manifest.Patches {
		if strings.HasPrefix(p.Name, "Patch") && strings.HasSuffix(p.Name, ".temir") {
			patchPath := filepath.Join(updatesDir, p.Name)
			info, err := os.Stat(patchPath)
			if err != nil {
				prunedPatches = true
				updated = true
				continue
			}
			_, diskTag, tagErr := ReadArchiveHeaderTag(patchPath)
			if uint64(info.Size()) != p.Size || (tagErr == nil && diskTag != "" && !strings.EqualFold(diskTag, p.HeaderTag)) {
				updated = true
				continue
			}
			activePatchNames = append(activePatchNames, p.Name)
			knownPatchNames[strings.ToLower(p.Name)] = struct{}{}
		}
		kept = append(kept, p)
	}
	if dirEntries, readErr := os.ReadDir(updatesDir); readErr == nil {
		var diskPatches []string
		for _, de := range dirEntries {
			if de.IsDir() {
				continue
			}
			name := de.Name()
			if strings.HasPrefix(name, "Patch") && strings.HasSuffix(name, ".temir") {
				if _, exists := knownPatchNames[strings.ToLower(name)]; !exists {
					diskPatches = append(diskPatches, name)
				}
			}
		}
		sort.Slice(diskPatches, func(i, j int) bool {
			return patchNumber(diskPatches[i]) < patchNumber(diskPatches[j])
		})
		for _, name := range diskPatches {
			patchPath := filepath.Join(updatesDir, name)
			sizeBytes, patchChecksum, hashErr := FNV1a64File(patchPath)
			if hashErr != nil {
				continue
			}
			_, headerTag, _ := ReadArchiveHeaderTag(patchPath)
			_, toc, tocErr := ReadArchiveTOC(patchPath, passphrase)
			if tocErr != nil || len(toc) == 0 {
				continue
			}
			entries := make([]PatchEntryDescriptor, 0, len(toc))
			for _, item := range toc {
				entries = append(entries, PatchEntryDescriptor{
					Path:     item.VirtualPath,
					Size:     item.DataLength,
					Checksum: item.Checksum,
				})
			}
			kept = append(kept, PatchDescriptor{
				Name:      name,
				Size:      sizeBytes,
				Checksum:  patchChecksum,
				Files:     len(toc),
				HeaderTag: headerTag,
				Entries:   entries,
			})
			activePatchNames = append(activePatchNames, name)
			manifest.Version++
			updated = true
		}
	}
	manifest.Patches = kept
	if fillPatchEntries(updatesDir, passphrase, manifest) {
		updated = true
	}

	// 3. Merge the live entries of every active PatchNNN.temir into
	// last_version/*.temir. Superseded entries are skipped, so this pass is
	// idempotent and leaves the archives untouched when they are current.
	mergedIntoLastVersion := false
	live := livePatchEntryKeys(manifest.Patches)
	for _, patchName := range activePatchNames {
		// A patch without entries is absent from live: nil merges it whole.
		patchPath := filepath.Join(updatesDir, patchName)
		merged, err := MergePatchIntoBaseArchives(lastVersionDir, patchPath, passphrase, live[patchName])
		if err != nil {
			log.Warnf("agent: updates: %v", err)
			continue
		}
		if merged {
			mergedIntoLastVersion = true
			updated = true
		}
	}

	// Merges only append; reclaim dead bytes while patches are published.
	// With no patches a changed header tag would read as a replaced base
	// archive and send every client a full download, so compaction waits.
	if len(activePatchNames) > 0 {
		for _, baseName := range ArchiveNames {
			basePath := filepath.Join(lastVersionDir, baseName)
			if _, err := os.Stat(basePath); err != nil {
				continue
			}
			if _, err := compactArchive(basePath, passphrase, compactMinDeadBytes); err != nil {
				log.Warnf("agent: updates: compact %s: %v", baseName, err)
			}
		}
	}

	// 4. Sync Media.temir, Data.temir, World.temir descriptors from lastVersionDir
	var basePrepend []PatchDescriptor
	for _, baseName := range ArchiveNames {
		basePath := filepath.Join(lastVersionDir, baseName)
		info, err := os.Stat(basePath)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		_, headerTag, tagErr := ReadArchiveHeaderTag(basePath)
		if tagErr != nil {
			continue
		}
		fileSize := uint64(info.Size())
		relURL := "last_version/" + baseName

		existingIdx := -1
		for i := range manifest.Patches {
			if strings.EqualFold(manifest.Patches[i].Name, baseName) {
				existingIdx = i
				break
			}
		}
		if existingIdx >= 0 {
			cur := &manifest.Patches[existingIdx]
			if cur.URL != relURL {
				cur.URL = relURL
				updated = true
			}
			if cur.Size != fileSize || !strings.EqualFold(cur.HeaderTag, headerTag) || cur.Checksum == 0 {
				_, toc, tocErr := ReadArchiveTOC(basePath, passphrase)
				_, checksum, hashErr := FNV1a64File(basePath)
				if tocErr == nil && hashErr == nil {
					if cur.HeaderTag != "" && !strings.EqualFold(cur.HeaderTag, headerTag) &&
						!mergedIntoLastVersion && len(activePatchNames) == 0 {
						baseReplaced = true
					}
					cur.Size = fileSize
					cur.Checksum = checksum
					cur.Files = len(toc)
					cur.HeaderTag = headerTag
					updated = true
				}
			}
			continue
		}

		_, toc, err := ReadArchiveTOC(basePath, passphrase)
		if err != nil {
			continue
		}
		// The whole-file checksum lets clients verify a 2 GB download, not
		// only its header and TOC.
		_, checksum, err := FNV1a64File(basePath)
		if err != nil {
			continue
		}
		basePrepend = append(basePrepend, PatchDescriptor{
			Name:      baseName,
			Size:      fileSize,
			Checksum:  checksum,
			Files:     len(toc),
			HeaderTag: headerTag,
			URL:       relURL,
		})
	}
	if len(basePrepend) > 0 {
		manifest.Patches = append(basePrepend, manifest.Patches...)
		updated = true
	}

	if baseReplaced || prunedPatches {
		manifest.Version++
		_, _ = WriteBaselineFromArchives(lastVersionDir, updatesDir, passphrase)
	}
	return updated
}

/*
================
fillPatchEntries

Backfills Entries on every PatchNNN.temir descriptor that lacks them, from the
patch's own TOC. Runs before the merge so livePatchEntryKeys sees every patch.
================
*/
func fillPatchEntries(updatesDir, passphrase string, manifest *PatchManifest) bool {
	updated := false
	for i := range manifest.Patches {
		p := &manifest.Patches[i]
		if !isDifferentialPatchName(p.Name) || len(p.Entries) > 0 {
			continue
		}
		patchPath := filepath.Join(updatesDir, p.Name)
		_, toc, err := ReadArchiveTOC(patchPath, passphrase)
		if err != nil || len(toc) == 0 {
			continue
		}
		entries := make([]PatchEntryDescriptor, 0, len(toc))
		for _, item := range toc {
			entries = append(entries, PatchEntryDescriptor{
				Path:     item.VirtualPath,
				Size:     item.DataLength,
				Checksum: item.Checksum,
			})
		}
		p.Entries = entries
		updated = true
	}
	return updated
}

/*
================
detectManifestChanged
================
*/
func detectManifestChanged(allFiles []SourceFile, known map[string]BaselineEntry) bool {
	for _, sf := range allFiles {
		if normalizeVirtualKey(sf.VirtualPath) == "assets/packs/manifest.json" {
			prev, ok := known["assets/packs/manifest.json"]
			if !ok {
				return true
			}
			size, hash, err := FNV1a64File(sf.DiskPath)
			if err != nil {
				return true
			}
			return size != prev.Size || hash != prev.Checksum
		}
	}
	return false
}

/*
================
findChangedFiles
================
*/
func findChangedFiles(
	allFiles []SourceFile,
	known map[string]BaselineEntry,
	manifestChanged bool,
) ([]SourceFile, error) {
	var changed []SourceFile
	for _, sf := range allFiles {
		key := normalizeVirtualKey(sf.VirtualPath)
		info, err := os.Stat(sf.DiskPath)
		if err != nil {
			return nil, err
		}
		fileLen := uint64(info.Size())

		prev, exists := known[key]
		if !exists || prev.Size != fileLen {
			changed = append(changed, sf)
			continue
		}
		if fileLen <= deepHashSizeThreshold || manifestChanged {
			_, hash, err := FNV1a64File(sf.DiskPath)
			if err != nil {
				return nil, err
			}
			if hash != prev.Checksum {
				changed = append(changed, sf)
			}
		}
	}
	return changed, nil
}
