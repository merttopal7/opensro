/*
===========================================================================

temir-opfs-bench.mjs - .temir reads from OPFS against the desktop shell

Usage:
  node tools/perf/bench/temir-opfs-bench.mjs --archive PATH
       [--tauri] [--tauri-origin URL] [--budget-mb 256] [--block-kb 1024]
       [--workers 4] [--browser PATH] [--headed] [--json FILE]

Stage A1 of docs/PWA-OPFS-MIGRATION.md. Launches Edge (WebView2's engine)
with a throwaway profile, copies the archive into the Origin Private File
System from a dedicated worker, opens it with foundation/assets/temir.ts
and measures:

  copy      how long the install copy into OPFS takes
  open      header + TOC open, key derivation included
  verify    whole-member FNV-1a against the TOC for a few small members,
            proving the real key and the real archive decrypt correctly
  opfs      one worker: SyncAccessHandle.read + decrypt per range
  opfs-par  several workers on read-only handles, when the browser has them
  lock      whether a page getFile() succeeds while a worker holds the file
            (K7: can a service worker serve the shell from the same archive)
  tauri     with --tauri: the same ranges fetched from the running desktop
            shell's edge (127.0.0.1:15180 or --tauri-origin), same origin,
            12 at a time like the desktop client, with a byte comparison
            against OPFS

Every pass runs twice; the second pass reads OS-cached files on both sides.
Without --tauri the page is served by a throwaway local server, because
OPFS needs a secure context and is partitioned by origin.

The passphrase is the archive key the packers use (temir.go
DefaultArchiveKey), overridable with SRO_TEMIR_KEY.

===========================================================================
*/
import { createServer } from "node:http";
import { readFileSync, statSync } from "node:fs";
import { writeFile } from "node:fs/promises";
import { stripTypeScriptTypes } from "node:module";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { chromium } from "playwright-core";
import "../../../tests/helpers/native-source-loader.mjs";
import { root } from "../../project.mjs";
import { parseOptions } from "../core/report.mjs";

const USAGE = "temir-opfs-bench.mjs --archive PATH [--tauri] [--tauri-origin URL] [--budget-mb N] " +
	"[--block-kb N] [--workers N] [--browser PATH] [--headed] [--json FILE]";
const EDGE = "C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe";
// The shell binds 15180 and moves to the next free port when it is taken
// (lib.rs start_local_edge); --tauri-origin names the one it got.
const TAURI_ORIGIN = "http://127.0.0.1:15180";
// The desktop client keeps 12 requests in flight (assets.ts REQUEST_CAPACITY).
const TAURI_CONCURRENCY = 12;
const DEFAULT_KEY = "OpenSRO-v150-Legend3-TEMIR-Key-2026!#99";
const OPFS_NAME = "bench.temir";
const PASSES = 2;
const VERIFY_MEMBERS = 8;
const VERIFY_MAX_BYTES = 4 << 20;
const PLAN_SEED = 0x5eed;
const temirPath = path.join( root, "src/engine/foundation/assets/temir.ts" );

