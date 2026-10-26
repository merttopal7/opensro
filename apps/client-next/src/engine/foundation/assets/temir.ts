/*
===========================================================================

temir.ts - read-only TEMIR (.temir) archive format

The installed PWA keeps the game's three .temir archives (Media, Data, World)
in the Origin Private File System and reads them the way the desktop shell
does (docs/PWA-OPFS-MIGRATION.md). This module is the format alone: header
authentication, the encrypted table of contents and random-access ChaCha20
decryption of member bytes. It owns no storage and no I/O; callers hand it
the bytes they read.

The writers are the Go packer (apps/server/internal/agent/updates/temir.go)
and the desktop shell (new-silkroad sropk.rs). Every rule here mirrors them,
and tests/runtime/temir-reader.test.mjs decrypts an archive the Go packer
wrote (check:shared-fixtures keeps that copy identical).

Layout, little-endian:
  header  0..8 magic, 8..24 salt, 24..48 encrypted {tocOffset, tocLength,
          entryCount}, 48..64 authentication tag
  members ChaCha20 under the master key and the member's own nonce, stream
          offset 0 at the member's first byte
  toc     ChaCha20 under TOC_NONCE; per entry u16 path length, path, u64
          offset, u64 length, 12-byte nonce, u64 FNV-1a plaintext checksum

The ChaCha20 block counter is 64 bits: its high word is folded into the first
nonce word, so any byte of any member decrypts in O(1).

===========================================================================
*/

export const TEMIR_HEADER_BYTES = 64;
// sropk.rs read_archive_toc refuses larger tables; so does this reader.
// 64 MiB. sropk.rs read_archive_toc refuses larger tables; so does this reader.
export const TEMIR_MAX_TOC_BYTES = 0x400_0000;

// "TEMIR" 01 00 00
const TEMIR_MAGIC = [ 0x54, 0x45, 0x4d, 0x49, 0x52, 0x01, 0x00, 0x00 ] as const;
// The ASCII nonces "TEMIR-HDR-01", "TEMIR-TOC-01" and "TEMIR-MAC-01".
const HEADER_NONCE = [ 0x54, 0x45, 0x4d, 0x49, 0x52, 0x2d, 0x48, 0x44, 0x52, 0x2d, 0x30, 0x31 ] as const;
const TOC_NONCE = [ 0x54, 0x45, 0x4d, 0x49, 0x52, 0x2d, 0x54, 0x4f, 0x43, 0x2d, 0x30, 0x31 ] as const;
const MAC_NONCE = [ 0x54, 0x45, 0x4d, 0x49, 0x52, 0x2d, 0x4d, 0x41, 0x43, 0x2d, 0x30, 0x31 ] as const;
const KEY_DERIVATION_ROUNDS = 4096;
const BLOCK_BYTES = 64;
const BLOCK_WORDS = 16;
const NONCE_BYTES = 12;
const KEY_BYTES = 32;
const KEY_WORDS = 8;
const SALT_BYTES = 16;
// Fixed part of a TOC entry after its path: u64 offset, u64 length, 12-byte
// nonce, u64 checksum.
const TOC_ENTRY_TAIL_BYTES = 36;
const TWO_POW_32 = 0x1_0000_0000;
const FNV_OFFSET_LO = 0x8422_2325;
const FNV_OFFSET_HI = 0xcbf2_9ce4;
// The FNV-1a 64 prime is 2^40 + 0x1b3 (fnv1a64).
const FNV_PRIME_LOW = 0x1b3;
const TEMIR_FORMAT_ERROR = "TEMIR_FORMAT";

/*
================
TemirEntry

One member of an archive. offset and length are absolute file positions;
checksum is the 64-bit FNV-1a of the plaintext.
================
*/
export interface TemirEntry {
	path: string;
	offset: number;
	length: number;
	nonce: Uint8Array;
	checksum: bigint;
}

