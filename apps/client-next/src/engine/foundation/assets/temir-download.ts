/*
===========================================================================

temir-download.ts - resumable, verified .temir downloads into OPFS

Stage A3 of docs/PWA-OPFS-MIGRATION.md. The installed PWA downloads the
three base archives (last_version/Media.temir, Data.temir, World.temir) the
desktop shell downloads, from the same /updates/patches.json, and checks
them the same way (new-silkroad updater.rs): exact size, the whole file's
64-bit FNV-1a against the manifest checksum, and the header tag.

A 1.5 GB archive must survive a closed tab. Bytes are written straight into
the archive's OPFS file through a FileSystemSyncAccessHandle (dedicated
workers only), and every checkpointBytes the file is flushed and a small
progress file records the offset, the running FNV-1a and the response's
validator. A resumed download truncates back to the recorded offset (bytes
past it were never confirmed), asks for the rest with Range and If-Range,
and folds the new bytes into the stored digest, so nothing already on disk
is read again. If the manifest names a different build, or the server
answers 200 instead of 206 because the file changed underneath (the Agent
merges patches into last_version in place), the download starts over.

This module owns no storage and no network: the caller hands it the two
sync handles and its fetch. It reports progress by yielding, not through
callbacks.

===========================================================================
*/

import { fnv1a64Start, fnv1a64Update, fnv1a64Value, normalizeTemirPath, TEMIR_HEADER_BYTES, type Fnv64 } from "./temir";

// Names of the base archives in patches.json (UPDATE-AND-DEPLOY.md).
const BASE_ARCHIVES = [ "Media.temir", "Data.temir", "World.temir" ] as const;
// 8 MiB: a resumed download repeats at most this much, and a checkpoint
// (flush + progress write) costs little against it.
export const CHECKPOINT_BYTES = 0x80_0000;
const PROGRESS_FORMAT = "temir-download";
const PROGRESS_VERSION = 1;
const HTTP_PARTIAL = 206;
const HTTP_OK = 200;
const INTEGRITY_ERROR = "TEMIR_DOWNLOAD_INTEGRITY";
const TAG_BYTES = 16;

/*
================
PatchEntry

One file a differential patch carries: its virtual path, plaintext size
and plaintext FNV-1a, as the archives' tables of contents record them.
================
*/
export interface PatchEntry {
	path: string;
	size: number;
	checksum: bigint;
}

/*
================
ReleaseFile

One archive patches.json publishes. checksum is the whole file's FNV-1a;
0n and "" mean the manifest does not say, as updater.rs treats them.
entries lists a differential patch's files (empty for a base archive).
================
*/
export interface ReleaseFile {
	name: string;
	size: number;
	checksum: bigint;
	headerTag: string;
	url: string;
	entries: PatchEntry[];
}

/*
================
PatchManifest
================
*/
export interface PatchManifest {
	version: number;
	files: ReleaseFile[];
}

/*
================
ArchiveDownload

One download job. baseUrl ends with "/updates/". file is the archive's own
OPFS handle, progress a second, small file beside it.
================
*/
export interface ArchiveDownload {
	release: ReleaseFile;
	baseUrl: string;
	file: FileSystemSyncAccessHandle;
	progress: FileSystemSyncAccessHandle;
	request: ( url: string, init: RequestInit ) => Promise<Response>;
	signal: AbortSignal;
	checkpointBytes?: number;
}

/*
================
DownloadProgress

What the download yields: bytes on disk out of the archive's size.
================
*/
export interface DownloadProgress {
	received: number;
	total: number;
}

/*
================
SavedProgress

The progress file's record. The release fields say which build the bytes
belong to; offset and the digest halves say how far they are confirmed.
================
*/
interface SavedProgress {
	format: string;
	version: number;
	name: string;
	size: number;
	headerTag: string;
	offset: number;
	fnvLo: number;
	fnvHi: number;
	validator: string;
	done: boolean;
}

/*
================
integrityError

The downloaded bytes are not the release: a short body, a wrong checksum or
header tag. Distinct from network faults (retry) by its code.
================
*/
function integrityError( message: string ) {
	return Object.assign( new Error( message ), { code: INTEGRITY_ERROR } );
}

/*
================
isArchiveIntegrityError
================
*/
export function isArchiveIntegrityError( error: unknown ): boolean {
	return error instanceof Error && "code" in error && error.code === INTEGRITY_ERROR;
}

// ============================================================================

