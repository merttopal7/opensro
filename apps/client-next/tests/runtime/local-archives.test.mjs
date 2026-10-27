/*
===========================================================================

local-archives.test.mjs - the installed PWA reads its game files from OPFS

runtime/assets/worker/archives/archives.ts downloads the archives the
server's patches.json names, opens them, overlays differential patches and
answers the pack reader's reads. Here the server publishes the Go packer's
fixtures (a base archive and a patch over it, temir_fixture_test.go), OPFS
is in memory (helpers/opfs-memory.mjs), and every read is checked against
the fixtures' known plaintext.

===========================================================================
*/
import "../helpers/native-source-loader.mjs";
import { pathToFileURL as sourceFileUrl } from "node:url";
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import { root } from "../../tools/project.mjs";
import { memoryStorage } from "../helpers/opfs-memory.mjs";

const { createLocalArchives, readerCount } = await import(
	sourceFileUrl( path.join( root, "src/engine/runtime/assets/worker/archives/archives.ts" ) ).href
);
const { createArchiveReader } = await import(
	sourceFileUrl( path.join( root, "src/engine/runtime/assets/worker/archives/reader.ts" ) ).href
);
const temir = await import( sourceFileUrl( path.join( root, "src/engine/foundation/assets/temir.ts" ) ).href );

const fixture = name => new Uint8Array( readFileSync( path.join( root, "tests/fixtures/temir", name ) ) );
const BASE = fixture( "client-reader.temir" );
const PATCH = fixture( "client-reader-patch.temir" );
// The fixtures' passphrase and plaintext (temir_fixture_test.go).
const PASSPHRASE = "opensro-temir-fixture";
const MANIFEST_TEXT = '{"format":"temir-fixture"}';
const PATCHED_MANIFEST_TEXT = '{"format":"temir-fixture","patch":1}';
const PATTERN = Uint8Array.from( { length: 1000 }, ( _, i ) => (i * 31 + 7) & 0xff );
const NEW_FILE = Uint8Array.from( { length: 300 }, ( _, i ) => (i * 13 + 1) & 0xff );
const ORIGIN = "https://game.example";
const NAMES = [ "Media.temir", "Data.temir", "World.temir" ];
const MANIFEST_URL = `${ORIGIN}/assets/packs/manifest.json`;
const MAP_URL = `${ORIGIN}/assets/packs/map-001-000000000000.bin`;
const NEW_URL = `${ORIGIN}/assets/packs/new-001-000000000000.bin`;
const encoder = new TextEncoder();

/*
================
row

One patches.json row for an archive's bytes, the checksum unquoted as the
Agent writes it.
================
*/
function row( name, bytes, url, entries ) {
	const tag = Buffer.from( bytes.subarray( 48, 64 ) ).toString( "hex" );
	const list = entries ?
		`, "entries": [ ${
			entries.map( e =>
				`{ "path": "${e.path}", "size": ${e.data.length}, "checksum": ${temir.fnv1a64( e.data )} }`
			)
				.join( ", " )
		} ]` :
		"";
	return `{ "name": "${name}", "size": ${bytes.length}, "checksum": ${temir.fnv1a64( bytes )}, ` +
		`"headerTag": "${tag}", "url": "${url}"${list} }`;
}

/*
================
release

What the server publishes: the base archives' bytes (one for all three,
or per name) and optionally Patch001.temir, whose bytes default to the
patch fixture, with the entries it lists.
================
*/
function release( base, patchEntries, patchBytes = PATCH ) {
	const bytesOf = name => base instanceof Uint8Array ? base : base[name];
	const rows = NAMES.map( name => row( name, bytesOf( name ), `last_version/${name}` ) );
	const files = new Map( NAMES.map( name => [ `/updates/last_version/${name}`, bytesOf( name ) ] ) );
	if ( patchEntries ) {
		rows.push( row( "Patch001.temir", patchBytes, "Patch001.temir", patchEntries ) );
		files.set( "/updates/Patch001.temir", patchBytes );
	}
	return { manifest: `{ "version": 1, "patches": [ ${rows.join( ", " )} ] }`, files };
}

// Patch001's entries as patches.json lists them (the fixture's members).
const PATCH_ENTRIES = [
	{ path: "assets/packs/manifest.json", data: encoder.encode( PATCHED_MANIFEST_TEXT ) },
	{ path: "assets/packs/new-001-000000000000.bin", data: NEW_FILE }
];

