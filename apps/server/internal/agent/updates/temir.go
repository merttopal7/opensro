/*
===========================================================================

temir.go - encrypted TEMIR (.temir) archive container and TOC reader/writer

Implements the cross-platform TEMIR v1 archive format shared with the desktop
client updater:
  - Key derivation from passphrase + per-archive 128-bit salt (4,096 rounds of
    ChaCha20 sponge mixing)
  - Authenticated 64-byte header + encrypted Table of Contents (TOC)
  - RFC 8439 20-round ChaCha20 stream encryption per file entry with 64-bit
    block counter folded into nonce[0..4]
  - 64-bit FNV-1a plaintext checksums

===========================================================================
*/
package updates

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"os"
	"strings"
)

const (
	// HeaderSize is the fixed byte size of a TEMIR v1 archive header.
	HeaderSize uint64 = 64

	// DefaultArchiveKey is the default passphrase shared with the client runtime.
	DefaultArchiveKey = "OpenSRO-v150-Legend3-TEMIR-Key-2026!#99"

	fnvOffset64 uint64 = 0xcbf29ce484222325
	fnvPrime64  uint64 = 0x0100000001b3

	maxTOCLength uint64 = 64 * 1024 * 1024
)

var (
	temirMagic  = [8]byte{'T', 'E', 'M', 'I', 'R', 1, 0, 0}
	headerNonce = [12]byte{'T', 'E', 'M', 'I', 'R', '-', 'H', 'D', 'R', '-', '0', '1'}
	tocNonce    = [12]byte{'T', 'E', 'M', 'I', 'R', '-', 'T', 'O', 'C', '-', '0', '1'}
	macNonce    = [12]byte{'T', 'E', 'M', 'I', 'R', '-', 'M', 'A', 'C', '-', '0', '1'}

	// ArchiveNames lists the three base client archive containers.
	ArchiveNames = [3]string{"Media.temir", "Data.temir", "World.temir"}
)

/*
================
SourceFile

Pairs a normalized virtual archive path with its source path on disk.
================
*/
type SourceFile struct {
	VirtualPath string
	DiskPath    string
}

/*
================
ArchiveEntry

Describes one file entry stored inside a TEMIR archive's Table of Contents.
================
*/
type ArchiveEntry struct {
	VirtualPath string
	DataOffset  uint64
	DataLength  uint64
	FileNonce   [12]byte
	Checksum    uint64
}

/*
================
ClassifyVirtualPath

Maps a normalized virtual URL path to one of the three base .temir archives.
================
*/
func ClassifyVirtualPath(virtualPath string) string {
	lower := strings.ToLower(strings.TrimPrefix(virtualPath, "/"))
	if strings.HasPrefix(lower, "assets/packs/") {
		packName := strings.TrimPrefix(lower, "assets/packs/")
		if strings.HasPrefix(packName, "map-") ||
			strings.HasPrefix(packName, "outdoor-world-") ||
			strings.HasPrefix(packName, "mission-minimap-") ||
			strings.HasPrefix(packName, "world-textures-") ||
			strings.HasPrefix(packName, "game-audio-") ||
			strings.HasPrefix(packName, "snd-") ||
			strings.HasPrefix(packName, "particle-") ||
			strings.HasPrefix(packName, "fx-") {
			return "World.temir"
		}
		if strings.HasPrefix(packName, "equipment-models-") ||
			strings.HasPrefix(packName, "game-models-") ||
			strings.HasPrefix(packName, "hwan-models-") ||
			strings.HasPrefix(packName, "mission-cos-models-") ||
			strings.HasPrefix(packName, "mission-npc-vat-") ||
			strings.HasPrefix(packName, "title-crowd-vat-") ||
			strings.HasPrefix(packName, "texture-variants-") ||
			strings.HasPrefix(packName, "char-") ||
			strings.HasPrefix(packName, "item-") ||
			strings.HasPrefix(packName, "mob-") ||
			strings.HasPrefix(packName, "obj-") {
			return "Data.temir"
		}
	}
	return "Media.temir"
}

