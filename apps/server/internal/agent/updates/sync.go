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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
PatchDescriptor

Describes one downloadable .temir archive or differential patch in patches.json.
================
*/
type PatchDescriptor struct {
	Name     string `json:"name"`
	Size     uint64 `json:"size"`
	Checksum uint64 `json:"checksum"`
	Files    int    `json:"files"`
	URL      string `json:"url,omitempty"`
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
================
*/
func collectDirFiles(baseDir, curDir, virtualPrefix string, out *[]SourceFile) error {
	info, err := os.Stat(curDir)
	if err != nil || !info.IsDir() {
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
	if strings.TrimSpace(clientDistDir) != "" {
		var distFiles []SourceFile
		if err := collectDirFiles(clientDistDir, clientDistDir, "", &distFiles); err != nil {
			return nil, err
		}
		for _, sf := range distFiles {
			if strings.EqualFold(sf.VirtualPath, "viewer.html") {
				continue
			}
			all = append(all, sf)
		}
	}
	if strings.TrimSpace(clientPublicDir) != "" {
		subTrees := []string{
			"assets/packs",
			"assets/delivery",
			"assets/images/Media_extracted/interface/outer",
			"assets/images/Media_extracted/interface/loading",
		}
		for _, sub := range subTrees {
			dir := filepath.Join(clientPublicDir, filepath.FromSlash(sub))
			if err := collectDirFiles(dir, dir, sub, &all); err != nil {
				return nil, err
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
	var manifest PatchManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return PatchManifest{Version: 0, Patches: []PatchDescriptor{}}, nil
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
	if err := os.WriteFile(tmpPath, encoded, 0o644); err != nil {
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
Sync

Checks the server's client asset directories against updates/baseline.json plus
any existing PatchNNN.temir files. When one or more files differ, packs only
the changed files into the next updates/PatchNNN.temir and updates
updates/patches.json.
================
*/
func Sync(cfg SyncConfig) (*SyncResult, error) {
	if strings.TrimSpace(cfg.UpdatesDir) == "" {
		return nil, fmt.Errorf("updates: UpdatesDir is required")
	}
	if cfg.ArchiveKey == "" {
		cfg.ArchiveKey = DefaultArchiveKey
	}
	if err := os.MkdirAll(cfg.UpdatesDir, 0o755); err != nil {
		return nil, err
	}
	if err := ensurePatchManifestFile(cfg.UpdatesDir); err != nil {
		return nil, err
	}

	manifest, err := LoadPatchManifest(cfg.UpdatesDir)
	if err != nil {
		return nil, err
	}

	known, err := loadKnownEntries(cfg, manifest)
	if err != nil {
		if os.IsNotExist(err) {
			return &SyncResult{Version: manifest.Version}, nil
		}
		return nil, err
	}

	allFiles, err := CollectSourceFiles(cfg.ClientDistDir, cfg.ClientPublicDir)
	if err != nil {
		return nil, err
	}
	if len(allFiles) == 0 {
		return &SyncResult{Version: manifest.Version}, nil
	}

	manifestChanged := detectManifestChanged(allFiles, known)
	changedFiles, err := findChangedFiles(allFiles, known, manifestChanged)
	if err != nil {
		return nil, err
	}
	if len(changedFiles) == 0 {
		return &SyncResult{Version: manifest.Version}, nil
	}

	nextIndex := len(manifest.Patches) + 1
	patchName := fmt.Sprintf("Patch%03d.temir", nextIndex)
	patchPath := filepath.Join(cfg.UpdatesDir, patchName)

	count, sizeBytes, err := PackArchiveFile(patchPath, patchName, changedFiles, cfg.ArchiveKey)
	if err != nil {
		return nil, fmt.Errorf("updates: pack %s: %w", patchName, err)
	}
	_, patchChecksum, err := FNV1a64File(patchPath)
	if err != nil {
		return nil, fmt.Errorf("updates: hash %s: %w", patchName, err)
	}

	manifest.Version = uint32(nextIndex)
	manifest.Patches = append(manifest.Patches, PatchDescriptor{
		Name:     patchName,
		Size:     sizeBytes,
		Checksum: patchChecksum,
		Files:    count,
	})
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