/*
================
workerMain

Runs inside the dedicated worker, after temir.ts's own code. Serialized with
toString(), so it may use only the worker's globals and temir.ts functions.
================
*/
function workerMain() {
	let handle = null, archive = null, fileBytes = 0;

	async function opfsFile( create ) {
		const dir = await navigator.storage.getDirectory();
		return dir.getFileHandle( "bench.temir", { create } );
	}

	function readAt( offset, length ) {
		const bytes = new Uint8Array( length );
		const got = handle.read( bytes, { at: offset } );
		if ( got !== length ) throw new Error( `short read at ${offset}: ${got} of ${length}` );
		return bytes;
	}

	async function install( file ) {
		const fh = await opfsFile( true );
		const sync = await fh.createSyncAccessHandle();
		try {
			if ( sync.getSize() === file.size ) return { skipped: true, bytes: file.size, ms: 0 };
			const started = performance.now();
			sync.truncate( 0 );
			const reader = file.stream().getReader();
			let at = 0;
			for ( ;; ) {
				const { done, value } = await reader.read();
				if ( done ) break;
				at += sync.write( value, { at } );
			}
			sync.flush();
			return { skipped: false, bytes: at, ms: performance.now() - started };
		} finally {
			sync.close();
		}
	}

	async function open( { passphrase, mode } ) {
		const fh = await opfsFile( false );
		const started = performance.now();
		handle = await fh.createSyncAccessHandle( mode ? { mode } : undefined );
		fileBytes = handle.getSize();
		const header = readTemirHeader( readAt( 0, TEMIR_HEADER_BYTES ), fileBytes, passphrase );
		archive = openTemirToc( header, readAt( header.tocOffset, header.tocLength ), fileBytes );
		return { ms: performance.now() - started, entries: archive.entries.size, fileBytes };
	}

	function verify( { count, maxBytes } ) {
		const small = [ ...archive.entries.values() ].filter( e => e.length > 0 && e.length <= maxBytes );
		const picked = small.filter( ( _, i ) => i % Math.max( 1, Math.floor( small.length / count ) ) === 0 )
			.slice( 0, count );
		return picked.map( entry => {
			const bytes = decryptTemirRange( archive, entry, 0, readAt( entry.offset, entry.length ) );
			return { path: entry.path, bytes: entry.length, ok: fnv1a64( bytes ) === entry.checksum };
		} );
	}

	function plan( { budget, block, seed } ) {
		// mulberry32: the same seed gives the same plan on every run.
		let state = seed >>> 0;
		const random = () => {
			state = (state + 0x6d2b79f5) >>> 0;
			let t = state;
			t = Math.imul( t ^ (t >>> 15), t | 1 );
			t ^= t + Math.imul( t ^ (t >>> 7), t | 61 );
			return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
		};
		const packs = [ ...archive.entries.values() ].filter( e => e.path.endsWith( ".bin" ) );
		for ( let i = packs.length - 1; i > 0; i-- ) {
			const j = Math.floor( random() * (i + 1) );
			[packs[i], packs[j]] = [ packs[j], packs[i] ];
		}
		const ranges = [];
		let total = 0;
		for ( const entry of packs ) {
			for ( let start = 0; start < entry.length && total < budget; start += block ) {
				const length = Math.min( block, entry.length - start );
				ranges.push( { path: entry.path, start, length } );
				total += length;
			}
			if ( total >= budget ) break;
		}
		return ranges;
	}

	function read( { ranges } ) {
		const latencies = [];
		let readMs = 0, decryptMs = 0, bytes = 0, check = 0;
		const started = performance.now();
		for ( const range of ranges ) {
			const entry = archive.entries.get( normalizeTemirPath( range.path ) );
			const t0 = performance.now();
			const data = readAt( entry.offset + range.start, range.length );
			const t1 = performance.now();
			decryptTemirRange( archive, entry, range.start, data );
			const t2 = performance.now();
			readMs += t1 - t0;
			decryptMs += t2 - t1;
			latencies.push( t2 - t0 );
			bytes += data.length;
			check = (check + data[0] + data[data.length - 1]) >>> 0;
		}
		return { wallMs: performance.now() - started, readMs, decryptMs, bytes, latencies, check };
	}

	// Every byte of one range as an FNV-1a digest, for the byte comparison
	// with the desktop shell (which hashes its copy the same way).
	function sample( { range } ) {
		const entry = archive.entries.get( normalizeTemirPath( range.path ) );
		const bytes = decryptTemirRange(
			archive,
			entry,
			range.start,
			readAt( entry.offset + range.start, range.length )
		);
		return fnv1a64( bytes ).toString( 16 );
	}

	const ops = { install, open, verify, plan, read, sample };
	self.onmessage = async event => {
		const { id, op, args } = event.data;
		try {
			if ( op === "close" ) {
				handle?.close();
				handle = null;
				self.postMessage( { id, result: true } );
				return;
			}
			self.postMessage( { id, result: await ops[op]( args ) } );
		} catch ( error ) {
			self.postMessage( { id, error: `${error.name}: ${error.message}` } );
		}
	};
}

/*
================
pageMain

Installed in the page: starts workers from the worker source and offers the
operations the bench drives through page.evaluate.
================
*/
function pageMain( workerSource ) {
	const url = URL.createObjectURL( new Blob( [ workerSource ], { type: "text/javascript" } ) );
	let nextId = 0;
	window.benchWorker = () => {
		const worker = new Worker( url, { type: "module" } );
		const pending = new Map();
		worker.onmessage = event => {
			const { id, result, error } = event.data;
			const settle = pending.get( id );
			pending.delete( id );
			if ( error ) settle.reject( new Error( error ) );
			else settle.resolve( result );
		};
		return {
			call: ( op, args ) =>
				new Promise( ( resolve, reject ) => {
					const id = nextId++;
					pending.set( id, { resolve, reject } );
					worker.postMessage( { id, op, args } );
				} ),
			terminate: () => worker.terminate()
		};
	};
	window.benchWorkers = [];
}

/*
================
percentile
================
*/
function percentile( values, p ) {
	if ( values.length === 0 ) return 0;
	const sorted = [ ...values ].sort( ( a, b ) => a - b );
	return sorted[Math.min( sorted.length - 1, Math.floor( p * sorted.length ) )];
}