/*
================
TemirHeader

What the 64-byte header says once the key opened it. The TOC location is
not trusted until verifyTemirToc has checked the tag against the TOC bytes.
================
*/
export interface TemirHeader {
	salt: Uint8Array;
	masterKey: Uint8Array;
	tocOffset: number;
	tocLength: number;
	entryCount: number;
	tag: Uint8Array;
}

/*
================
TemirArchive

An opened archive: the key words its members decrypt under and its entries
by normalized virtual path.
================
*/
export interface TemirArchive {
	keyWords: Uint32Array;
	entries: Map<string, TemirEntry>;
}

/*
================
formatError

The bytes are not an archive this key opens. The code tells a wrong key or
a damaged file (download it again) apart from a storage fault without
matching error prose (isTemirFormatError).
================
*/
function formatError( message: string ) {
	return Object.assign( new Error( message ), { code: TEMIR_FORMAT_ERROR } );
}

/*
================
isTemirFormatError
================
*/
export function isTemirFormatError( error: unknown ): boolean {
	return error instanceof Error && "code" in error && error.code === TEMIR_FORMAT_ERROR;
}

/*
================
keyWordsOf

A 32-byte key as the eight little-endian words the block function reads.
Built byte by byte, so the host's own byte order never matters.
================
*/
export function keyWordsOf( key: Uint8Array ) {
	if ( key.length !== KEY_BYTES ) throw new Error( "TEMIR key must be 32 bytes" );
	const words = new Uint32Array( KEY_WORDS );
	for ( let i = 0; i < KEY_WORDS; i++ ) {
		words[i] = key[i * 4]! | (key[i * 4 + 1]! << 8) | (key[i * 4 + 2]! << 16) | (key[i * 4 + 3]! << 24);
	}
	return words;
}

/*
================
keystreamByte

Byte k of a block held as 16 little-endian words.
================
*/
function keystreamByte( words: Uint32Array, k: number ) {
	return (words[k >>> 2]! >>> ((k & 3) << 3)) & 0xff;
}

// ============================================================================

