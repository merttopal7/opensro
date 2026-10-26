/*
===========================================================================

merge.go - server-side PatchNNN.temir merger into last_version/

Merges differential PatchNNN.temir files into the base archives (Media.temir,
Data.temir, World.temir) stored inside last_version/ on the server so
first-time client installations download a single already-updated set of base
archives without needing to download any additional PatchNNN.temir files.

Two rules keep that tree correct while it is being served:
  - Only live entries are merged (livePatchEntryKeys): an entry a later patch
    supersedes never overwrites the newer content, so re-running the merge on
    every Sync is idempotent and never churns the archives.
  - Merges only append. Nothing the committed header points at is
    overwritten, the new bytes are flushed before the header moves, and a
    download that is streaming the archive keeps reading a consistent file.
    compactArchive reclaims the dead bytes by writing a copy and renaming it
    over the original (open readers keep the old file).

The desktop client (new-silkroad src-tauri/src/sropk.rs) implements the same
rules; keep the two in step.

===========================================================================
*/
package updates

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// compactMinDeadBytes and compactDeadRatioDivisor gate compaction: the
	// dead bytes must pass both 64 MiB and a quarter of the archive.
	compactMinDeadBytes     uint64 = 64 * 1024 * 1024
	compactDeadRatioDivisor uint64 = 4

	compactTempSuffix = ".compact.tmp"
)

/*
================
bundleFamily

Vite emits one hashed entry bundle and one hashed stylesheet per build
(assets/bootstrap-<hash>.js, assets/index-<hash>.css). A newer file of a
family supersedes every older one even though their paths differ.
================
*/
func bundleFamily(key string) string {
	if strings.HasPrefix(key, "assets/bootstrap-") && strings.HasSuffix(key, ".js") {
		return "bootstrap"
	}
	if strings.HasPrefix(key, "assets/index-") && strings.HasSuffix(key, ".css") {
		return "index-css"
	}
	return ""
}

/*
================
isDifferentialPatchName
================
*/
func isDifferentialPatchName(name string) bool {
	return strings.HasPrefix(name, "Patch") && strings.HasSuffix(name, ".temir")
}

/*
================
livePatchEntryKeys

Returns, per differential patch name, the normalized paths of its entries that
still describe the final state: an entry is dead when a later patch (manifest
order) carries the same path or a later bundle of the same family. A patch
without entries is absent from the map, which callers read as "merge all".
================
*/
func livePatchEntryKeys(patches []PatchDescriptor) map[string]map[string]struct{} {
	live := make(map[string]map[string]struct{})
	laterKeys := make(map[string]struct{})
	laterFamilies := make(map[string]struct{})

	for i := len(patches) - 1; i >= 0; i-- {
		patch := patches[i]
		if !isDifferentialPatchName(patch.Name) || len(patch.Entries) == 0 {
			continue
		}
		kept := make(map[string]struct{}, len(patch.Entries))
		for _, entry := range patch.Entries {
			key := normalizeVirtualKey(entry.Path)
			if _, dead := laterKeys[key]; dead {
				continue
			}
			if family := bundleFamily(key); family != "" {
				if _, dead := laterFamilies[family]; dead {
					continue
				}
			}
			kept[key] = struct{}{}
		}
		for _, entry := range patch.Entries {
			key := normalizeVirtualKey(entry.Path)
			if family := bundleFamily(key); family != "" {
				laterFamilies[family] = struct{}{}
			}
			laterKeys[key] = struct{}{}
		}
		live[patch.Name] = kept
	}
	return live
}

/*
================
missingArchiveEntries

Returns the entries of expected that archivePath's TOC does not hold with the
same byte length and 64-bit FNV-1a checksum.
================
*/
func missingArchiveEntries(archivePath, passphrase string, expected []ArchiveEntry) ([]ArchiveEntry, error) {
	if len(expected) == 0 {
		return nil, nil
	}
	_, toc, err := ReadArchiveTOC(archivePath, passphrase)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]ArchiveEntry, len(toc))
	for _, e := range toc {
		byKey[normalizeVirtualKey(e.VirtualPath)] = e
	}
	var missing []ArchiveEntry
	for _, exp := range expected {
		cur, ok := byKey[normalizeVirtualKey(exp.VirtualPath)]
		if !ok || cur.DataLength != exp.DataLength || cur.Checksum != exp.Checksum {
			missing = append(missing, exp)
		}
	}
	return missing, nil
}