/*
================
summarize

One pass as MB/s and per-range latency percentiles.
================
*/
function summarize( pass ) {
	const mb = pass.bytes / (1 << 20);
	return {
		mbPerSecond: Math.round( mb / (pass.wallMs / 1000) ),
		wallMs: Math.round( pass.wallMs ),
		p50Ms: Number( percentile( pass.latencies, 0.5 ).toFixed( 2 ) ),
		p95Ms: Number( percentile( pass.latencies, 0.95 ).toFixed( 2 ) ),
		...(pass.decryptMs === undefined ? {} : {
			decryptShare: Number( (pass.decryptMs / pass.wallMs).toFixed( 2 ) ),
			readMs: Math.round( pass.readMs ),
			decryptMs: Math.round( pass.decryptMs )
		})
	};
}

/*
================
startPageServer

A blank page on a loopback origin, for runs without the desktop shell.
================
*/
async function startPageServer() {
	const server = createServer( ( _, res ) => {
		res.writeHead( 200, { "Content-Type": "text/html; charset=utf-8" } );
		res.end( "<!doctype html><title>temir-opfs-bench</title><input id=archive type=file>" );
	} );
	await new Promise( resolve => server.listen( 0, "127.0.0.1", resolve ) );
	return { server, origin: `http://127.0.0.1:${server.address().port}` };
}

/*
================
openPage

The page the bench drives. With the desktop shell, a small same-origin
document (the pack manifest) instead of the game, so the client does not
load beside the measurement.
================
*/
async function openPage( context, origin, tauri ) {
	const page = await context.newPage();
	await page.goto( tauri ? `${origin}/assets/packs/manifest.json` : origin );
	await page.evaluate( () => {
		if ( !document.getElementById( "archive" ) ) {
			const input = document.createElement( "input" );
			input.type = "file";
			input.id = "archive";
			document.body.append( input );
		}
	} );
	return page;
}

/*
================
lockProbe

K7: a service worker would read the shell from the same file the asset
worker holds open. getFile() from the page stands in for it.
================
*/
async function lockProbe( page ) {
	return page.evaluate( async name => {
		const dir = await navigator.storage.getDirectory();
		try {
			const file = await (await dir.getFileHandle( name )).getFile();
			await file.slice( 0, 64 ).arrayBuffer();
			return "getFile ok";
		} catch ( error ) {
			return `getFile failed: ${error.name}`;
		}
	}, OPFS_NAME );
}

/*
================
tauriPass

The same ranges from the desktop shell's edge, TAURI_CONCURRENCY at a time.
================
*/
async function tauriPass( page, ranges ) {
	return page.evaluate( async ( { ranges, concurrency } ) => {
		const latencies = [];
		let bytes = 0, next = 0, check = 0;
		const started = performance.now();
		async function lane() {
			while ( next < ranges.length ) {
				const range = ranges[next++];
				const t0 = performance.now();
				const response = await fetch( "/" + range.path.replace( /^\/+/, "" ), {
					headers: { Range: `bytes=${range.start}-${range.start + range.length - 1}` },
					cache: "no-store"
				} );
				const data = new Uint8Array( await response.arrayBuffer() );
				latencies.push( performance.now() - t0 );
				if ( data.length !== range.length ) {
					throw new Error( `${range.path}: ${data.length} of ${range.length}` );
				}
				bytes += data.length;
				check = (check + data[0] + data[data.length - 1]) >>> 0;
			}
		}
		await Promise.all( Array.from( { length: concurrency }, lane ) );
		return { wallMs: performance.now() - started, bytes, latencies, check };
	}, { ranges, concurrency: TAURI_CONCURRENCY } );
}