/*
================
parsePatchManifest

patches.json from the Agent. Its checksums are unsigned 64-bit integers,
most of them above 2^53, which JSON.parse would round; they are quoted
before parsing and read as BigInt. url defaults to the file's name.
================
*/
export function parsePatchManifest( text: string ): PatchManifest {
	const quoted = text.replace( /("checksum"\s*:\s*)(\d+)/g, '$1"$2"' );
	const raw = JSON.parse( quoted ) as {
		version?: number;
		patches?: Array<{
			name?: string;
			size?: number;
			checksum?: string;
			headerTag?: string;
			url?: string;
			entries?: Array<{ path?: string; size?: number; checksum?: string; }>;
		}>;
	};
	if ( !raw || typeof raw !== "object" || !Array.isArray( raw.patches ) ) throw new Error( "invalid patches.json" );
	return {
		version: Number( raw.version ?? 0 ),
		files: raw.patches.map( patch => {
			if ( typeof patch.name !== "string" || !Number.isSafeInteger( patch.size ) ) {
				throw new Error( "invalid patches.json entry" );
			}
			return {
				name: patch.name,
				size: patch.size!,
				checksum: BigInt( patch.checksum ?? "0" ),
				headerTag: (patch.headerTag ?? "").trim().toLowerCase(),
				url: patch.url ?? patch.name,
				entries: (patch.entries ?? []).map( entry => {
					if ( typeof entry.path !== "string" ) throw new Error( "invalid patches.json patch entry" );
					return { path: entry.path, size: entry.size ?? 0, checksum: BigInt( entry.checksum ?? "0" ) };
				} )
			};
		} )
	};
}

/*
================
isDifferentialPatch

A PatchNNN.temir the Agent's sync packs (sropk.rs is_differential_patch_name).
================
*/
export function isDifferentialPatch( name: string ) {
	return name.startsWith( "Patch" ) && name.endsWith( ".temir" );
}

/*
================
bundleFamily

Vite emits one hashed entry bundle and one hashed stylesheet per build
(assets/bootstrap-<hash>.js, assets/index-<hash>.css): a newer file of a
family supersedes every older one although their paths differ (sropk.rs
bundle_family).
================
*/
function bundleFamily( key: string ) {
	if ( key.startsWith( "assets/bootstrap-" ) && key.endsWith( ".js" ) ) return "bootstrap";
	if ( key.startsWith( "assets/index-" ) && key.endsWith( ".css" ) ) return "index-css";
	return null;
}

/*
================
livePatchEntries

Per differential patch (manifest order), the entries that still describe
the final state: an entry is dead when a later patch carries the same path
or a later bundle of the same family. Overlaying only live entries keeps an
old patch from rolling a newer file back, and every path is live in at most
one patch (sropk.rs live_patch_entries).
================
*/
export function livePatchEntries( manifest: PatchManifest ): Map<string, PatchEntry[]> {
	const live = new Map<string, PatchEntry[]>();
	const laterKeys = new Set<string>(), laterFamilies = new Set<string>();
	const patches = manifest.files.filter( f => isDifferentialPatch( f.name ) && f.entries.length > 0 );
	for ( const patch of [ ...patches ].reverse() ) {
		const kept = patch.entries.filter( entry => {
			const key = normalizeTemirPath( entry.path ), family = bundleFamily( key );
			return !laterKeys.has( key ) && !(family && laterFamilies.has( family ));
		} );
		for ( const entry of patch.entries ) {
			const key = normalizeTemirPath( entry.path ), family = bundleFamily( key );
			laterKeys.add( key );
			if ( family ) laterFamilies.add( family );
		}
		live.set( patch.name, kept );
	}
	return new Map( patches.map( patch => [ patch.name, live.get( patch.name )! ] ) );
}

/*
================
baseArchiveReleases

The three base archives a first install downloads, in BASE_ARCHIVES order.
Throws when the manifest lacks one: an install cannot start half-way.
================
*/
export function baseArchiveReleases( manifest: PatchManifest ): ReleaseFile[] {
	return BASE_ARCHIVES.map( name => {
		const file = manifest.files.find( f => f.name.toLowerCase() === name.toLowerCase() );
		if ( !file ) throw new Error( `patches.json has no ${name}` );
		return file;
	} );
}

// ============================================================================

/*
================
readProgress

The saved record, or null when there is none or it is not ours.
================
*/
function readProgress( handle: FileSystemSyncAccessHandle ): SavedProgress | null {
	const size = handle.getSize();
	if ( size === 0 ) return null;
	const bytes = new Uint8Array( size );
	handle.read( bytes, { at: 0 } );
	try {
		const saved = JSON.parse( new TextDecoder().decode( bytes ) ) as SavedProgress;
		return saved.format === PROGRESS_FORMAT && saved.version === PROGRESS_VERSION ? saved : null;
	} catch {
		return null;
	}
}