/*
================
FNV1a64

Computes the 64-bit FNV-1a hash of a byte slice.
================
*/
func FNV1a64(data []byte) uint64 {
	hash := fnvOffset64
	for _, b := range data {
		hash ^= uint64(b)
		hash *= fnvPrime64
	}
	return hash
}

/*
================
FNV1a64File

Streams a file from disk and returns its byte length and 64-bit FNV-1a hash.
================
*/
func FNV1a64File(path string) (uint64, uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	buf := make([]byte, 64*1024)
	hash := fnvOffset64
	var totalLen uint64
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			for _, b := range chunk {
				hash ^= uint64(b)
				hash *= fnvPrime64
			}
			totalLen += uint64(n)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return 0, 0, readErr
		}
	}
	return totalLen, hash, nil
}

/*
================
quarterRound
================
*/
func quarterRound(state *[16]uint32, a, b, c, d int) {
	state[a] += state[b]
	state[d] = bits.RotateLeft32(state[d]^state[a], 16)

	state[c] += state[d]
	state[b] = bits.RotateLeft32(state[b]^state[c], 12)

	state[a] += state[b]
	state[d] = bits.RotateLeft32(state[d]^state[a], 8)

	state[c] += state[d]
	state[b] = bits.RotateLeft32(state[b]^state[c], 7)
}

/*
================
chacha20Block

RFC 8439 20-round ChaCha20 block function producing 64 keystream bytes.
Folds the upper 32 bits of the 64-bit block counter into nonce[0..4] to match
sropk.rs.
================
*/
func chacha20Block(key *[32]byte, nonce *[12]byte, blockCounter uint64) [64]byte {
	var state [16]uint32
	state[0] = 0x61707865
	state[1] = 0x3320646e
	state[2] = 0x79622d32
	state[3] = 0x6b206574

	for i := 0; i < 8; i++ {
		off := i * 4
		state[4+i] = binary.LittleEndian.Uint32(key[off : off+4])
	}

	counterLo := uint32(blockCounter & 0xffffffff)
	counterHi := uint32(blockCounter >> 32)
	n0 := binary.LittleEndian.Uint32(nonce[0:4]) ^ counterHi
	n1 := binary.LittleEndian.Uint32(nonce[4:8])
	n2 := binary.LittleEndian.Uint32(nonce[8:12])

	state[12] = counterLo
	state[13] = n0
	state[14] = n1
	state[15] = n2

	working := state
	for i := 0; i < 10; i++ {
		quarterRound(&working, 0, 4, 8, 12)
		quarterRound(&working, 1, 5, 9, 13)
		quarterRound(&working, 2, 6, 10, 14)
		quarterRound(&working, 3, 7, 11, 15)

		quarterRound(&working, 0, 5, 10, 15)
		quarterRound(&working, 1, 6, 11, 12)
		quarterRound(&working, 2, 7, 8, 13)
		quarterRound(&working, 3, 4, 9, 14)
	}

	var out [64]byte
	for i := 0; i < 16; i++ {
		off := i * 4
		binary.LittleEndian.PutUint32(out[off:off+4], working[i]+state[i])
	}
	return out
}

/*
================
ChaCha20XORAt

XORs buf in-place with the ChaCha20 keystream starting at arbitrary byte offset
streamOffset.
================
*/
func ChaCha20XORAt(key *[32]byte, nonce *[12]byte, streamOffset uint64, buf []byte) {
	pos := 0
	curOffset := streamOffset
	for pos < len(buf) {
		blockIdx := curOffset / 64
		inBlock := int(curOffset % 64)
		keystream := chacha20Block(key, nonce, blockIdx)
		avail := 64 - inBlock
		take := len(buf) - pos
		if take > avail {
			take = avail
		}
		for i := 0; i < take; i++ {
			buf[pos+i] ^= keystream[inBlock+i]
		}
		pos += take
		curOffset += uint64(take)
	}
}