/*
================
chachaBlock

RFC 8439 20-round block function into out (16 words). counter is the 64-bit
block index: its low word is the block counter and its high word is XORed
into the first nonce word, as sropk.rs chacha20_block does.
================
*/
function chachaBlock( key: Uint32Array, nonce: ArrayLike<number>, counter: number, out: Uint32Array ) {
	const counterHi = Math.floor( counter / TWO_POW_32 ), counterLo = counter >>> 0;
	// Callers pass a 12-byte nonce and eight key words (keyWordsOf): the
	// assertions below are those lengths, not unchecked input.
	const n0 = (nonce[0]! | (nonce[1]! << 8) | (nonce[2]! << 16) | (nonce[3]! << 24)) ^ counterHi;
	const n1 = nonce[4]! | (nonce[5]! << 8) | (nonce[6]! << 16) | (nonce[7]! << 24);
	const n2 = nonce[8]! | (nonce[9]! << 8) | (nonce[10]! << 16) | (nonce[11]! << 24);
	const j0 = 0x6170_7865, j1 = 0x3320_646e, j2 = 0x7962_2d32, j3 = 0x6b20_6574;
	const j4 = key[0]!,
		j5 = key[1]!,
		j6 = key[2]!,
		j7 = key[3]!,
		j8 = key[4]!,
		j9 = key[5]!,
		j10 = key[6]!,
		j11 = key[7]!;
	const j12 = counterLo, j13 = n0, j14 = n1, j15 = n2;
	let x0 = j0, x1 = j1, x2 = j2, x3 = j3, x4 = j4, x5 = j5, x6 = j6, x7 = j7;
	let x8 = j8, x9 = j9, x10 = j10, x11 = j11, x12 = j12, x13 = j13, x14 = j14, x15 = j15;
	// Unrolled column and diagonal rounds: this is the decrypt hot path.
	for ( let round = 0; round < 10; round++ ) {
		x0 = (x0 + x4) | 0, x12 ^= x0, x12 = (x12 << 16) | (x12 >>> 16);
		x8 = (x8 + x12) | 0, x4 ^= x8, x4 = (x4 << 12) | (x4 >>> 20);
		x0 = (x0 + x4) | 0, x12 ^= x0, x12 = (x12 << 8) | (x12 >>> 24);
		x8 = (x8 + x12) | 0, x4 ^= x8, x4 = (x4 << 7) | (x4 >>> 25);
		x1 = (x1 + x5) | 0, x13 ^= x1, x13 = (x13 << 16) | (x13 >>> 16);
		x9 = (x9 + x13) | 0, x5 ^= x9, x5 = (x5 << 12) | (x5 >>> 20);
		x1 = (x1 + x5) | 0, x13 ^= x1, x13 = (x13 << 8) | (x13 >>> 24);
		x9 = (x9 + x13) | 0, x5 ^= x9, x5 = (x5 << 7) | (x5 >>> 25);
		x2 = (x2 + x6) | 0, x14 ^= x2, x14 = (x14 << 16) | (x14 >>> 16);
		x10 = (x10 + x14) | 0, x6 ^= x10, x6 = (x6 << 12) | (x6 >>> 20);
		x2 = (x2 + x6) | 0, x14 ^= x2, x14 = (x14 << 8) | (x14 >>> 24);
		x10 = (x10 + x14) | 0, x6 ^= x10, x6 = (x6 << 7) | (x6 >>> 25);
		x3 = (x3 + x7) | 0, x15 ^= x3, x15 = (x15 << 16) | (x15 >>> 16);
		x11 = (x11 + x15) | 0, x7 ^= x11, x7 = (x7 << 12) | (x7 >>> 20);
		x3 = (x3 + x7) | 0, x15 ^= x3, x15 = (x15 << 8) | (x15 >>> 24);
		x11 = (x11 + x15) | 0, x7 ^= x11, x7 = (x7 << 7) | (x7 >>> 25);

		x0 = (x0 + x5) | 0, x15 ^= x0, x15 = (x15 << 16) | (x15 >>> 16);
		x10 = (x10 + x15) | 0, x5 ^= x10, x5 = (x5 << 12) | (x5 >>> 20);
		x0 = (x0 + x5) | 0, x15 ^= x0, x15 = (x15 << 8) | (x15 >>> 24);
		x10 = (x10 + x15) | 0, x5 ^= x10, x5 = (x5 << 7) | (x5 >>> 25);
		x1 = (x1 + x6) | 0, x12 ^= x1, x12 = (x12 << 16) | (x12 >>> 16);
		x11 = (x11 + x12) | 0, x6 ^= x11, x6 = (x6 << 12) | (x6 >>> 20);
		x1 = (x1 + x6) | 0, x12 ^= x1, x12 = (x12 << 8) | (x12 >>> 24);
		x11 = (x11 + x12) | 0, x6 ^= x11, x6 = (x6 << 7) | (x6 >>> 25);
		x2 = (x2 + x7) | 0, x13 ^= x2, x13 = (x13 << 16) | (x13 >>> 16);
		x8 = (x8 + x13) | 0, x7 ^= x8, x7 = (x7 << 12) | (x7 >>> 20);
		x2 = (x2 + x7) | 0, x13 ^= x2, x13 = (x13 << 8) | (x13 >>> 24);
		x8 = (x8 + x13) | 0, x7 ^= x8, x7 = (x7 << 7) | (x7 >>> 25);
		x3 = (x3 + x4) | 0, x14 ^= x3, x14 = (x14 << 16) | (x14 >>> 16);
		x9 = (x9 + x14) | 0, x4 ^= x9, x4 = (x4 << 12) | (x4 >>> 20);
		x3 = (x3 + x4) | 0, x14 ^= x3, x14 = (x14 << 8) | (x14 >>> 24);
		x9 = (x9 + x14) | 0, x4 ^= x9, x4 = (x4 << 7) | (x4 >>> 25);
	}
	out[0] = x0 + j0, out[1] = x1 + j1, out[2] = x2 + j2, out[3] = x3 + j3;
	out[4] = x4 + j4, out[5] = x5 + j5, out[6] = x6 + j6, out[7] = x7 + j7;
	out[8] = x8 + j8, out[9] = x9 + j9, out[10] = x10 + j10, out[11] = x11 + j11;
	out[12] = x12 + j12, out[13] = x13 + j13, out[14] = x14 + j14, out[15] = x15 + j15;
}