/*
================
writeProgress

Replaces the record and flushes it. Called only after the archive file
itself was flushed, so the record never points past confirmed bytes.
================
*/
function writeProgress( handle: FileSystemSyncAccessHandle, saved: SavedProgress ) {
	const bytes = new TextEncoder().encode( JSON.stringify( saved ) );
	handle.truncate( 0 );
	handle.write( bytes, { at: 0 } );
	handle.flush();
}

/*
================
headerTagOf

The archive's header tag as patches.json writes it (lower-case hex of
header bytes 48..64), or "" when the file is too short to have one.
================
*/
function headerTagOf( file: FileSystemSyncAccessHandle ) {
	if ( file.getSize() < TEMIR_HEADER_BYTES ) return "";
	const header = new Uint8Array( TEMIR_HEADER_BYTES );
	file.read( header, { at: 0 } );
	let hex = "";
	for ( const byte of header.subarray( TEMIR_HEADER_BYTES - TAG_BYTES ) ) {
		hex += byte.toString( 16 ).padStart( 2, "0" );
	}
	return hex;
}

/*
================
strongValidator

The response's validator usable in If-Range: an ETag unless weak (RFC
9110 13.1.5 forbids weak ones there), else Last-Modified, else none.
================
*/
function strongValidator( response: Response ) {
	const etag = response.headers.get( "ETag" ) ?? "";
	if ( etag && !etag.startsWith( "W/" ) ) return etag;
	return response.headers.get( "Last-Modified" ) ?? "";
}

/*
================
rangeStart

The first byte of a 206 response (Content-Range: bytes a-b/n), or -1.
================
*/
function rangeStart( response: Response ) {
	const match = /^bytes\s+(\d+)-/.exec( response.headers.get( "Content-Range" ) ?? "" );
	return match ? Number( match[1] ) : -1;
}

/*
================
DownloadState

How far a release is confirmed on disk: the offset, the FNV-1a of every
byte before it, and the validator of the response those bytes came from.
================
*/
interface DownloadState {
	offset: number;
	fnv: Fnv64;
	validator: string;
}

/*
================
resumePoint

Where the saved record lets this release continue: its offset and digest
when it is the same build and the file still holds those bytes, else the
start. done says the record already holds a verified download.
================
*/
function resumePoint( job: ArchiveDownload ): { state: DownloadState; done: boolean; } {
	const saved = readProgress( job.progress ), release = job.release;
	if (
		saved && saved.name === release.name && saved.size === release.size &&
		saved.headerTag === release.headerTag && saved.offset <= job.file.getSize()
	) {
		return {
			state: { offset: saved.offset, fnv: { lo: saved.fnvLo, hi: saved.fnvHi }, validator: saved.validator },
			done: saved.done
		};
	}
	return { state: { offset: 0, fnv: fnv1a64Start(), validator: "" }, done: false };
}

/*
================
savedRecord
================
*/
function savedRecord( release: ReleaseFile, state: DownloadState, done: boolean ): SavedProgress {
	return {
		format: PROGRESS_FORMAT,
		version: PROGRESS_VERSION,
		name: release.name,
		size: release.size,
		headerTag: release.headerTag,
		offset: state.offset,
		fnvLo: state.fnv.lo,
		fnvHi: state.fnv.hi,
		validator: state.validator,
		done
	};
}

/*
================
forget

Drops every byte of the attempt and records that, so the next attempt
starts from the first byte.
================
*/
function forget( job: ArchiveDownload, state: DownloadState ) {
	state.offset = 0;
	state.fnv = fnv1a64Start();
	job.file.truncate( 0 );
	writeProgress( job.progress, savedRecord( job.release, state, false ) );
}

/*
================
archiveComplete

True when a finished download of exactly this release is on disk: the
record says done for this build and the file's size and header tag agree.
The cheap check an installed app makes on every start.
================
*/
export function archiveComplete(
	release: ReleaseFile,
	file: FileSystemSyncAccessHandle,
	progress: FileSystemSyncAccessHandle
) {
	const saved = readProgress( progress );
	return Boolean( saved?.done ) && saved!.name === release.name && saved!.size === release.size &&
		saved!.headerTag === release.headerTag && file.getSize() === release.size &&
		(!release.headerTag || headerTagOf( file ) === release.headerTag);
}