/*
================
DeriveMasterKey

Derives a 256-bit master key from a passphrase and 16-byte archive salt using
4,096 rounds of ChaCha20 sponge mixing, matching sropk.rs byte-for-byte.
================
*/
func DeriveMasterKey(passphrase string, archiveSalt *[16]byte) [32]byte {
	var key [32]byte
	passBytes := []byte(passphrase)
	for i := 0; i < 32; i++ {
		var p byte = 0x5a
		if len(passBytes) > 0 {
			p = passBytes[i%len(passBytes)]
		}
		s := archiveSalt[i%16]
		key[i] = p ^ bits.RotateLeft8(s, i%7) ^ (byte(i) * 0x9e)
	}

	var nonce [12]byte
	copy(nonce[:], archiveSalt[0:12])
	for i, b := range passBytes {
		key[i%32] = bits.RotateLeft8(key[i%32]+b, 3)
		nonce[i%12] ^= b
	}

	for round := uint64(0); round < 4096; round++ {
		block := chacha20Block(&key, &nonce, round)
		for i := 0; i < 32; i++ {
			key[i] ^= block[i] ^ block[32+i]
		}
		nonce[round%12] ^= block[round%64]
	}
	return key
}

/*
================
DeriveFileNonce
================
*/
func DeriveFileNonce(archiveSalt *[16]byte, index uint64, virtualPath string) [12]byte {
	var pathHash [8]byte
	var idxBytes [8]byte
	binary.LittleEndian.PutUint64(pathHash[:], FNV1a64([]byte(virtualPath)))
	binary.LittleEndian.PutUint64(idxBytes[:], index)

	var nonce [12]byte
	for i := 0; i < 12; i++ {
		nonce[i] = archiveSalt[i] ^ pathHash[i%8] ^ bits.RotateLeft8(idxBytes[i%8], i%5)
	}
	return nonce
}

/*
================
computeHeaderAuthTag
================
*/
func computeHeaderAuthTag(
	masterKey *[32]byte,
	archiveSalt *[16]byte,
	tocOffset uint64,
	tocLength uint64,
	entryCount uint64,
	tocCipherHash uint64,
) [16]byte {
	var blockIn [64]byte
	copy(blockIn[0:8], temirMagic[:])
	copy(blockIn[8:24], archiveSalt[:])
	binary.LittleEndian.PutUint64(blockIn[24:32], tocOffset)
	binary.LittleEndian.PutUint64(blockIn[32:40], tocLength)
	binary.LittleEndian.PutUint64(blockIn[40:48], entryCount)
	binary.LittleEndian.PutUint64(blockIn[48:56], tocCipherHash)
	binary.LittleEndian.PutUint64(blockIn[56:64], tocOffset^tocLength^entryCount)

	ChaCha20XORAt(masterKey, &macNonce, 0, blockIn[:])
	stream := chacha20Block(masterKey, &macNonce, 1)
	var tag [16]byte
	for i := 0; i < 16; i++ {
		tag[i] = blockIn[i] ^ blockIn[16+i] ^ blockIn[32+i] ^ blockIn[48+i] ^ stream[i]
	}
	return tag
}

/*
================
encodeTOC
================
*/
func encodeTOC(entries []ArchiveEntry) []byte {
	buf := make([]byte, 0, len(entries)*64)
	var tmp8 [8]byte
	var tmp2 [2]byte
	for _, e := range entries {
		pathBytes := []byte(e.VirtualPath)
		binary.LittleEndian.PutUint16(tmp2[:], uint16(len(pathBytes)))
		buf = append(buf, tmp2[:]...)
		buf = append(buf, pathBytes...)

		binary.LittleEndian.PutUint64(tmp8[:], e.DataOffset)
		buf = append(buf, tmp8[:]...)
		binary.LittleEndian.PutUint64(tmp8[:], e.DataLength)
		buf = append(buf, tmp8[:]...)
		buf = append(buf, e.FileNonce[:]...)
		binary.LittleEndian.PutUint64(tmp8[:], e.Checksum)
		buf = append(buf, tmp8[:]...)
	}
	return buf
}