/*
================
readArchiveHeader

Reads and authenticates the header of an open archive, returning its salt,
master key, entry count and decrypted TOC.
================
*/
func readArchiveHeader(f *os.File, fileSize uint64, passphrase string) ([16]byte, [32]byte, []ArchiveEntry, error) {
	var salt [16]byte
	var key [32]byte
	if fileSize < HeaderSize {
		return salt, key, nil, errors.New("temir: target archive too small")
	}
	var header [HeaderSize]byte
	if _, err := f.ReadAt(header[:], 0); err != nil {
		return salt, key, nil, err
	}
	if string(header[0:8]) != string(temirMagic[:]) {
		return salt, key, nil, errors.New("temir: invalid target TEMIR magic header")
	}
	copy(salt[:], header[8:24])
	key = DeriveMasterKey(passphrase, &salt)

	var meta [24]byte
	copy(meta[:], header[24:48])
	ChaCha20XORAt(&key, &headerNonce, 0, meta[:])
	tocOffset := binary.LittleEndian.Uint64(meta[0:8])
	tocLength := binary.LittleEndian.Uint64(meta[8:16])
	entryCount := binary.LittleEndian.Uint64(meta[16:24])
	if tocOffset < HeaderSize || tocLength > maxTOCLength || tocOffset+tocLength > fileSize {
		return salt, key, nil, errors.New("temir: corrupted target TEMIR header")
	}

	tocBytes := make([]byte, tocLength)
	if _, err := f.ReadAt(tocBytes, int64(tocOffset)); err != nil {
		return salt, key, nil, err
	}
	expectedTag := computeHeaderAuthTag(&key, &salt, tocOffset, tocLength, entryCount, FNV1a64(tocBytes))
	if string(header[48:64]) != string(expectedTag[:]) {
		return salt, key, nil, errors.New("temir: target archive authentication tag mismatch")
	}
	ChaCha20XORAt(&key, &tocNonce, 0, tocBytes)
	entries, err := decodeTOC(tocBytes, int(entryCount))
	return salt, key, entries, err
}

/*
================
encodeHeader

Builds the 64-byte authenticated header for a TOC already encrypted in
tocCipher and written at tocOffset.
================
*/
func encodeHeader(masterKey *[32]byte, salt *[16]byte, tocOffset uint64, tocCipher []byte, entryCount uint64) [HeaderSize]byte {
	tocLength := uint64(len(tocCipher))
	authTag := computeHeaderAuthTag(masterKey, salt, tocOffset, tocLength, entryCount, FNV1a64(tocCipher))

	var meta [24]byte
	binary.LittleEndian.PutUint64(meta[0:8], tocOffset)
	binary.LittleEndian.PutUint64(meta[8:16], tocLength)
	binary.LittleEndian.PutUint64(meta[16:24], entryCount)
	ChaCha20XORAt(masterKey, &headerNonce, 0, meta[:])

	var header [HeaderSize]byte
	copy(header[0:8], temirMagic[:])
	copy(header[8:24], salt[:])
	copy(header[24:48], meta[:])
	copy(header[48:64], authTag[:])
	return header
}