/*
================
completeAsRecorded

True when the file holds a finished, verified download of whichever build
its record names, current or not: a base archive an update has moved past
is still a whole archive that patches can complete.
================
*/
export function completeAsRecorded( file: FileSystemSyncAccessHandle, progress: FileSystemSyncAccessHandle ) {
	const saved = readProgress( progress );
	return Boolean( saved?.done ) && file.getSize() === saved!.size &&
		(!saved!.headerTag || headerTagOf( file ) === saved!.headerTag);
}

/*
================
openRest

Requests the bytes from state.offset on. A 206 must start exactly there; a
200 is the whole file (nothing was asked for, or it changed since the
validator), so the attempt starts over and reads it from the first byte.
================
*/
async function openRest( job: ArchiveDownload, state: DownloadState ) {
	const release = job.release, headers: Record<string, string> = {};
	if ( state.offset > 0 ) {
		headers.Range = `bytes=${state.offset}-`;
		if ( state.validator ) headers["If-Range"] = state.validator;
	}
	const response = await job.request( new URL( release.url, job.baseUrl ).href, {
		headers,
		signal: job.signal,
		cache: "no-store"
	} );
	if ( response.status === HTTP_PARTIAL && state.offset > 0 ) {
		if ( rangeStart( response ) !== state.offset ) {
			throw new Error( `${release.name}: range does not start at ${state.offset}` );
		}
	} else if ( response.status === HTTP_OK ) {
		if ( state.offset > 0 ) forget( job, state );
	} else {
		throw new Error( `${release.name}: HTTP ${response.status}` );
	}
	if ( !response.body ) throw new Error( `${release.name}: empty response` );
	state.validator = strongValidator( response ) || state.validator;
	return response.body.getReader();
}

/*
================
receive

Writes the response into the archive and checkpoints every
checkpointBytes, yielding progress at the start and at each checkpoint.
A body longer than the release is an integrity error.
================
*/
async function* receive( job: ArchiveDownload, state: DownloadState, checkpoint: number ) {
	const release = job.release, reader = await openRest( job, state );
	yield { received: state.offset, total: release.size };
	let unsaved = 0;
	try {
		for ( ;; ) {
			const { done, value } = await reader.read();
			if ( done ) return;
			if ( state.offset + value.length > release.size ) {
				forget( job, state );
				throw integrityError( `${release.name}: longer than ${release.size} bytes` );
			}
			job.file.write( value, { at: state.offset } );
			state.fnv = fnv1a64Update( state.fnv, value );
			state.offset += value.length;
			unsaved += value.length;
			if ( unsaved >= checkpoint ) {
				job.file.flush();
				writeProgress( job.progress, savedRecord( release, state, false ) );
				unsaved = 0;
				yield { received: state.offset, total: release.size };
			}
		}
	} finally {
		reader.releaseLock();
	}
}

/*
================
settle

The finished attempt's verdict. Short: keep it for the next attempt. Wrong
checksum or header tag: forget it, it can never become right. Otherwise
record the download as done.
================
*/
function settle( job: ArchiveDownload, state: DownloadState ) {
	const release = job.release;
	job.file.flush();
	if ( state.offset !== release.size ) {
		writeProgress( job.progress, savedRecord( release, state, false ) );
		throw new Error( `${release.name}: ended at ${state.offset} of ${release.size} bytes` );
	}
	const checksumOk = release.checksum === 0n || fnv1a64Value( state.fnv ) === release.checksum;
	const tagOk = !release.headerTag || headerTagOf( job.file ) === release.headerTag;
	if ( !checksumOk || !tagOk ) {
		forget( job, state );
		throw integrityError(
			`${release.name}: ${checksumOk ? "header tag" : "checksum"} does not match patches.json`
		);
	}
	writeProgress( job.progress, savedRecord( release, state, true ) );
}

/*
================
downloadArchive

Downloads job.release into job.file, resuming a previous attempt, and
yields progress as it goes. Throws an integrity error (and forgets the
attempt) when the bytes are not the release; a network failure or abort
leaves the last checkpoint for the next attempt.
================
*/
export async function* downloadArchive( job: ArchiveDownload ): AsyncGenerator<DownloadProgress, void> {
	const total = job.release.size;
	const { state, done } = resumePoint( job );
	if ( done && archiveComplete( job.release, job.file, job.progress ) ) {
		yield { received: total, total };
		return;
	}
	// Bytes past the record were never confirmed by a flush.
	job.file.truncate( state.offset );
	// When every byte arrived but the tab closed before the verdict, asking
	// for the empty rest would only earn a 416.
	if ( state.offset < total ) yield* receive( job, state, job.checkpointBytes ?? CHECKPOINT_BYTES );
	settle( job, state );
	yield { received: total, total };
}