/*
================
chachaXorAt

XORs buf in place with the keystream starting at byte streamOffset, the
counterpart of sropk.rs chacha20_xor_at. Decrypting a member range is
chachaXorAt( keyWords, entry.nonce, start, bytesReadFromFile ).
================
*/
export function chachaXorAt( key: Uint32Array, nonce: ArrayLike<number>, streamOffset: number, buf: Uint8Array ) {
	const words = new Uint32Array( BLOCK_WORDS );
	let pos = 0, block = Math.floor( streamOffset / BLOCK_BYTES ), inBlock = streamOffset % BLOCK_BYTES;
	while ( pos < buf.length ) {
		chachaBlock( key, nonce, block, words );
		const take = Math.min( buf.length - pos, BLOCK_BYTES - inBlock );
		for ( let i = 0; i < take; i++ ) buf[pos + i]! ^= keystreamByte( words, inBlock + i );
		pos += take;
		block++;
		inBlock = 0;
	}
}

// ============================================================================

/*
================
Fnv64

A running 64-bit FNV-1a as two 32-bit halves, plain numbers so a download
can store it beside its offset and resume the digest later.
================
*/
export interface Fnv64 {
	lo: number;
	hi: number;
}

/*
================
fnv1a64Start
================
*/
export function fnv1a64Start(): Fnv64 {
	return { lo: FNV_OFFSET_LO, hi: FNV_OFFSET_HI };
}

/*
================
fnv1a64Update

Folds bytes into a running digest and returns the new one. Multiplying by
the prime 2^40 + 0x1b3 is hash * 0x1b3 plus hash shifted left 40, whose
high word is the low word's bottom 24 bits shifted left 8. Every
intermediate stays below 2^53.
================
*/
export function fnv1a64Update( state: Fnv64, bytes: Uint8Array ): Fnv64 {
	let lo = state.lo, hi = state.hi;
	for ( let i = 0; i < bytes.length; i++ ) {
		lo = (lo ^ bytes[i]!) >>> 0;
		const low = lo * FNV_PRIME_LOW;
		const carry = Math.floor( low / TWO_POW_32 );
		hi = (hi * FNV_PRIME_LOW + carry + ((lo << 8) >>> 0)) >>> 0;
		lo = low >>> 0;
	}
	return { lo, hi };
}

/*
================
fnv1a64Value
================
*/
export function fnv1a64Value( state: Fnv64 ): bigint {
	return (BigInt( state.hi ) << 32n) | BigInt( state.lo );
}

/*
================
fnv1a64

The 64-bit FNV-1a of bytes, as the writers' checksums are.
================
*/
export function fnv1a64( bytes: Uint8Array ): bigint {
	return fnv1a64Value( fnv1a64Update( fnv1a64Start(), bytes ) );
}

/*
================
rotl8
================
*/
function rotl8( value: number, shift: number ) {
	return ((value << shift) | (value >>> (8 - shift))) & 0xff;
}