/*
================
updatesServer

Serves a release's /updates/patches.json and files; requests records every
path asked for. A release whose manifest is null answers patches.json with
503.
================
*/
function updatesServer( published ) {
	const requests = [];
	/** @param {string} url */
	const request = async url => {
		const pathname = new URL( url ).pathname;
		requests.push( pathname );
		if ( pathname === "/updates/patches.json" ) {
			// A null manifest is an Agent that cannot answer (restarting, down).
			return published.manifest === null ?
				new Response( "", { status: 503 } ) :
				new Response( published.manifest, { status: 200 } );
		}
		const bytes = published.files.get( pathname );
		if ( bytes ) return new Response( bytes.slice(), { status: 200, headers: { ETag: '"v1"' } } );
		return new Response( "", { status: 404 } );
	};
	return { request, requests };
}

/*
================
countingWait

The loader's cancellable wait without a timer; waits counts the calls.
================
*/
function countingWait() {
	const wait = async () => {
		wait.waits++;
	};
	wait.waits = 0;
	return wait;
}

/*
================
prepared

Runs prepare() to the end and returns the archives with its progress.
readers is { count, spawn } for reader workers; none by default.
================
*/
/**
 * @param {any} storage
 * @param {{ request: (url: string) => Promise<Response>, requests: string[] }} server
 * @param {() => Promise<void>} [wait]
 * @param {{ count: number, spawn?: () => any }} [readers]
 */
async function prepared( storage, server, wait = countingWait(), readers = { count: 0 } ) {
	const io = { request: server.request, storage, wait, readers: readers.count, spawnReader: readers.spawn };
	const archives = createLocalArchives( io, ORIGIN, PASSPHRASE );
	const steps = [];
	for await ( const step of archives.prepare( new AbortController().signal ) ) steps.push( step );
	return { archives, steps };
}

/*
================
text
================
*/
async function text( archives, url ) {
	return new TextDecoder().decode( await archives.read( url, 1 << 20 ) );
}

test("a first start downloads all three archives, then reads come from them", async () => {
	const storage = memoryStorage(), server = updatesServer( release( BASE ) );
	const { archives, steps } = await prepared( storage, server );
	assert.deepEqual( server.requests, [
		"/updates/patches.json",
		"/updates/last_version/Media.temir",
		"/updates/last_version/Data.temir",
		"/updates/last_version/World.temir"
	] );
	for ( const name of NAMES ) {
		assert.deepEqual( steps.filter( s => s.name === name ).at( -1 ), {
			name,
			received: BASE.length,
			total: BASE.length
		} );
		assert.deepEqual( storage.files.get( `temir/${name}` )?.bytes, BASE );
	}
	assert.deepEqual( archives.progress(), {
		received: BASE.length * NAMES.length,
		total: BASE.length * NAMES.length
	} );
	assert.equal( await text( archives, MANIFEST_URL ), MANIFEST_TEXT );
	const block = await archives.read( MAP_URL, 1 << 20, { start: 100, end: 432, total: PATTERN.length } );
	assert.deepEqual( block, PATTERN.subarray( 100, 433 ) );
	archives.dispose();
});

test("a later start finds the archives on disk and downloads nothing", async () => {
	const storage = memoryStorage();
	(await prepared( storage, updatesServer( release( BASE ) ) )).archives.dispose();
	const again = updatesServer( release( BASE ) );
	const { archives } = await prepared( storage, again );
	assert.deepEqual( again.requests, [ "/updates/patches.json" ] );
	assert.deepEqual( await archives.read( MAP_URL, 1 << 20 ), PATTERN );
	archives.dispose();
});

test("an update keeps the finished bases and downloads only the patch", async () => {
	const storage = memoryStorage();
	(await prepared( storage, updatesServer( release( BASE ) ) )).archives.dispose();

	// The server merged Patch001 into new last_version bases (here PATCH's
	// bytes stand in for them: another header tag, another size) and
	// publishes the patch beside them.
	const update = release( PATCH, PATCH_ENTRIES );
	const server = updatesServer( update );
	const { archives } = await prepared( storage, server );
	assert.deepEqual( server.requests, [ "/updates/patches.json", "/updates/Patch001.temir" ] );
	assert.equal( await text( archives, MANIFEST_URL ), PATCHED_MANIFEST_TEXT );
	assert.deepEqual( await archives.read( NEW_URL, 1 << 20 ), NEW_FILE );
	// Files the patch does not carry still come from the kept base.
	assert.deepEqual( await archives.read( MAP_URL, 1 << 20 ), PATTERN );
	archives.dispose();

	// The next start finds the patch on disk as well.
	const later = updatesServer( update );
	(await prepared( storage, later )).archives.dispose();
	assert.deepEqual( later.requests, [ "/updates/patches.json" ] );
});

