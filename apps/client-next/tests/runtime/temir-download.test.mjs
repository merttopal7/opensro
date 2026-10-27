/*
===========================================================================

temir-download.test.mjs - archive downloads survive a closed tab

foundation/assets/temir-download.ts writes an archive into OPFS through a
sync access handle and checkpoints its offset and running FNV-1a. These
tests run it against an in-memory handle and a server that honours Range
and If-Range, with the Go packer's fixture archive as the release (a real
header tag, a real checksum), and break the transfer in the ways a player's
connection does.

===========================================================================
*/
import "../helpers/native-source-loader.mjs";
import { pathToFileURL as sourceFileUrl } from "node:url";
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import { root } from "../../tools/project.mjs";
import { memoryHandle } from "../helpers/opfs-memory.mjs";

const download = await import(
	sourceFileUrl( path.join( root, "src/engine/foundation/assets/temir-download.ts" ) ).href
);
const temir = await import( sourceFileUrl( path.join( root, "src/engine/foundation/assets/temir.ts" ) ).href );

const ARCHIVE = new Uint8Array( readFileSync( path.join( root, "tests/fixtures/temir/client-reader.temir" ) ) );
const BASE_URL = "https://game.example/updates/";
// Small enough that the 1229-byte fixture crosses many checkpoints.
const CHECKPOINT = 100;
const CHUNK = 37;

/*
================
release

The fixture as patches.json would publish it.
================
*/
function release( bytes = ARCHIVE ) {
	return {
		name: "World.temir",
		size: bytes.length,
		checksum: temir.fnv1a64( bytes ),
		headerTag: Buffer.from( bytes.subarray( 48, 64 ) ).toString( "hex" ),
		url: "last_version/World.temir"
	};
}

/*
================
serverState

What the server holds and how it misbehaves. failAfter breaks the stream
after that many bytes; corrupt flips the byte at that offset in transit.
================
*/
/**
 * @param {Uint8Array} content
 * @param {string} etag
 * @param {{ failAfter?: number, corrupt?: number }} [faults]
 * @returns {{ content: Uint8Array, etag: string, failAfter: number | undefined, corrupt: number | undefined }}
 */
function serverState( content, etag, faults = {} ) {
	return { content, etag, failAfter: faults.failAfter, corrupt: faults.corrupt };
}

/*
================
server

Serves content with Range and If-Range (RFC 9110 14.2, 13.1.5): a range
is honoured only when If-Range is absent or names the current ETag. The
body arrives in CHUNK-byte pieces; failAfter makes the stream error after
that many bytes, corrupt flips one byte in transit.
================
*/
function server( state ) {
	const requests = [];
	const request = async ( url, init ) => {
		const headers = init.headers ?? {};
		requests.push( { url, range: headers.Range, ifRange: headers["If-Range"] } );
		const content = state.content;
		let start = 0, status = 200;
		const match = /^bytes=(\d+)-$/.exec( headers.Range ?? "" );
		if ( match && (!headers["If-Range"] || headers["If-Range"] === state.etag) ) {
			start = Number( match[1] );
			status = 206;
		}
		const body = content.slice( start );
		if ( state.corrupt !== undefined && state.corrupt >= start ) body[state.corrupt - start] ^= 0xff;
		let sent = 0;
		const stream = new ReadableStream( {
			pull( controller ) {
				if ( state.failAfter !== undefined && sent >= state.failAfter ) {
					controller.error( new Error( "connection reset" ) );
					return;
				}
				if ( sent >= body.length ) {
					controller.close();
					return;
				}
				const piece = body.slice( sent, sent + CHUNK );
				sent += piece.length;
				controller.enqueue( piece );
			}
		} );
		const responseHeaders = { ETag: state.etag };
		if ( status === 206 ) {
			responseHeaders["Content-Range"] = `bytes ${start}-${content.length - 1}/${content.length}`;
		}
		return new Response( stream, { status, headers: responseHeaders } );
	};
	return { request, requests };
}

/*
================
run

One download attempt; resolves to its progress events or rejects with its
error.
================
*/
async function run( job ) {
	const events = [];
	for await ( const event of download.downloadArchive( job ) ) events.push( event );
	return events;
}

/*
================
job
================
*/
function job( files, origin, overrides = {} ) {
	return {
		release: release(),
		baseUrl: BASE_URL,
		file: files.file,
		progress: files.progress,
		request: origin.request,
		signal: new AbortController().signal,
		checkpointBytes: CHECKPOINT,
		...overrides
	};
}