/*
================
decodeTOC
================
*/
func decodeTOC(buf []byte, expectedCount int) ([]ArchiveEntry, error) {
	entries := make([]ArchiveEntry, 0, expectedCount)
	pos := 0
	for i := 0; i < expectedCount; i++ {
		if pos+2 > len(buf) {
			return nil, errors.New("temir: truncated TOC path length")
		}
		pathLen := int(binary.LittleEndian.Uint16(buf[pos : pos+2]))
		pos += 2
		if pos+pathLen+8+8+12+8 > len(buf) {
			return nil, errors.New("temir: truncated TOC entry")
		}
		virtualPath := string(buf[pos : pos+pathLen])
		pos += pathLen

		dataOffset := binary.LittleEndian.Uint64(buf[pos : pos+8])
		pos += 8
		dataLength := binary.LittleEndian.Uint64(buf[pos : pos+8])
		pos += 8
		var fileNonce [12]byte
		copy(fileNonce[:], buf[pos:pos+12])
		pos += 12
		checksum := binary.LittleEndian.Uint64(buf[pos : pos+8])
		pos += 8

		entries = append(entries, ArchiveEntry{
			VirtualPath: virtualPath,
			DataOffset:  dataOffset,
			DataLength:  dataLength,
			FileNonce:   fileNonce,
			Checksum:    checksum,
		})
	}
	return entries, nil
}

/*
================
PackArchiveFile

Writes a single encrypted .temir archive file from a slice of SourceFile pairs
using the provided passphrase.
================
*/
func PackArchiveFile(
	outputPath string,
	archiveName string,
	files []SourceFile,
	passphrase string,
) (int, uint64, error) {
	saltSeed := fmt.Sprintf("TEMIR-SALT-2026:%s:%d", archiveName, len(files))
	h1 := FNV1a64([]byte(saltSeed))
	h2 := bits.RotateLeft64(FNV1a64([]byte(passphrase)), 17)
	var archiveSalt [16]byte
	binary.LittleEndian.PutUint64(archiveSalt[0:8], h1)
	binary.LittleEndian.PutUint64(archiveSalt[8:16], h2)

	masterKey := DeriveMasterKey(passphrase, &archiveSalt)

	out, err := os.Create(outputPath)
	if err != nil {
		return 0, 0, err
	}
	defer out.Close()

	var zeroHeader [HeaderSize]byte
	if _, err := out.Write(zeroHeader[:]); err != nil {
		return 0, 0, err
	}

	currentOffset := HeaderSize
	entries := make([]ArchiveEntry, 0, len(files))
	copyBuf := make([]byte, 256*1024)

	for idx, sf := range files {
		fileLen, hash, err := packSingleEntry(
			out,
			sf,
			uint64(idx),
			&archiveSalt,
			&masterKey,
			copyBuf,
		)
		if err != nil {
			return 0, 0, err
		}
		fileNonce := DeriveFileNonce(&archiveSalt, uint64(idx), sf.VirtualPath)
		entries = append(entries, ArchiveEntry{
			VirtualPath: sf.VirtualPath,
			DataOffset:  currentOffset,
			DataLength:  fileLen,
			FileNonce:   fileNonce,
			Checksum:    hash,
		})
		currentOffset += fileLen
	}

	tocOffset := currentOffset
	tocBytes := encodeTOC(entries)
	tocLength := uint64(len(tocBytes))
	ChaCha20XORAt(&masterKey, &tocNonce, 0, tocBytes)
	tocCipherHash := FNV1a64(tocBytes)
	if _, err := out.Write(tocBytes); err != nil {
		return 0, 0, err
	}

	entryCount := uint64(len(entries))
	authTag := computeHeaderAuthTag(
		&masterKey,
		&archiveSalt,
		tocOffset,
		tocLength,
		entryCount,
		tocCipherHash,
	)

	var meta [24]byte
	binary.LittleEndian.PutUint64(meta[0:8], tocOffset)
	binary.LittleEndian.PutUint64(meta[8:16], tocLength)
	binary.LittleEndian.PutUint64(meta[16:24], entryCount)
	ChaCha20XORAt(&masterKey, &headerNonce, 0, meta[:])

	var header [HeaderSize]byte
	copy(header[0:8], temirMagic[:])
	copy(header[8:24], archiveSalt[:])
	copy(header[24:48], meta[:])
	copy(header[48:64], authTag[:])

	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return 0, 0, err
	}
	if _, err := out.Write(header[:]); err != nil {
		return 0, 0, err
	}
	if err := out.Sync(); err != nil {
		return 0, 0, err
	}
	return len(entries), tocOffset + tocLength, nil
}

