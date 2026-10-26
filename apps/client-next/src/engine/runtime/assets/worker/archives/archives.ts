/*
===========================================================================

archives.ts - the installed PWA's .temir archives in OPFS

Stage A3b of docs/PWA-OPFS-MIGRATION.md. In the "opfs" delivery
(foundation/assets/archive-delivery.ts) the asset worker reads every game
file from the three .temir archives the desktop shell reads, kept in the
Origin Private File System. This module owns them: prepare() downloads or
resumes each archive the server's patches.json names (temir-download.ts),
after checking the origin has room, and opens them; read() then answers
the pack reader's downloads (packs/packs.ts PackDownload) from them, the
way the shell's loopback edge answers the same URLs (new-silkroad lib.rs
serve_local_file).

The loader sends nothing else until prepare() settles (assets.ts holds
every request while the archives are prepared), so a multi-gigabyte first
download never runs inside a load and its watchdog. Sync access handles
exist only in dedicated workers; this worker holds each archive's handle
open for its lifetime.

===========================================================================
*/

import {
	decryptTemirRange,
	normalizeTemirPath,
	openTemirToc,
	readTemirHeader,
	TEMIR_HEADER_BYTES,
	type TemirArchive,
	type TemirEntry
} from "@/engine/foundation/assets/temir";
import {
	archiveComplete,
	baseArchiveReleases,
	completeAsRecorded,
	downloadArchive,
	livePatchEntries,
	parsePatchManifest,
	type PatchEntry,
	type ReleaseFile
} from "@/engine/foundation/assets/temir-download";
import { updatesBaseUrl } from "@/engine/foundation/assets/archive-delivery";
import { gunzipBytes } from "@/engine/foundation/assets/read-bytes";
import type { ReaderReply, ReaderRequest } from "./internal/reader-contract";

// The packers' passphrase (apps/server/internal/agent/updates/temir.go
// DefaultArchiveKey, new-silkroad sropk.rs). It is in the public sources;
// the archives' encryption is obfuscation, not secrecy (migration doc 8).
const ARCHIVE_KEY = "OpenSRO-v150-Legend3-TEMIR-Key-2026!#99";
const DIRECTORY = "temir";
const PROGRESS_SUFFIX = ".progress";
const MANIFEST_NAME = "patches.json";
const MANIFEST_LIMIT = 16 << 20;
// Space asked for beyond the missing bytes: room for patches (A6) and for
// what else the origin stores.
const FREE_SPACE_MARGIN = 1.1;
const GIB = 1 << 30;
// A reloaded page's worker can start before the old page's worker has
// released its access handles (measured in Edge 155: the first open after a
// reload fails with NoModificationAllowedError). Opening waits this long, in
// LOCK_RETRY_MS steps, before it calls the file taken.
const LOCK_WAIT_MS = 15000;
const LOCK_RETRY_MS = 250;
const MAX_READERS = 4;

/*
================
ArchiveIo

What prepare() needs from the worker: its fetch, its storage manager and a
cancellable wait (the worker's timers belong to the loader). readers is
how many reader workers read and decrypt in parallel once the archives are
whole (readerCount; 0 reads on this worker); spawnReader stands in for the
real worker in tests.
================
*/
export interface ArchiveIo {
	request: ( url: string, init: RequestInit ) => Promise<Response>;
	storage: StorageManager;
	wait: ( ms: number, signal: AbortSignal ) => Promise<void>;
	readers: number;
	spawnReader?: () => Worker;
}

/*
================
readerCount

How many reader workers a device gets: four, the count A1 measured at
~290 MB/s against the desktop shell's 146-174 MB/s, but never more than
leaves two cores for the page and the asset worker. Below two, parallel
reading cannot pay for its messages and the asset worker reads alone.
================
*/
export function readerCount( cores: number ) {
	const count = Math.min( MAX_READERS, cores - 2 );
	return count >= 2 ? count : 0;
}