test("patches that cost more than the behind base download the base instead", async () => {
	const storage = memoryStorage();
	(await prepared( storage, updatesServer( release( BASE ) ) )).archives.dispose();

	// Only Media moved on (to PATCH's 539 bytes, which carry the patched
	// manifest); the patch the update lists is larger than that (BASE's
	// 1229 bytes stand in for it), so catching Media up costs less.
	const update = release( { "Media.temir": PATCH, "Data.temir": BASE, "World.temir": BASE }, PATCH_ENTRIES, BASE );
	const server = updatesServer( update );
	const { archives } = await prepared( storage, server );
	assert.deepEqual( server.requests, [ "/updates/patches.json", "/updates/last_version/Media.temir" ] );
	assert.equal( await text( archives, MANIFEST_URL ), PATCHED_MANIFEST_TEXT );
	assert.deepEqual( await archives.read( NEW_URL, 1 << 20 ), NEW_FILE );
	assert.ok( !storage.files.has( "temir/Patch001.temir" ) );
	archives.dispose();
});

test("a fresh install from last_version skips a patch the bases already hold", async () => {
	// last_version already has Patch001 merged in: its entries match the
	// bases' tables of contents exactly.
	const merged = [ { path: "assets/packs/manifest.json", data: encoder.encode( MANIFEST_TEXT ) } ];
	const server = updatesServer( release( BASE, merged ) );
	const { archives } = await prepared( memoryStorage(), server );
	assert.ok( !server.requests.includes( "/updates/Patch001.temir" ) );
	assert.equal( await text( archives, MANIFEST_URL ), MANIFEST_TEXT );
	archives.dispose();
});

test("once the bases hold a patch, its file is removed", async () => {
	const storage = memoryStorage();
	(await prepared( storage, updatesServer( release( BASE ) ) )).archives.dispose();
	(await prepared( storage, updatesServer( release( PATCH, PATCH_ENTRIES ) ) )).archives.dispose();
	assert.ok( storage.files.has( "temir/Patch001.temir" ) );

	// A later manifest lists Patch001 with entries the bases already hold
	// (a base re-downloaded after the server merged it).
	const merged = [ { path: "assets/packs/manifest.json", data: encoder.encode( MANIFEST_TEXT ) } ];
	(await prepared( storage, updatesServer( release( BASE, merged ) ) )).archives.dispose();
	assert.ok( !storage.files.has( "temir/Patch001.temir" ) );
	assert.ok( !storage.files.has( "temir/Patch001.temir.progress" ) );
});

test("without differential patches, a changed base is downloaded again", async () => {
	const storage = memoryStorage();
	(await prepared( storage, updatesServer( release( BASE ) ) )).archives.dispose();
	const server = updatesServer( release( PATCH ) );
	const { archives } = await prepared( storage, server );
	assert.deepEqual( server.requests.filter( p => p.startsWith( "/updates/last_version/" ) ).length, 3 );
	assert.equal( await text( archives, MANIFEST_URL ), PATCHED_MANIFEST_TEXT );
	archives.dispose();
});

test("a path no archive holds is a 404, as the network would answer", async () => {
	const { archives } = await prepared( memoryStorage(), updatesServer( release( BASE ) ) );
	await assert.rejects(
		archives.read( `${ORIGIN}/assets/packs/absent.bin`, 1 << 20 ),
		error => Object( error ).status === 404
	);
	await assert.rejects( archives.read( MAP_URL, 10 ), error => Object( error ).code === "ASSET_BYTE_LIMIT" );
	await assert.rejects( archives.read( MAP_URL, 1 << 20, { start: 0, end: 9, total: 99 } ), /range/ );
	archives.dispose();
});

test("reads wait for prepare", async () => {
	const archives = createLocalArchives(
		{
			request: updatesServer( release( BASE ) ).request,
			storage: memoryStorage(),
			wait: countingWait(),
			readers: 0
		},
		ORIGIN,
		PASSPHRASE
	);
	await assert.rejects( archives.read( MANIFEST_URL, 1 << 20 ), /not open/ );
});

test("a reloaded page waits for the old page's worker to let go of the files", async () => {
	// Edge 155: the first opens after a reload fail while the old worker
	// still holds its handles.
	const storage = memoryStorage( { lockedOpens: 3 } ), wait = countingWait();
	const { archives } = await prepared( storage, updatesServer( release( BASE ) ), wait );
	assert.equal( wait.waits, 3 );
	assert.equal( await text( archives, MANIFEST_URL ), MANIFEST_TEXT );
	archives.dispose();
});

