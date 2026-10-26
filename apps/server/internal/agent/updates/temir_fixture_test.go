/*
===========================================================================

temir_fixture_test.go - the TEMIR archives the browser reader is checked against

The browser client reads .temir archives from the Origin Private File System
(docs/PWA-OPFS-MIGRATION.md) with its own TypeScript reader. Both sides must
agree on every byte of the format, so these tests pack small fixed archives
with PackArchiveFile and require them to equal the files in testdata/: a base
archive (client-reader.temir) and a differential patch over it
(client-reader-patch.temir). check:shared-fixtures keeps the client's copies
(apps/client-next/tests/fixtures/temir/) byte-identical, and the client tests
decrypt and overlay those copies. A format change in the packer fails here
first; regenerate with SRO_WRITE_TEMIR_FIXTURE=1, then sync the copies.

===========================================================================
*/
package updates

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

const (
	// Not DefaultArchiveKey: the fixture proves the format, not the key.
	fixturePassphrase = "opensro-temir-fixture"
	// Long enough to span many 64-byte ChaCha20 blocks, so a reader that
	// starts mid-block is exercised.
	fixturePatternBytes = 1000
	fixturePatchBytes   = 300
)

/*
================
fixtureMember

One file of a fixture archive. The client tests repeat the same contents.
================
*/
type fixtureMember struct {
	virtual string
	data    []byte
}

/*
================
fixturePattern
================
*/
func fixturePattern(length, step, offset int) []byte {
	pattern := make([]byte, length)
	for i := range pattern {
		pattern[i] = byte(i*step + offset)
	}
	return pattern
}

/*
================
checkFixture

Packs members into archiveName in a temporary directory and requires the
result to equal path, or writes path when SRO_WRITE_TEMIR_FIXTURE=1.
================
*/
func checkFixture(t *testing.T, archiveName, path string, members []fixtureMember) {
	t.Helper()
	dir := t.TempDir()
	sources := make([]SourceFile, 0, len(members))
	for i, member := range members {
		disk := filepath.Join(dir, "member"+string(rune('0'+i)))
		if err := os.WriteFile(disk, member.data, 0o644); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, SourceFile{VirtualPath: member.virtual, DiskPath: disk})
	}
	out := filepath.Join(dir, archiveName)
	if _, _, err := PackArchiveFile(out, archiveName, sources, fixturePassphrase); err != nil {
		t.Fatal(err)
	}
	packed, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SRO_WRITE_TEMIR_FIXTURE") == "1" {
		if err := os.WriteFile(path, packed, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(packed))
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v (regenerate with SRO_WRITE_TEMIR_FIXTURE=1)", err)
	}
	if !bytes.Equal(packed, want) {
		t.Fatalf("PackArchiveFile no longer produces %s: the TEMIR format changed", path)
	}
}

/*
================
TestTemirClientReaderFixture

The base archive: a short text file and a byte pattern.
================
*/
func TestTemirClientReaderFixture(t *testing.T) {
	checkFixture(t, "Media.temir", "testdata/client-reader.temir", []fixtureMember{
		{"assets/packs/manifest.json", []byte(`{"format":"temir-fixture"}`)},
		{"assets/packs/map-001-000000000000.bin", fixturePattern(fixturePatternBytes, 31, 7)},
	})
}

/*
================
TestTemirClientReaderPatchFixture

A differential patch over the base: a changed manifest and a new file, the
shape sync.go gives PatchNNN.temir.
================
*/
func TestTemirClientReaderPatchFixture(t *testing.T) {
	checkFixture(t, "Patch001.temir", "testdata/client-reader-patch.temir", []fixtureMember{
		{"assets/packs/manifest.json", []byte(`{"format":"temir-fixture","patch":1}`)},
		{"assets/packs/new-001-000000000000.bin", fixturePattern(fixturePatchBytes, 13, 1)},
	})
}