test("patches.json checksums above 2^53 are read exactly", () => {
	const manifest = download.parsePatchManifest( `{
		"version": 16,
		"patches": [
			{ "name": "Media.temir", "size": 1005703803, "checksum": 1424778519986605008,
				"headerTag": "A2F64BB5E8C4D513CAFBAAEC3D46E079", "url": "last_version/Media.temir" },
			{ "name": "Data.temir", "size": 1675275491, "checksum": 6465258095230198140, "url": "last_version/Data.temir" },
			{ "name": "World.temir", "size": 1573841246, "checksum": 6164874526896301665, "url": "last_version/World.temir" },
			{ "name": "Patch001.temir", "size": 49877949, "checksum": 11779723509040315787 }
		]
	}` );
	assert.equal( manifest.version, 16 );
	assert.equal( manifest.files[0].checksum, 1424778519986605008n );
	assert.equal( manifest.files[3].checksum, 11779723509040315787n );
	assert.equal( manifest.files[0].headerTag, "a2f64bb5e8c4d513cafbaaec3d46e079" );
	assert.equal( manifest.files[3].url, "Patch001.temir" );
	assert.deepEqual( download.baseArchiveReleases( manifest ).map( f => f.name ), [
		"Media.temir",
		"Data.temir",
		"World.temir"
	] );
	assert.throws( () => download.baseArchiveReleases( { version: 1, files: [] } ), /Media\.temir/ );
});

test("only a patch's live entries count: a later path or bundle family wins", () => {
	// checksum is spliced in as written: u64 text, not a rounded Number.
	const entry = ( path, checksum ) => `{ "path": "${path}", "size": 1, "checksum": ${checksum} }`;
	const manifest = download.parsePatchManifest( `{ "version": 2, "patches": [
		{ "name": "Media.temir", "size": 9, "checksum": 1 },
		{ "name": "Patch001.temir", "size": 9, "checksum": 2, "entries": [
			${entry( "assets/a.bin", 1 )}, ${entry( "assets/bootstrap-OLD.js", 2 )}, ${entry( "assets/b.bin", 3 )} ] },
		{ "name": "Patch002.temir", "size": 9, "checksum": 3, "entries": [
			${entry( "/Assets/A.bin", 4 )}, ${entry( "assets/bootstrap-NEW.js", "18446744073709551615" )} ] },
		{ "name": "Patch003.temir", "size": 9, "checksum": 4 }
	] }` );
	const live = download.livePatchEntries( manifest );
	// Patch003 lists no entries: there is nothing to overlay from it.
	assert.deepEqual( [ ...live.keys() ], [ "Patch001.temir", "Patch002.temir" ] );
	assert.deepEqual( live.get( "Patch001.temir" ).map( e => e.path ), [ "assets/b.bin" ] );
	assert.deepEqual( live.get( "Patch002.temir" ).map( e => e.path ), [ "/Assets/A.bin", "assets/bootstrap-NEW.js" ] );
	assert.equal( live.get( "Patch002.temir" )[1].checksum, 18446744073709551615n );
	assert.equal( download.isDifferentialPatch( "Patch002.temir" ), true );
	assert.equal( download.isDifferentialPatch( "Media.temir" ), false );
});

test("a fresh download writes the archive and records it as done", async () => {
	const files = { file: memoryHandle(), progress: memoryHandle() };
	const origin = server( serverState( ARCHIVE, '"v1"' ) );
	const events = await run( job( files, origin ) );
	assert.deepEqual( files.file.bytes(), ARCHIVE );
	assert.equal( origin.requests[0].url, BASE_URL + "last_version/World.temir" );
	assert.equal( origin.requests[0].range, undefined );
	assert.deepEqual( events.at( -1 ), { received: ARCHIVE.length, total: ARCHIVE.length } );
	assert.ok( events.every( ( e, i ) => i === 0 || e.received >= events[i - 1].received ) );
	assert.equal( download.archiveComplete( release(), files.file, files.progress ), true );
});

test("a broken connection resumes from the last checkpoint with Range and If-Range", async () => {
	const files = { file: memoryHandle(), progress: memoryHandle() };
	const state = serverState( ARCHIVE, '"v1"', { failAfter: 600 } );
	const origin = server( state );
	await assert.rejects( run( job( files, origin ) ), /connection reset/ );
	assert.equal( download.archiveComplete( release(), files.file, files.progress ), false );

	state.failAfter = undefined;
	await run( job( files, origin ) );
	const resumed = origin.requests[1];
	// Three 37-byte chunks pass the 100-byte checkpoint, so checkpoints fall
	// at 111, 222, ... and the last one before the break at 600 is 555.
	assert.equal( resumed.range, "bytes=555-" );
	assert.equal( resumed.ifRange, '"v1"' );
	assert.deepEqual( files.file.bytes(), ARCHIVE );
	assert.equal( download.archiveComplete( release(), files.file, files.progress ), true );
});