/*
================
deriveTemirKey

The archive master key from the passphrase and the archive's salt: a byte
mix of both, then 4096 rounds of ChaCha20 sponge mixing (sropk.rs
derive_master_key). A few milliseconds per archive, paid once on open.
================
*/
export function deriveTemirKey( passphrase: string, salt: Uint8Array ) {
	if ( salt.length !== SALT_BYTES ) throw new Error( "TEMIR salt must be 16 bytes" );
	const pass = new TextEncoder().encode( passphrase );
	const key = new Uint8Array( KEY_BYTES );
	for ( let i = 0; i < KEY_BYTES; i++ ) {
		const p = pass.length === 0 ? 0x5a : pass[i % pass.length]!;
		key[i] = p ^ rotl8( salt[i % SALT_BYTES]!, i % 7 ) ^ Math.imul( i, 0x9e ) & 0xff;
	}
	const nonce = salt.slice( 0, NONCE_BYTES );
	for ( let i = 0; i < pass.length; i++ ) {
		key[i % KEY_BYTES] = rotl8( (key[i % KEY_BYTES]! + pass[i]!) & 0xff, 3 );
		nonce[i % NONCE_BYTES]! ^= pass[i]!;
	}
	const block = new Uint32Array( BLOCK_WORDS );
	for ( let round = 0; round < KEY_DERIVATION_ROUNDS; round++ ) {
		chachaBlock( keyWordsOf( key ), nonce, round, block );
		for ( let i = 0; i < KEY_BYTES; i++ ) key[i]! ^= keystreamByte( block, i ) ^ keystreamByte( block, 32 + i );
		nonce[round % NONCE_BYTES]! ^= keystreamByte( block, round % BLOCK_BYTES );
	}
	return key;
}

/*
================
headerTag

The 16-byte authentication tag over the salt, the TOC location and the
TOC ciphertext digest (sropk.rs compute_header_auth_tag). A wrong key or a
changed TOC both fail it.
================
*/
function headerTag( header: TemirHeader, tocCipherHash: bigint ) {
	const input = new Uint8Array( BLOCK_BYTES ), view = new DataView( input.buffer );
	input.set( TEMIR_MAGIC, 0 );
	input.set( header.salt, 8 );
	const offset = BigInt( header.tocOffset ), length = BigInt( header.tocLength ), count = BigInt( header.entryCount );
	view.setBigUint64( 24, offset, true );
	view.setBigUint64( 32, length, true );
	view.setBigUint64( 40, count, true );
	view.setBigUint64( 48, tocCipherHash, true );
	view.setBigUint64( 56, offset ^ length ^ count, true );
	const keyWords = keyWordsOf( header.masterKey );
	chachaXorAt( keyWords, MAC_NONCE, 0, input );
	const stream = new Uint32Array( BLOCK_WORDS );
	chachaBlock( keyWords, MAC_NONCE, 1, stream );
	const tag = new Uint8Array( 16 );
	for ( let i = 0; i < 16; i++ ) {
		tag[i] = input[i]! ^ input[16 + i]! ^ input[32 + i]! ^ input[48 + i]! ^ keystreamByte( stream, i );
	}
	return tag;
}

/*
================
safeNumber

A u64 field as a Number. Archives are a few GiB, far below 2^53; anything
larger is a damaged or hostile header, never a real offset.
================
*/
function safeNumber( value: bigint, field: string ) {
	if ( value > BigInt( Number.MAX_SAFE_INTEGER ) ) throw formatError( `TEMIR ${field} out of range` );
	return Number( value );
}

// ============================================================================

/*
================
readTemirHeader

Opens the 64-byte header with the passphrase. fileBytes is the archive's
size, so a TOC pointing past the end is refused before anything reads it.
================
*/
export function readTemirHeader( bytes: Uint8Array, fileBytes: number, passphrase: string ): TemirHeader {
	if ( bytes.length < TEMIR_HEADER_BYTES || fileBytes < TEMIR_HEADER_BYTES ) {
		throw formatError( "file too small to be a TEMIR archive" );
	}
	for ( let i = 0; i < TEMIR_MAGIC.length; i++ ) {
		if ( bytes[i] !== TEMIR_MAGIC[i] ) throw formatError( "invalid TEMIR magic" );
	}
	const salt = bytes.slice( 8, 24 ), masterKey = deriveTemirKey( passphrase, salt );
	const meta = bytes.slice( 24, 48 ), view = new DataView( meta.buffer );
	chachaXorAt( keyWordsOf( masterKey ), HEADER_NONCE, 0, meta );
	const tocOffset = safeNumber( view.getBigUint64( 0, true ), "TOC offset" );
	const tocLength = safeNumber( view.getBigUint64( 8, true ), "TOC length" );
	const entryCount = safeNumber( view.getBigUint64( 16, true ), "entry count" );
	// A wrong key decrypts these into noise; refuse it here, as sropk.rs does,
	// before the caller reads a bogus TOC range.
	if ( tocOffset < TEMIR_HEADER_BYTES || tocLength > TEMIR_MAX_TOC_BYTES || tocOffset + tocLength > fileBytes ) {
		throw formatError( "invalid archive key or corrupted TEMIR header" );
	}
	return { salt, masterKey, tocOffset, tocLength, entryCount, tag: bytes.slice( 48, 64 ) };
}