test("a relaunched iOS PWA waits for WebKit's lock the same way", async () => {
	// iOS Safari refuses an open on a file another worker holds with
	// InvalidStateError, not Chromium's NoModificationAllowedError.
	const storage = memoryStorage( { lockedOpens: 3, lockError: "InvalidStateError" } ), wait = countingWait();
	const { archives } = await prepared( storage, updatesServer( release( BASE ) ), wait );
	assert.equal( wait.waits, 3 );
	assert.equal( await text( archives, MANIFEST_URL ), MANIFEST_TEXT );
	archives.dispose();
});

test("a file that stays taken fails after the bounded wait", async () => {
	const storage = memoryStorage( { lockedOpens: Infinity } ), wait = countingWait();
	await assert.rejects( prepared( storage, updatesServer( release( BASE ) ), wait ), {
		name: "NoModificationAllowedError"
	} );
	// LOCK_WAIT_MS / LOCK_RETRY_MS
	assert.equal( wait.waits, 60 );
});

test("a made-up storage estimate does not stop the download", async () => {
	// Brave (2026-10-26) reported a quota too small for the archives on a
	// disk with tens of GB free; the writes themselves succeed.
	const storage = memoryStorage( { reported: 1 } );
	const { archives } = await prepared( storage, updatesServer( release( BASE ) ) );
	assert.equal( await text( archives, MANIFEST_URL ), MANIFEST_TEXT );
	archives.dispose();
});

test("a full origin says how much is missing, and the download goes on once there is room", async () => {
	// Room for Media and part of Data: the write that would pass the quota
	// is refused as a full origin refuses it.
	const storage = memoryStorage( { quota: Math.floor( BASE.length * 1.5 ) } );
	await assert.rejects(
		prepared( storage, updatesServer( release( BASE ) ) ),
		/Not enough storage for the game files: [\d.]+ GB more are needed/
	);
	assert.equal( storage.files.get( "temir/Media.temir" )?.bytes.length, BASE.length );

	storage.quota = 1 << 30;
	const server = updatesServer( release( BASE ) );
	const { archives } = await prepared( storage, server );
	// Media was whole already; only the rest is fetched.
	assert.ok( !server.requests.includes( "/updates/last_version/Media.temir" ) );
	assert.deepEqual( await archives.read( MAP_URL, 1 << 20 ), PATTERN );
	archives.dispose();
});

test("when /updates/ is out of reach, whole archives still open", async () => {
	// Installed, then updated through Patch001: the bases and the patch are
	// on disk, and the last manifest is saved.
	const storage = memoryStorage();
	(await prepared( storage, updatesServer( release( BASE ) ) )).archives.dispose();
	(await prepared( storage, updatesServer( release( PATCH, PATCH_ENTRIES ) ) )).archives.dispose();
	const down = updatesServer( { manifest: null, files: new Map() } );
	const { archives } = await prepared( storage, down );
	assert.deepEqual( down.requests, [ "/updates/patches.json" ] );
	// The saved manifest still knows the patch: its overlay is back.
	assert.equal( await text( archives, MANIFEST_URL ), PATCHED_MANIFEST_TEXT );
	archives.dispose();
});

test("when /updates/ is out of reach and files are missing, its error stands", async () => {
	await assert.rejects(
		prepared( memoryStorage(), updatesServer( { manifest: null, files: new Map() } ) ),
		/HTTP 503/
	);
	const storage = memoryStorage();
	(await prepared( storage, updatesServer( release( BASE ) ) )).archives.dispose();
	storage.files.delete( "temir/Data.temir" );
	await assert.rejects( prepared( storage, updatesServer( { manifest: null, files: new Map() } ) ), /HTTP 503/ );
});

/*
================
inProcessReaders

Reader workers that run the real reader (reader.ts) in this process over
storage: each port delivers messages on a later microtask, as a worker's
would. A reader made with mute drops reads, so they stay owed; one made with
crash raises a worker error on its first read instead. ports records every
spawned port.
================
*/
function inProcessReaders( storage, count, { mute = false, crash = false } = {} ) {
	const ports = [];
	const spawn = () => {
		const port = {
			/** @type {((event: { data: any }) => void) | null} */
			onmessage: null,
			/** @type {((event: { message: string }) => void) | null} */
			onerror: null,
			terminated: false,
			reads: 0,
			postMessage( request ) {
				if ( request.kind === "read" ) {
					port.reads++;
					if ( mute ) return;
					if ( crash ) {
						queueMicrotask( () => port.onerror?.( { message: "Out of memory" } ) );
						return;
					}
				}
				queueMicrotask( () => reader.receive( request ) );
			},
			terminate() {
				port.terminated = true;
			}
		};
		const reader = createArchiveReader( reply => queueMicrotask( () => port.onmessage?.( { data: reply } ) ) );
		ports.push( port );
		return port;
	};
	// A reader worker opens its files through its own navigator.storage.
	Object.defineProperty( globalThis.navigator, "storage", { value: storage, configurable: true } );
	return { count, spawn, ports };
}