/*
================
spawnReader

A reader worker (reader-entry.ts). Its messages are ReaderRequest and
ReaderReply (internal/reader-contract.ts).
================
*/
function spawnReader(): Worker {
	return new Worker( new URL( "./reader-entry.ts", import.meta.url ), {
		type: "module",
		name: "sro-archive-reader"
	} );
}

/*
================
PrepareProgress

One archive's download progress, yielded by prepare().
================
*/
export interface PrepareProgress {
	name: string;
	received: number;
	total: number;
}

/*
================
ArchiveRange

The inclusive byte range of a member a pack read asks for, and the
member's whole length (the pack reader's PackRange, which is internal to
packs/).
================
*/
export interface ArchiveRange {
	start: number;
	end: number;
	total: number;
}

/*
================
OpenedRelease

A release's archive file and progress record, both open read-write.
================
*/
interface OpenedRelease {
	release: ReleaseFile;
	file: FileSystemSyncAccessHandle;
	progress: FileSystemSyncAccessHandle;
}

/*
================
Mounted
================
*/
interface Mounted {
	name: string;
	// This worker's own handle; closed while reader workers hold the file.
	handle: FileSystemSyncAccessHandle;
	archive: TemirArchive;
}

/*
================
Reader

A running reader worker and how many of its reads are outstanding.
================
*/
interface Reader {
	port: Worker;
	pending: number;
}

/*
================
statusError

A miss shaped like the network's: packs.ts tells a container the release
does not have (404) from other failures by status, the same for both.
================
*/
function statusError( message: string, status: number ) {
	return Object.assign( new Error( message ), { status } );
}

/*
================
readExact
================
*/
function readExact( handle: FileSystemSyncAccessHandle, offset: number, length: number ) {
	const bytes = new Uint8Array( new ArrayBuffer( length ) );
	const got = handle.read( bytes, { at: offset } );
	if ( got !== length ) throw new Error( `Archive read ended at ${got} of ${length} bytes` );
	return bytes;
}

/*
================
mount

Opens a verified archive: header and table of contents, authenticated by
the key (temir.ts).
================
*/
function mount( name: string, handle: FileSystemSyncAccessHandle, passphrase: string ): Mounted {
	const size = handle.getSize();
	const header = readTemirHeader( readExact( handle, 0, TEMIR_HEADER_BYTES ), size, passphrase );
	const archive = openTemirToc( header, readExact( handle, header.tocOffset, header.tocLength ), size );
	return { name, handle, archive };
}