test("a file that changed on the server restarts from the first byte", async () => {
	const files = { file: memoryHandle(), progress: memoryHandle() };
	const state = serverState( ARCHIVE, '"v1"', { failAfter: 600 } );
	const origin = server( state );
	await assert.rejects( run( job( files, origin ) ) );

	// Same release in the manifest, but the server's copy was rewritten:
	// If-Range no longer matches, so the answer is the whole file (200).
	state.failAfter = undefined;
	state.etag = '"v2"';
	await run( job( files, origin ) );
	assert.ok( origin.requests[1].range, "the resume asked for a range" );
	assert.deepEqual( files.file.bytes(), ARCHIVE );
	assert.equal( download.archiveComplete( release(), files.file, files.progress ), true );
});

test("a new release in the manifest discards the old attempt without asking for a range", async () => {
	const files = { file: memoryHandle(), progress: memoryHandle() };
	const state = serverState( ARCHIVE, '"v1"', { failAfter: 600 } );
	const origin = server( state );
	await assert.rejects( run( job( files, origin ) ) );

	state.failAfter = undefined;
	const next = { ...release(), headerTag: "00".repeat( 16 ), checksum: 0n };
	await assert.rejects(
		run( job( files, origin, { release: next } ) ),
		error => download.isArchiveIntegrityError( error )
	);
	assert.equal( origin.requests[1].range, undefined );
});

test("a shorter new release leaves none of the older, longer file behind", async () => {
	const files = { file: memoryHandle(), progress: memoryHandle() };
	const older = new Uint8Array( ARCHIVE.length + 500 );
	older.set( ARCHIVE );
	older.fill( 0xab, ARCHIVE.length );
	await run( job( files, server( serverState( older, '"old"' ) ), { release: release( older ) } ) );
	assert.equal( files.file.getSize(), older.length );

	await run( job( files, server( serverState( ARCHIVE, '"new"' ) ) ) );
	assert.deepEqual( files.file.bytes(), ARCHIVE );
	assert.equal( download.archiveComplete( release(), files.file, files.progress ), true );
});

test("a byte corrupted in transit fails the checksum and the next attempt starts over", async () => {
	const files = { file: memoryHandle(), progress: memoryHandle() };
	const state = serverState( ARCHIVE, '"v1"', { corrupt: 900 } );
	const origin = server( state );
	await assert.rejects( run( job( files, origin ) ), error => download.isArchiveIntegrityError( error ) );
	assert.equal( files.file.getSize(), 0 );

	state.corrupt = undefined;
	await run( job( files, origin ) );
	assert.equal( origin.requests[1].range, undefined );
	assert.deepEqual( files.file.bytes(), ARCHIVE );
});

test("a wrong header tag is refused even when the checksum is not published", async () => {
	const files = { file: memoryHandle(), progress: memoryHandle() };
	const origin = server( serverState( ARCHIVE, '"v1"' ) );
	const wrong = { ...release(), checksum: 0n, headerTag: "ff".repeat( 16 ) };
	await assert.rejects( run( job( files, origin, { release: wrong } ) ), /header tag/ );
});

test("a finished download is not fetched again", async () => {
	const files = { file: memoryHandle(), progress: memoryHandle() };
	const origin = server( serverState( ARCHIVE, '"v1"' ) );
	await run( job( files, origin ) );
	const events = await run( job( files, origin ) );
	assert.equal( origin.requests.length, 1 );
	assert.deepEqual( events, [ { received: ARCHIVE.length, total: ARCHIVE.length } ] );
});

test("bytes past the last checkpoint are never trusted", async () => {
	const files = { file: memoryHandle(), progress: memoryHandle() };
	const state = serverState( ARCHIVE, '"v1"', { failAfter: 600 } );
	const origin = server( state );
	await assert.rejects( run( job( files, origin ) ) );
	// The last checkpoint is 555; two more chunks reached the file before
	// the break, so 555..629 was written but never confirmed.
	assert.equal( files.file.getSize(), 629 );

	state.failAfter = undefined;
	// Scribble over the unconfirmed tail, as a crash mid-write could leave it.
	files.file.write( new Uint8Array( 40 ).fill( 0xee ), { at: 589 } );
	await run( job( files, origin ) );
	assert.equal( origin.requests[1].range, "bytes=555-" );
	assert.deepEqual( files.file.bytes(), ARCHIVE );
});

test("a patch smaller than the checkpoint interval still yields chunk progress", async () => {
	const files = { file: memoryHandle(), progress: memoryHandle() };
	const origin = server( serverState( ARCHIVE, '"v1"' ) );
	const events = await run( job( files, origin, { checkpointBytes: 8 * 1024 * 1024 } ) );
	assert.ok( events.length > 2, "stream chunks yield progress before the 8 MiB checkpoint" );
	assert.equal( events[0].received, 0 );
	assert.equal( events[1].received, CHUNK );
	assert.deepEqual( events.at( -1 ), { received: ARCHIVE.length, total: ARCHIVE.length } );
});