test("reader counts leave two cores and run four at most", () => {
	assert.equal( readerCount( 1 ), 0 );
	assert.equal( readerCount( 3 ), 0 );
	assert.equal( readerCount( 4 ), 2 );
	assert.equal( readerCount( 6 ), 4 );
	assert.equal( readerCount( 32 ), 4 );
});

test("reader workers return the same bytes and share the reads", async () => {
	const storage = memoryStorage(), readers = inProcessReaders( storage, 3 );
	const { archives } = await prepared( storage, updatesServer( release( BASE ) ), countingWait(), readers );
	assert.equal( readers.ports.length, 3 );
	const reads = await Promise.all( [
		text( archives, MANIFEST_URL ),
		archives.read( MAP_URL, 1 << 20 ),
		archives.read( MAP_URL, 1 << 20, { start: 100, end: 432, total: PATTERN.length } )
	] );
	assert.equal( reads[0], MANIFEST_TEXT );
	assert.deepEqual( reads[1], PATTERN );
	assert.deepEqual( reads[2], PATTERN.subarray( 100, 433 ) );
	// Three reads at once go to the three least busy readers.
	assert.deepEqual( readers.ports.map( p => p.reads ), [ 1, 1, 1 ] );
	archives.dispose();
	assert.ok( readers.ports.every( p => p.terminated ) );
});

test("a browser without read-only handles reads on the asset worker alone", async () => {
	const storage = memoryStorage( { readOnly: false } ), readers = inProcessReaders( storage, 3 );
	const { archives } = await prepared( storage, updatesServer( release( BASE ) ), countingWait(), readers );
	assert.ok( readers.ports.every( p => p.terminated ) );
	assert.equal( await text( archives, MANIFEST_URL ), MANIFEST_TEXT );
	assert.deepEqual( await archives.read( MAP_URL, 1 << 20 ), PATTERN );
	assert.ok( readers.ports.every( p => p.reads === 0 ) );
	archives.dispose();
});

test("closing the archives fails the reads the readers still owe", async () => {
	const storage = memoryStorage(), readers = inProcessReaders( storage, 2, { mute: true } );
	const { archives } = await prepared( storage, updatesServer( release( BASE ) ), countingWait(), readers );
	const owed = archives.read( MAP_URL, 1 << 20 );
	archives.dispose();
	await assert.rejects( owed, /Game archives closed/ );
});

test("a reader worker that fails answers the reads it owes with the reason", async () => {
	// Unanswered, the read would wait for the load's watchdog and the HUD
	// would report only "resource unavailable" (iPhone 13, 2026-10-27).
	const storage = memoryStorage(), readers = inProcessReaders( storage, 2, { crash: true } );
	const { archives } = await prepared( storage, updatesServer( release( BASE ) ), countingWait(), readers );
	await assert.rejects( archives.read( MAP_URL, 1 << 20 ), /Archive reader failed: Out of memory/ );
	archives.dispose();
});

test("a read that throws in the reader is answered as failed", async () => {
	const replies = [];
	const reader = createArchiveReader( reply => replies.push( reply ) );
	const file = {
		createSyncAccessHandle: async () => ({
			read() {
				throw new RangeError( "Out of memory" );
			},
			close() {}
		})
	};
	const dir = { getFileHandle: async () => file };
	Object.defineProperty( globalThis.navigator, "storage", {
		value: { getDirectory: async () => ({ getDirectoryHandle: async () => dir }) },
		configurable: true
	} );
	reader.receive( { kind: "open", directory: "temir", files: [ "Data.temir" ] } );
	await new Promise( resolve => setTimeout( resolve, 0 ) );
	assert.deepEqual( replies, [ { kind: "opened" } ] );
	reader.receive( {
		kind: "read",
		id: 7,
		file: "Data.temir",
		at: 0,
		length: 16,
		key: new Uint32Array( 8 ),
		nonce: new Uint8Array( 12 ),
		start: 0
	} );
	assert.deepEqual( replies[1], { kind: "read-failed", id: 7, error: "RangeError: Out of memory" } );
});