/*
================
createLocalArchives

The owner of the archives. origin is the page's, against which the
updates URL resolves; passphrase is the packers' (tests pass the fixture's).
================
*/
export function createLocalArchives( io: ArchiveIo, origin: string, passphrase = ARCHIVE_KEY ) {
	// The base archives, and each live patch entry by path (its patch's
	// mount and the entry in that patch's table of contents).
	const mounted: Mounted[] = [];
	const overlay = new Map<string, { mount: Mounted; entry: TemirEntry; }>();
	const handles: FileSystemSyncAccessHandle[] = [];
	// The reader workers once started (startReaders), and every read they
	// owe an answer, by id.
	const readers: Reader[] = [];
	const owed = new Map<number, {
		reader: Reader;
		resolve: ( bytes: Uint8Array<ArrayBuffer> ) => void;
		reject: ( error: Error ) => void;
	}>();
	let ready = false, disposed = false, nextRead = 0;

	/*
	================
	fetchManifest
	================
	*/
	async function fetchManifest( base: string, signal: AbortSignal ) {
		const response = await io.request( new URL( MANIFEST_NAME, base ).href, { signal, cache: "no-store" } );
		if ( !response.ok ) throw statusError( `${MANIFEST_NAME}: HTTP ${response.status}`, response.status );
		const text = await response.text();
		if ( text.length > MANIFEST_LIMIT ) throw new Error( `${MANIFEST_NAME} is too large` );
		parsePatchManifest( text );
		return text;
	}

	/*
	================
	savedManifest

	The patches.json the last successful prepare() used, or "" (A5).
	================
	*/
	async function savedManifest( dir: FileSystemDirectoryHandle, signal: AbortSignal ) {
		const handle = await openHandle( dir, MANIFEST_NAME, signal );
		try {
			const bytes = new Uint8Array( handle.getSize() );
			handle.read( bytes, { at: 0 } );
			return new TextDecoder().decode( bytes );
		} finally {
			close( handle );
		}
	}

	/*
	================
	saveManifest
	================
	*/
	async function saveManifest( dir: FileSystemDirectoryHandle, text: string, signal: AbortSignal ) {
		const handle = await openHandle( dir, MANIFEST_NAME, signal );
		try {
			const bytes = new TextEncoder().encode( text );
			handle.truncate( 0 );
			handle.write( bytes, { at: 0 } );
			handle.flush();
		} finally {
			close( handle );
		}
	}

	/*
	================
	loadManifest

	The server's patches.json. When /updates/ cannot be reached (the Agent
	restarting, a dropped connection) the one the last successful start used
	stands in, with the fetch's error: prepare() can still open archives
	that are already whole, and fails with that error only if it would have
	to download. A first start has nothing saved and fails at once.
	================
	*/
	async function loadManifest( dir: FileSystemDirectoryHandle, base: string, signal: AbortSignal ) {
		try {
			return { text: await fetchManifest( base, signal ), unreachable: null };
		} catch ( error ) {
			if ( signal.aborted ) throw error;
			const saved = await savedManifest( dir, signal );
			if ( !saved ) throw error;
			return { text: saved, unreachable: error };
		}
	}

	/*
	================
	openHandle

	A read-write handle on name, waiting out another page's worker that
	still holds the file (LOCK_WAIT_MS). Any other failure is immediate.
	================
	*/
	async function openHandle( dir: FileSystemDirectoryHandle, name: string, signal: AbortSignal ) {
		const file = await dir.getFileHandle( name, { create: true } );
		for ( let waited = 0;; waited += LOCK_RETRY_MS ) {
			try {
				const handle = await file.createSyncAccessHandle();
				handles.push( handle );
				return handle;
			} catch ( error ) {
				const locked = error instanceof Error && error.name === "NoModificationAllowedError";
				if ( !locked || waited >= LOCK_WAIT_MS ) throw error;
				await io.wait( LOCK_RETRY_MS, signal );
			}
		}
	}

	/*
	================
	openFiles

	The archive's handle and its progress record's, both read-write.
	================
	*/
	async function openFiles( dir: FileSystemDirectoryHandle, release: ReleaseFile, signal: AbortSignal ) {
		const file = await openHandle( dir, release.name, signal );
		const progress = await openHandle( dir, release.name + PROGRESS_SUFFIX, signal );
		return { file, progress };
	}

	/*
	================
	requireSpace

	Refuses before downloading when the origin's quota cannot hold the
	missing bytes: a download that fails at 90% helps nobody.
	================
	*/
	async function requireSpace( missing: number ) {
		if ( missing === 0 ) return;
		const { quota, usage } = await io.storage.estimate();
		if ( quota === undefined || usage === undefined ) return;
		const needed = missing * FREE_SPACE_MARGIN;
		if ( quota - usage < needed ) {
			throw new Error(
				`Not enough storage for the game files: ${(needed / GIB).toFixed( 1 )} GB needed, ` +
					`${((quota - usage) / GIB).toFixed( 1 )} GB available`
			);
		}
	}

	/*
	================
	missingBytes

	What a set of opened files still lacks of their releases.
	================
	*/
	function missingBytes( files: OpenedRelease[] ) {
		return files.reduce( ( sum, f ) => sum + f.release.size - Math.min( f.file.getSize(), f.release.size ), 0 );
	}

	/*
	================
	download

	Downloads (or resumes) one opened release, yielding its progress.
	================
	*/
	async function* download(
		base: string,
		f: OpenedRelease,
		signal: AbortSignal
	): AsyncGenerator<PrepareProgress, void> {
		const job = {
			release: f.release,
			baseUrl: base,
			file: f.file,
			progress: f.progress,
			request: io.request,
			signal
		};
		for await ( const step of downloadArchive( job ) ) yield { name: f.release.name, ...step };
	}

	/*
	================
	close
	================
	*/
	function close( handle: FileSystemSyncAccessHandle ) {
		handle.close();
		handles.splice( handles.indexOf( handle ), 1 );
	}

	/*
	================
	mountBases

	(Re)opens the base archives' tables of contents.
	================
	*/
	function mountBases( bases: OpenedRelease[] ) {
		mounted.length = 0;
		for ( const f of bases ) mounted.push( mount( f.release.name, f.file, passphrase ) );
	}

	/*
	================
	wantedPatches

	The patches whose live entries the bases do not already hold, opened;
	a patch the bases hold is not downloaded and its old file is removed.
	================
	*/
	async function wantedPatches(
		dir: FileSystemDirectoryHandle,
		patches: ReleaseFile[],
		live: Map<string, PatchEntry[]>,
		signal: AbortSignal
	) {
		const wanted: (OpenedRelease & { entries: PatchEntry[]; })[] = [];
		for ( const patch of patches ) {
			const entries = live.get( patch.name )!;
			if ( entries.every( inBases ) ) await discard( dir, patch.name );
			else wanted.push( { release: patch, entries, ...(await openFiles( dir, patch, signal )) } );
		}
		return wanted;
	}

	/*
	================
	discard

	Removes a patch file (and its record) the archives no longer need. A file
	another page's worker still holds stays for a later start.
	================
	*/
	async function discard( dir: FileSystemDirectoryHandle, name: string ) {
		for ( const entry of [ name, name + PROGRESS_SUFFIX ] ) {
			try {
				await dir.removeEntry( entry );
			} catch {}
		}
	}

	/*
	================
	inBases

	True when the base archives already hold a patch entry exactly (size and
	plaintext checksum), as an install from last_version does for every
	patch the server merged into it (sropk.rs missing_patch_entries).
	================
	*/
	function inBases( entry: PatchEntry ) {
		const held = findInBases( normalizeTemirPath( entry.path ) );
		return held !== null && held.entry.length === entry.size && held.entry.checksum === entry.checksum;
	}

	/*
	================
	prepare

	Makes the archives patches.json describes readable, yielding each
	download's progress:

	1. The three base archives. A first install downloads last_version,
	   which already has every patch merged in. A finished base an update
	   has moved past ("behind") is kept while the server publishes
	   differential patches, as updater.rs keeps a mounted base. Without
	   patches, a changed base is downloaded again.
	2. Each differential patch whose live entries the bases do not already
	   hold is downloaded and its live entries overlay the bases; a patch the
	   bases already hold is not downloaded, and its old file is removed.

	A patch is downloaded whole, and the server's patches run to gigabytes
	(Patch010 alone is 1 GB in the 2026-10-26 production manifest). When
	the patches a player needs cost more than downloading the behind bases
	again from last_version, the bases are downloaded instead, and no patch
	is needed. Progress records stay open until the end, so a base can
	still be downloaded after that decision.
	================
	*/
	async function* prepare( signal: AbortSignal ): AsyncGenerator<PrepareProgress, void> {
		const base = updatesBaseUrl( origin );
		const dir = await (await io.storage.getDirectory()).getDirectoryHandle( DIRECTORY, { create: true } );
		const { text, unreachable } = await loadManifest( dir, base, signal );
		const manifest = parsePatchManifest( text );
		const live = livePatchEntries( manifest );
		const patches = manifest.files.filter( f => live.has( f.name ) );
		/*
		================
		obtain

		One download; refused, with the reason /updates/ was out of reach,
		when the saved manifest stands in for the server's.
		================
		*/
		const obtain = async function*( f: OpenedRelease ): AsyncGenerator<PrepareProgress, void> {
			if ( unreachable ) throw unreachable;
			yield* download( base, f, signal );
		};

		const bases: OpenedRelease[] = [];
		for ( const release of baseArchiveReleases( manifest ) ) {
			bases.push( { release, ...(await openFiles( dir, release, signal )) } );
		}
		const current = ( f: OpenedRelease ) => archiveComplete( f.release, f.file, f.progress );
		const behind = bases.filter( f =>
			!current( f ) && patches.length > 0 && completeAsRecorded( f.file, f.progress )
		);
		const stale = bases.filter( f => !current( f ) && !behind.includes( f ) );
		await requireSpace( missingBytes( stale ) );
		for ( const f of stale ) yield* obtain( f );
		mountBases( bases );

		let wanted = await wantedPatches( dir, patches, live, signal );
		const baseCost = behind.reduce( ( sum, f ) => sum + f.release.size, 0 );
		if ( behind.length > 0 && missingBytes( wanted ) > baseCost ) {
			for ( const f of wanted ) {
				close( f.file );
				close( f.progress );
			}
			mounted.length = 0;
			await requireSpace( baseCost );
			for ( const f of behind ) yield* obtain( f );
			mountBases( bases );
			wanted = await wantedPatches( dir, patches, live, signal );
		}
		const pending = wanted.filter( f => !archiveComplete( f.release, f.file, f.progress ) );
		await requireSpace( missingBytes( pending ) );
		for ( const f of pending ) yield* obtain( f );
		const patchMounts: Mounted[] = [];
		for ( const f of wanted ) {
			const patch = mount( f.release.name, f.file, passphrase );
			patchMounts.push( patch );
			for ( const entry of f.entries ) {
				const key = normalizeTemirPath( entry.path ), held = patch.archive.entries.get( key );
				if ( held ) overlay.set( key, { mount: patch, entry: held } );
			}
		}
		for ( const f of [ ...bases, ...wanted ] ) close( f.progress );
		// The manifest a later start falls back to when /updates/ is out of reach.
		if ( !unreachable ) await saveManifest( dir, text, signal );
		await startReaders( dir, [ ...mounted, ...patchMounts ], signal );
		ready = !disposed;
	}

	/*
	================
	answer

	A reader's reply settles the read it owes.
	================
	*/
	function answer( reply: ReaderReply ) {
		if ( reply.kind !== "bytes" && reply.kind !== "read-failed" ) return;
		const read = owed.get( reply.id );
		if ( !read ) return;
		owed.delete( reply.id );
		read.reader.pending--;
		const { resolve, reject } = read;
		if ( reply.kind === "bytes" ) resolve( reply.bytes );
		else reject( new Error( reply.error ) );
	}

	/*
	================
	startReaders

	Hands the archive files to io.readers reader workers (K8). Read-write
	handles are exclusive, so this worker closes its own first; each reader
	opens every file read-only, which several may. A browser without the
	read-only mode (an open refused) keeps the files here: the readers
	stop, this worker reopens its handles and reads alone, as before.
	================
	*/
	async function startReaders( dir: FileSystemDirectoryHandle, files: Mounted[], signal: AbortSignal ) {
		if ( io.readers < 2 || files.length === 0 ) return;
		for ( const file of files ) close( file.handle );
		const pool: Reader[] = [];
		for ( let i = 0; i < io.readers; i++ ) pool.push( { port: (io.spawnReader ?? spawnReader)(), pending: 0 } );
		const opened = await Promise.all( pool.map( reader =>
			new Promise<boolean>( resolve => {
				reader.port.onmessage = ( event: MessageEvent<ReaderReply> ) => {
					if ( event.data.kind === "opened" ) resolve( true );
					else if ( event.data.kind === "open-failed" ) resolve( false );
				};
				const open: ReaderRequest = { kind: "open", directory: DIRECTORY, files: files.map( f => f.name ) };
				reader.port.postMessage( open );
			} )
		) );
		if ( opened.every( Boolean ) && !disposed ) {
			for ( const reader of pool ) {
				reader.port.onmessage = ( event: MessageEvent<ReaderReply> ) => answer( event.data );
			}
			readers.push( ...pool );
			return;
		}
		for ( const reader of pool ) reader.port.terminate();
		for ( const file of files ) file.handle = await openHandle( dir, file.name, signal );
	}

	/*
	================
	readRange

	length plaintext bytes of entry from stream offset start: on the least
	busy reader worker, or here when there are none.
	================
	*/
	function readRange( source: Mounted, entry: TemirEntry, start: number, length: number ) {
		if ( readers.length === 0 ) {
			const bytes = readExact( source.handle, entry.offset + start, length );
			decryptTemirRange( source.archive, entry, start, bytes );
			return Promise.resolve( bytes );
		}
		const reader = readers.reduce( ( best, r ) => r.pending < best.pending ? r : best );
		const id = nextRead++;
		reader.pending++;
		return new Promise<Uint8Array<ArrayBuffer>>( ( resolve, reject ) => {
			owed.set( id, { reader, resolve, reject } );
			const request: ReaderRequest = {
				kind: "read",
				id,
				file: source.name,
				at: entry.offset + start,
				length,
				key: source.archive.keyWords,
				nonce: entry.nonce,
				start
			};
			reader.port.postMessage( request );
		} );
	}

	/*
	================
	findInBases
	================
	*/
	function findInBases( key: string ): { mount: Mounted; entry: TemirEntry; } | null {
		for ( const archive of mounted ) {
			const entry = archive.archive.entries.get( key );
			if ( entry ) return { mount: archive, entry };
		}
		return null;
	}

	/*
	================
	find

	The entry for a URL path: a live patch entry first, else whichever base
	archive holds it (each path lives in exactly one, by
	classify_virtual_path).
	================
	*/
	function find( key: string ): { mount: Mounted; entry: TemirEntry; } | null {
		return overlay.get( key ) ?? findInBases( key );
	}

	/*
	================
	read

	A PackDownload answered from the archives: a whole file, or the range
	of one the pack reader asks for. A whole .json file is read from its
	.json.gz twin when there is one and inflated, the order the shell's edge
	serves them in (lib.rs serve_local_file, Content-Encoding: gzip), so a
	patch that updates only the compressed twin is seen. Throws a 404 for a
	path no archive holds.
	================
	*/
	async function read( url: string, limit: number, range?: ArchiveRange ): Promise<Uint8Array<ArrayBuffer>> {
		if ( !ready ) throw new Error( "Game archives are not open" );
		const key = normalizeTemirPath( decodeURIComponent( new URL( url ).pathname ) );
		const packed = !range && key.endsWith( ".json" ) ? find( key + ".gz" ) : null;
		if ( packed ) {
			return gunzipBytes( await readRange( packed.mount, packed.entry, 0, packed.entry.length ), limit );
		}
		const hit = find( key );
		if ( !hit ) throw statusError( "Asset HTTP 404", 404 );
		const { mount: source, entry } = hit;
		if ( range ) {
			if ( range.total !== entry.length || range.start > range.end || range.end >= entry.length ) {
				throw new Error( "Invalid asset range response" );
			}
			return readRange( source, entry, range.start, range.end - range.start + 1 );
		}
		if ( entry.length > limit ) {
			throw Object.assign( new Error( "Asset exceeds byte budget" ), { code: "ASSET_BYTE_LIMIT" } );
		}
		return readRange( source, entry, 0, entry.length );
	}

	return {
		prepare,
		read,
		/*
		================
		dispose

		Closes every handle, so the files are free for the next page.
		================
		*/
		dispose() {
			disposed = true;
			ready = false;
			for ( const reader of readers ) reader.port.terminate();
			readers.length = 0;
			for ( const id of [ ...owed.keys() ] ) answer( { kind: "read-failed", id, error: "Game archives closed" } );
			// Every open handle, mounted archives included, is in handles.
			for ( const handle of handles ) handle.close();
			mounted.length = 0;
			overlay.clear();
			handles.length = 0;
		}
	};
}