/*
================
packSingleEntry
================
*/
func packSingleEntry(
	out io.Writer,
	sf SourceFile,
	idx uint64,
	archiveSalt *[16]byte,
	masterKey *[32]byte,
	copyBuf []byte,
) (uint64, uint64, error) {
	src, err := os.Open(sf.DiskPath)
	if err != nil {
		return 0, 0, err
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return 0, 0, err
	}
	fileLen := uint64(info.Size())
	fileNonce := DeriveFileNonce(archiveSalt, idx, sf.VirtualPath)

	var fileOffset uint64
	hash := fnvOffset64

	for fileOffset < fileLen {
		remaining := fileLen - fileOffset
		toRead := len(copyBuf)
		if remaining < uint64(toRead) {
			toRead = int(remaining)
		}
		n, readErr := src.Read(copyBuf[:toRead])
		if n > 0 {
			chunk := copyBuf[:n]
			for _, b := range chunk {
				hash ^= uint64(b)
				hash *= fnvPrime64
			}
			ChaCha20XORAt(masterKey, &fileNonce, fileOffset, chunk)
			if _, writeErr := out.Write(chunk); writeErr != nil {
				return 0, 0, writeErr
			}
			fileOffset += uint64(n)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return 0, 0, readErr
		}
	}
	return fileLen, hash, nil
}

/*
================
ReadArchiveTOC

Opens an encrypted .temir archive, validates the passphrase via the header
authentication tag, and returns the master key plus decrypted TOC entries.
================
*/
func ReadArchiveTOC(archivePath, passphrase string) ([32]byte, []ArchiveEntry, error) {
	var zeroKey [32]byte
	f, err := os.Open(archivePath)
	if err != nil {
		return zeroKey, nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return zeroKey, nil, err
	}
	fileSize := uint64(info.Size())
	if fileSize < HeaderSize {
		return zeroKey, nil, errors.New("temir: file too small to be TEMIR archive")
	}

	var header [HeaderSize]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return zeroKey, nil, err
	}
	if string(header[0:8]) != string(temirMagic[:]) {
		return zeroKey, nil, errors.New("temir: invalid TEMIR magic header")
	}

	var archiveSalt [16]byte
	copy(archiveSalt[:], header[8:24])
	masterKey := DeriveMasterKey(passphrase, &archiveSalt)

	var meta [24]byte
	copy(meta[:], header[24:48])
	ChaCha20XORAt(&masterKey, &headerNonce, 0, meta[:])

	tocOffset := binary.LittleEndian.Uint64(meta[0:8])
	tocLength := binary.LittleEndian.Uint64(meta[8:16])
	entryCount := binary.LittleEndian.Uint64(meta[16:24])

	if tocOffset < HeaderSize || tocLength > maxTOCLength || tocOffset+tocLength > fileSize {
		return zeroKey, nil, errors.New("temir: invalid archive key or corrupted TEMIR header")
	}

	if _, err := f.Seek(int64(tocOffset), io.SeekStart); err != nil {
		return zeroKey, nil, err
	}
	tocBytes := make([]byte, tocLength)
	if _, err := io.ReadFull(f, tocBytes); err != nil {
		return zeroKey, nil, err
	}
	tocCipherHash := FNV1a64(tocBytes)

	expectedTag := computeHeaderAuthTag(
		&masterKey,
		&archiveSalt,
		tocOffset,
		tocLength,
		entryCount,
		tocCipherHash,
	)
	if string(header[48:64]) != string(expectedTag[:]) {
		return zeroKey, nil, errors.New("temir: invalid archive key (authentication tag mismatch)")
	}

	ChaCha20XORAt(&masterKey, &tocNonce, 0, tocBytes)
	entries, err := decodeTOC(tocBytes, int(entryCount))
	if err != nil {
		return zeroKey, nil, err
	}
	return masterKey, entries, nil
}