/*
================
openTemirToc

Authenticates the TOC ciphertext against the header tag, decrypts it and
indexes its entries by normalized path. tocCipher is not modified. An entry
that points outside the file is refused: the tag proves the key, not that
the writer was sane.
================
*/
export function openTemirToc( header: TemirHeader, tocCipher: Uint8Array, fileBytes: number ): TemirArchive {
	if ( tocCipher.length !== header.tocLength ) throw formatError( "TEMIR TOC length mismatch" );
	const expected = headerTag( header, fnv1a64( tocCipher ) );
	for ( let i = 0; i < expected.length; i++ ) {
		if ( expected[i] !== header.tag[i] ) {
			throw formatError( "invalid archive key (authentication tag mismatch)" );
		}
	}
	const keyWords = keyWordsOf( header.masterKey );
	const toc = tocCipher.slice();
	chachaXorAt( keyWords, TOC_NONCE, 0, toc );
	const view = new DataView( toc.buffer ), decoder = new TextDecoder( "utf-8", { fatal: true } );
	const entries = new Map<string, TemirEntry>();
	let pos = 0;
	for ( let i = 0; i < header.entryCount; i++ ) {
		if ( pos + 2 > toc.length ) throw formatError( "truncated TEMIR TOC path length" );
		const pathBytes = view.getUint16( pos, true );
		pos += 2;
		if ( pos + pathBytes + TOC_ENTRY_TAIL_BYTES > toc.length ) {
			throw formatError( "truncated TEMIR TOC entry" );
		}
		let path: string;
		try {
			path = decoder.decode( toc.subarray( pos, pos + pathBytes ) );
		} catch {
			throw formatError( "invalid UTF-8 in TEMIR TOC" );
		}
		pos += pathBytes;
		const offset = safeNumber( view.getBigUint64( pos, true ), "entry offset" );
		const length = safeNumber( view.getBigUint64( pos + 8, true ), "entry length" );
		const nonce = toc.slice( pos + 16, pos + 16 + NONCE_BYTES );
		const checksum = view.getBigUint64( pos + 16 + NONCE_BYTES, true );
		pos += TOC_ENTRY_TAIL_BYTES;
		if ( offset < TEMIR_HEADER_BYTES || offset + length > fileBytes ) {
			throw formatError( `TEMIR entry outside the archive: ${path}` );
		}
		// A later entry for the same path wins, matching the writers' TOC order.
		entries.set( normalizeTemirPath( path ), { path, offset, length, nonce, checksum } );
	}
	return { keyWords, entries };
}

/*
================
normalizeTemirPath

The lookup key for a virtual or URL path: forward slashes, no leading slash,
ASCII lower case (sropk.rs normalize_virtual_key).
================
*/
export function normalizeTemirPath( path: string ) {
	return path.replaceAll( "\\", "/" ).replace( /^\/+/, "" ).toLowerCase();
}

/*
================
decryptTemirRange

Decrypts, in place, the bytes read from the file at entry.offset + start.
The caller reads the ciphertext; this keeps storage out of the format.
================
*/
export function decryptTemirRange( archive: TemirArchive, entry: TemirEntry, start: number, bytes: Uint8Array ) {
	if ( start < 0 || start + bytes.length > entry.length ) throw new RangeError( "TEMIR range outside its entry" );
	chachaXorAt( archive.keyWords, entry.nonce, start, bytes );
	return bytes;
}
