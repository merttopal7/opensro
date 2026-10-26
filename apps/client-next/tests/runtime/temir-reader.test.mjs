/*
===========================================================================

temir-reader.test.mjs - the browser reads the archives the Go packer writes

foundation/assets/temir.ts is checked against bytes it did not produce:
tests/fixtures/temir/client-reader.temir is written by the Go packer
(TestTemirClientReaderFixture) and kept identical by check:shared-fixtures.
The members' plaintext is repeated here; if either side's format drifts,
the archive stops opening or decrypts to different bytes.

===========================================================================
*/
import "../helpers/native-source-loader.mjs";
import { pathToFileURL as sourceFileUrl } from "node:url";
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import { root } from "../../tools/project.mjs";

const temir = await import(
	sourceFileUrl( path.join( root, "src/engine/foundation/assets/temir.ts" ) ).href
);

const FIXTURE = readFileSync( path.join( root, "tests/fixtures/temir/client-reader.temir" ) );
// Must match fixturePassphrase and fixtureSources in temir_fixture_test.go.
const PASSPHRASE = "opensro-temir-fixture";
const MANIFEST_TEXT = '{"format":"temir-fixture"}';
const PATTERN = Uint8Array.from( { length: 1000 }, ( _, i ) => (i * 31 + 7) & 0xff );

/*
================
hex
================
*/
function hex( bytes ) {
	return Buffer.from( bytes ).toString( "hex" );
}

/*
================
openFixture

Opens an archive the way the OPFS owner will: header bytes first, then the
TOC range the header names.
================
*/
function openFixture( bytes, passphrase = PASSPHRASE ) {
	const file = new Uint8Array( bytes );
	const header = temir.readTemirHeader( file.subarray( 0, temir.TEMIR_HEADER_BYTES ), file.length, passphrase );
	const toc = file.subarray( header.tocOffset, header.tocOffset + header.tocLength );
	return { file, archive: temir.openTemirToc( header, toc, file.length ) };
}

/*
================
readMember

Reads and decrypts length bytes of a member from start, as a range read.
================
*/
function readMember( opened, key, start, length ) {
	const entry = opened.archive.entries.get( key );
	assert.ok( entry, `entry ${key}` );
	const bytes = opened.file.slice( entry.offset + start, entry.offset + start + length );
	return temir.decryptTemirRange( opened.archive, entry, start, bytes );
}

test("the keystream matches the RFC 8439 block function test vector", () => {
	// RFC 8439 2.3.2: key 00..1f, nonce 00:00:00:09:00:00:00:4a:00:00:00:00,
	// block counter 1. Keystream for block 1 starts at stream byte 64.
	const key = temir.keyWordsOf( Uint8Array.from( { length: 32 }, ( _, i ) => i ) );
	const nonce = Uint8Array.from( [ 0, 0, 0, 9, 0, 0, 0, 0x4a, 0, 0, 0, 0 ] );
	const stream = new Uint8Array( 64 );
	temir.chachaXorAt( key, nonce, 64, stream );
	assert.equal(
		hex( stream ),
		"10f1e7e4d13b5915500fdd1fa32071c4c7d1f4c733c068030422aa9ac3d46c4e" +
			"d2826446079faa0914c2d705d98b02a2b5129cd1de164eb9cbd083e8a2503c4e"
	);
});

test("a range that starts mid-block decrypts like the whole stream", () => {
	const key = temir.keyWordsOf( Uint8Array.from( { length: 32 }, ( _, i ) => 255 - i ) );
	const nonce = Uint8Array.from( { length: 12 }, ( _, i ) => i * 7 );
	const whole = new Uint8Array( 300 );
	temir.chachaXorAt( key, nonce, 0, whole );
	const part = new Uint8Array( 150 );
	temir.chachaXorAt( key, nonce, 77, part );
	assert.deepEqual( part, whole.subarray( 77, 227 ) );
});

test("fnv1a64 matches the published FNV-1a 64 values", () => {
	assert.equal( temir.fnv1a64( new Uint8Array( 0 ) ), 0xcbf29ce484222325n );
	assert.equal( temir.fnv1a64( new TextEncoder().encode( "a" ) ), 0xaf63dc4c8601ec8cn );
	assert.equal( temir.fnv1a64( new TextEncoder().encode( "foobar" ) ), 0x85944171f73967e8n );
});

test("the Go packer's archive opens and every member decrypts to its plaintext", () => {
	const opened = openFixture( FIXTURE );
	assert.deepEqual( [ ...opened.archive.entries.keys() ], [
		"assets/packs/manifest.json",
		"assets/packs/map-001-000000000000.bin"
	] );
	const manifest = readMember( opened, "assets/packs/manifest.json", 0, MANIFEST_TEXT.length );
	assert.equal( new TextDecoder().decode( manifest ), MANIFEST_TEXT );
	const pattern = readMember( opened, "assets/packs/map-001-000000000000.bin", 0, PATTERN.length );
	assert.deepEqual( pattern, PATTERN );
	for ( const [key, entry] of opened.archive.entries ) {
		assert.equal( temir.fnv1a64( readMember( opened, key, 0, entry.length ) ), entry.checksum, key );
	}
});

test("a range read from the middle of a member returns those bytes", () => {
	const opened = openFixture( FIXTURE );
	const part = readMember( opened, "assets/packs/map-001-000000000000.bin", 100, 333 );
	assert.deepEqual( part, PATTERN.subarray( 100, 433 ) );
});

test("lookups normalize URL paths the way the desktop shell does", () => {
	const opened = openFixture( FIXTURE );
	const key = temir.normalizeTemirPath( "/Assets\\Packs/MANIFEST.json" );
	assert.ok( opened.archive.entries.has( key ) );
});

test("a wrong passphrase is refused as a format error", () => {
	assert.throws( () => openFixture( FIXTURE, "not-the-key" ), error => temir.isTemirFormatError( error ) );
});

test("a changed TOC byte fails the header tag", () => {
	const damaged = new Uint8Array( FIXTURE );
	const header = temir.readTemirHeader( damaged.subarray( 0, temir.TEMIR_HEADER_BYTES ), damaged.length, PASSPHRASE );
	damaged[header.tocOffset + 5] ^= 0xff;
	assert.throws( () => openFixture( damaged ), /authentication tag mismatch/ );
});

test("a decrypt range past the member's end is refused", () => {
	const opened = openFixture( FIXTURE );
	const entry = opened.archive.entries.get( "assets/packs/manifest.json" );
	assert.throws(
		() => temir.decryptTemirRange( opened.archive, entry, 1, new Uint8Array( entry.length ) ),
		RangeError
	);
});