/*
================
main
================
*/
async function main() {
	const options = parseOptions( process.argv.slice( 2 ), {
		archive: "",
		tauri: false,
		tauriOrigin: TAURI_ORIGIN,
		budgetMb: 256,
		blockKb: 1024,
		workers: 4,
		browser: EDGE,
		headed: false,
		json: ""
	}, USAGE );
	if ( !options.archive ) throw Error( `--archive is required\nusage: ${USAGE}` );
	const archivePath = path.resolve( options.archive );
	const passphrase = process.env.SRO_TEMIR_KEY ?? DEFAULT_KEY;
	const temirSource = stripTypeScriptTypes( readFileSync( temirPath, "utf8" ) );
	const workerSource = `${temirSource}\n(${workerMain.toString()})();\n`;

	const local = options.tauri ? null : await startPageServer();
	const origin = options.tauri ? options.tauriOrigin : local.origin;
	const browser = await chromium.launch( { executablePath: options.browser, headless: !options.headed } );
	const report = {
		archive: archivePath,
		archiveBytes: statSync( archivePath ).size,
		browser: browser.version(),
		origin,
		budgetMb: options.budgetMb,
		blockKb: options.blockKb
	};
	try {
		const page = await openPage( await browser.newContext(), origin, options.tauri );
		await page.evaluate( pageMain, workerSource );
		await page.setInputFiles( "#archive", archivePath );

		const main = await page.evaluateHandle( () => window.benchWorker() );
		const call = ( op, args ) => main.evaluate( ( w, { op, args } ) => w.call( op, args ), { op, args } );
		report.copy = await page.evaluate( async () => {
			const worker = window.benchWorker();
			try {
				return await worker.call( "install", document.getElementById( "archive" ).files[0] );
			} finally {
				worker.terminate();
			}
		} );
		console.log( `copy: ${JSON.stringify( report.copy )}` );

		report.open = await call( "open", { passphrase } );
		console.log( `open: ${JSON.stringify( report.open )}` );
		report.verify = await call( "verify", { count: VERIFY_MEMBERS, maxBytes: VERIFY_MAX_BYTES } );
		console.log( `verify: ${report.verify.filter( v => v.ok ).length}/${report.verify.length} members match` );

		const ranges = await call( "plan", {
			budget: options.budgetMb << 20,
			block: options.blockKb << 10,
			seed: PLAN_SEED
		} );
		report.ranges = ranges.length;
		report.opfs = [];
		for ( let pass = 0; pass < PASSES; pass++ ) {
			const result = await call( "read", { ranges } );
			report.opfs.push( summarize( result ) );
			report.opfsCheck = result.check;
		}
		console.log( `opfs: ${JSON.stringify( report.opfs )}` );

		report.lock = { readwrite: await lockProbe( page ) };
		await call( "close" );

		// Read-only handles let several workers read one file at once (Chrome
		// 121+). Where the mode is unknown the second open fails, and the run
		// says so instead of measuring a serial fallback.
		report.opfsParallel = await page.evaluate( async ( { ranges, workers, passphrase, passes } ) => {
			const pool = Array.from( { length: workers }, () => window.benchWorker() );
			try {
				for ( const worker of pool ) await worker.call( "open", { passphrase, mode: "read-only" } );
			} catch ( error ) {
				pool.forEach( w => w.terminate() );
				return { unsupported: error.message };
			}
			const results = [];
			for ( let pass = 0; pass < passes; pass++ ) {
				const started = performance.now();
				const parts = await Promise.all(
					pool.map( ( worker, i ) =>
						worker.call( "read", { ranges: ranges.filter( ( _, j ) => j % workers === i ) } )
					)
				);
				results.push( {
					wallMs: performance.now() - started,
					bytes: parts.reduce( ( sum, p ) => sum + p.bytes, 0 ),
					latencies: parts.flatMap( p => p.latencies )
				} );
			}
			window.benchWorkers = pool;
			return { results };
		}, { ranges, workers: options.workers, passphrase, passes: PASSES } );
		if ( report.opfsParallel.results ) {
			report.lock.readOnly = await lockProbe( page );
			report.opfsParallel = report.opfsParallel.results.map( summarize );
		}
		console.log( `opfs-par: ${JSON.stringify( report.opfsParallel )}` );
		console.log( `lock: ${JSON.stringify( report.lock )}` );
		await page.evaluate( () => window.benchWorkers.forEach( w => w.terminate() ) );

		if ( options.tauri ) {
			await call( "open", { passphrase } );
			report.tauri = [];
			for ( let pass = 0; pass < PASSES; pass++ ) {
				const result = await tauriPass( page, ranges );
				report.tauri.push( summarize( result ) );
				report.tauriCheck = result.check;
			}
			// Byte equality on a whole sample range: OPFS + temir.ts decrypts
			// what the shell's Rust decrypts. The edge's bytes are hashed by
			// the same fnv1a64, in Node.
			const probe = ranges[Math.floor( ranges.length / 2 )];
			const ours = await call( "sample", { range: probe } );
			const theirs = await page.evaluate( async range => {
				const response = await fetch( "/" + range.path.replace( /^\/+/, "" ), {
					headers: { Range: `bytes=${range.start}-${range.start + range.length - 1}` },
					cache: "no-store"
				} );
				return Array.from( new Uint8Array( await response.arrayBuffer() ) );
			}, probe );
			const { fnv1a64 } = await import( pathToFileURL( temirPath ).href );
			report.sameBytes = report.opfsCheck === report.tauriCheck &&
				fnv1a64( Uint8Array.from( theirs ) ).toString( 16 ) === ours;
			console.log( `tauri: ${JSON.stringify( report.tauri )}` );
			console.log( `same bytes as the desktop shell: ${report.sameBytes}` );
			await call( "close" );
		}

		await page.evaluate( async name => {
			await (await navigator.storage.getDirectory()).removeEntry( name );
		}, OPFS_NAME );
	} finally {
		await browser.close();
		local?.server.close();
	}
	if ( options.json ) await writeFile( options.json, JSON.stringify( report, null, "\t" ) + "\n" );
	return report;
}

main().catch( error => {
	console.error( error );
	process.exitCode = 1;
} );