/*
================
mergeEntriesIntoArchive

Appends the given entries of an opened PatchNNN.temir to a base archive past
its current end of file, then the new TOC, flushes, and only then rewrites
the header (and flushes again). A crash at any point leaves the old archive
or the new one; superseded payloads become dead bytes for compactArchive.
Offsets only grow, so deriving each nonce from its offset keeps nonces unique.
================
*/
func mergeEntriesIntoArchive(
	targetPath string,
	patchFile *os.File,
	patchMasterKey *[32]byte,
	updates []ArchiveEntry,
	passphrase string,
) error {
	if len(updates) == 0 {
		return nil
	}
	tf, err := os.OpenFile(targetPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer tf.Close()

	info, err := tf.Stat()
	if err != nil {
		return err
	}
	fileSize := uint64(info.Size())
	archiveSalt, targetMasterKey, entries, err := readArchiveHeader(tf, fileSize, passphrase)
	if err != nil {
		return err
	}

	// A new hashed bundle retires every older bundle of its family.
	incomingKeys := make(map[string]struct{}, len(updates))
	incomingFamilies := make(map[string]struct{})
	for _, u := range updates {
		key := normalizeVirtualKey(u.VirtualPath)
		incomingKeys[key] = struct{}{}
		if family := bundleFamily(key); family != "" {
			incomingFamilies[family] = struct{}{}
		}
	}
	if len(incomingFamilies) > 0 {
		kept := entries[:0]
		for _, e := range entries {
			key := normalizeVirtualKey(e.VirtualPath)
			family := bundleFamily(key)
			_, retired := incomingFamilies[family]
			_, incoming := incomingKeys[key]
			if family != "" && retired && !incoming {
				continue
			}
			kept = append(kept, e)
		}
		entries = kept
	}

	byKey := make(map[string]int, len(entries))
	for idx, e := range entries {
		byKey[normalizeVirtualKey(e.VirtualPath)] = idx
	}

	appendCursor := fileSize
	copyBuf := make([]byte, 256*1024)
	for _, patchEntry := range updates {
		normKey := normalizeVirtualKey(patchEntry.VirtualPath)
		existingIdx, exists := byKey[normKey]
		if exists && entries[existingIdx].DataLength == patchEntry.DataLength &&
			entries[existingIdx].Checksum == patchEntry.Checksum {
			continue
		}

		writeOffset := appendCursor
		newNonce := DeriveFileNonce(&archiveSalt, writeOffset, patchEntry.VirtualPath)
		var fileOffset uint64
		hash := fnvOffset64
		for fileOffset < patchEntry.DataLength {
			toRead := uint64(len(copyBuf))
			if remaining := patchEntry.DataLength - fileOffset; remaining < toRead {
				toRead = remaining
			}
			chunk := copyBuf[:toRead]
			if _, err := patchFile.ReadAt(chunk, int64(patchEntry.DataOffset+fileOffset)); err != nil {
				return err
			}
			ChaCha20XORAt(patchMasterKey, &patchEntry.FileNonce, fileOffset, chunk)
			for _, b := range chunk {
				hash ^= uint64(b)
				hash *= fnvPrime64
			}
			ChaCha20XORAt(&targetMasterKey, &newNonce, fileOffset, chunk)
			if _, err := tf.WriteAt(chunk, int64(writeOffset+fileOffset)); err != nil {
				return err
			}
			fileOffset += toRead
		}
		if hash != patchEntry.Checksum {
			return fmt.Errorf("temir: checksum mismatch while merging %s", patchEntry.VirtualPath)
		}
		appendCursor += patchEntry.DataLength

		newEntry := ArchiveEntry{
			VirtualPath: patchEntry.VirtualPath,
			DataOffset:  writeOffset,
			DataLength:  patchEntry.DataLength,
			FileNonce:   newNonce,
			Checksum:    patchEntry.Checksum,
		}
		if exists {
			entries[existingIdx] = newEntry
		} else {
			byKey[normKey] = len(entries)
			entries = append(entries, newEntry)
		}
	}

	newTOCBytes := encodeTOC(entries)
	ChaCha20XORAt(&targetMasterKey, &tocNonce, 0, newTOCBytes)
	if _, err := tf.WriteAt(newTOCBytes, int64(appendCursor)); err != nil {
		return err
	}
	if err := tf.Sync(); err != nil {
		return err
	}

	// Commit point: only now does the header move to the new TOC.
	newHeader := encodeHeader(&targetMasterKey, &archiveSalt, appendCursor, newTOCBytes, uint64(len(entries)))
	if _, err := tf.WriteAt(newHeader[:], 0); err != nil {
		return err
	}
	return tf.Sync()
}

/*
================
compactArchive

Rewrites archivePath without the dead bytes append-only merges leave behind,
once they pass minDeadBytes and 1/compactDeadRatioDivisor of the file.
Payloads are copied as ciphertext: each entry keeps its nonce and ChaCha20
keys on the offset inside the entry. The copy is flushed under a temporary
name and renamed over the original, so a crash leaves the old archive whole
and a download already streaming the old file keeps its handle. When the
rename is refused (Windows with an open reader) the copy is removed and the
uncompacted archive stays. Returns true when the archive was replaced.
================
*/
func compactArchive(archivePath, passphrase string, minDeadBytes uint64) (bool, error) {
	src, err := os.Open(archivePath)
	if err != nil {
		return false, err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return false, err
	}
	fileSize := uint64(info.Size())
	salt, masterKey, entries, err := readArchiveHeader(src, fileSize, passphrase)
	if err != nil {
		return false, err
	}

	var liveBytes uint64
	for _, e := range entries {
		liveBytes += e.DataLength
	}
	tocLength := uint64(len(encodeTOC(entries)))
	deadBytes := uint64(0)
	if used := HeaderSize + liveBytes + tocLength; fileSize > used {
		deadBytes = fileSize - used
	}
	if deadBytes < minDeadBytes || deadBytes*compactDeadRatioDivisor < fileSize {
		return false, nil
	}

	tempPath := archivePath + compactTempSuffix
	err = writeCompactCopy(src, tempPath, &salt, &masterKey, entries)
	// Windows refuses to replace a file this process still holds open.
	_ = src.Close()
	if err != nil {
		_ = os.Remove(tempPath)
		return false, err
	}
	if _, _, err := ReadArchiveTOC(tempPath, passphrase); err != nil {
		_ = os.Remove(tempPath)
		return false, err
	}
	if err := os.Rename(tempPath, archivePath); err != nil {
		_ = os.Remove(tempPath)
		return false, nil
	}
	return true, nil
}

/*
================
writeCompactCopy
================
*/
func writeCompactCopy(src *os.File, tempPath string, salt *[16]byte, masterKey *[32]byte, entries []ArchiveEntry) error {
	out, err := os.Create(tempPath)
	if err != nil {
		return err
	}
	defer out.Close()

	var zeroHeader [HeaderSize]byte
	if _, err := out.Write(zeroHeader[:]); err != nil {
		return err
	}

	// Copy in disk order so the source is read sequentially.
	order := make([]int, len(entries))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		return entries[order[a]].DataOffset < entries[order[b]].DataOffset
	})

	cursor := HeaderSize
	for _, idx := range order {
		e := &entries[idx]
		section := io.NewSectionReader(src, int64(e.DataOffset), int64(e.DataLength))
		if _, err := io.Copy(out, section); err != nil {
			return err
		}
		e.DataOffset = cursor
		cursor += e.DataLength
	}

	tocBytes := encodeTOC(entries)
	ChaCha20XORAt(masterKey, &tocNonce, 0, tocBytes)
	if _, err := out.Write(tocBytes); err != nil {
		return err
	}
	header := encodeHeader(masterKey, salt, cursor, tocBytes, uint64(len(entries)))
	if _, err := out.WriteAt(header[:], 0); err != nil {
		return err
	}
	return out.Sync()
}

/*
================
MergePatchIntoBaseArchives

Merges the entries of patchPath into the corresponding base archives
(Media.temir, Data.temir, World.temir) inside lastVersionDir. When only is not
nil, entries outside it are skipped (they belong to later patches). Entries the
archive already holds are skipped too. Returns true if any archive changed.
================
*/
func MergePatchIntoBaseArchives(lastVersionDir, patchPath, passphrase string, only map[string]struct{}) (bool, error) {
	if passphrase == "" {
		passphrase = DefaultArchiveKey
	}
	patchMasterKey, patchTOC, err := ReadArchiveTOC(patchPath, passphrase)
	if err != nil {
		return false, err
	}

	grouped := make(map[string][]ArchiveEntry, 3)
	for _, entry := range patchTOC {
		if only != nil {
			if _, ok := only[normalizeVirtualKey(entry.VirtualPath)]; !ok {
				continue
			}
		}
		targetName := ClassifyVirtualPath(entry.VirtualPath)
		grouped[targetName] = append(grouped[targetName], entry)
	}

	// Without the base archives there is no last_version to keep current
	// (first installs then come from the patches alone); nothing to do.
	for _, baseName := range ArchiveNames {
		if len(grouped[baseName]) == 0 {
			continue
		}
		if _, err := os.Stat(filepath.Join(lastVersionDir, baseName)); err != nil {
			return false, nil
		}
	}

	patchFile, err := os.Open(patchPath)
	if err != nil {
		return false, err
	}
	defer patchFile.Close()

	mergedAny := false
	for _, baseName := range ArchiveNames {
		targetPath := filepath.Join(lastVersionDir, baseName)
		missing, err := missingArchiveEntries(targetPath, passphrase, grouped[baseName])
		if err != nil {
			return mergedAny, fmt.Errorf("merge %s into %s: %w", filepath.Base(patchPath), baseName, err)
		}
		if len(missing) == 0 {
			continue
		}
		if err := mergeEntriesIntoArchive(targetPath, patchFile, &patchMasterKey, missing, passphrase); err != nil {
			return mergedAny, fmt.Errorf("merge %s into %s: %w", filepath.Base(patchPath), baseName, err)
		}
		mergedAny = true
	}
	return mergedAny, nil
}
