/*
===========================================================================

reader.ts - one archive reader worker

Opens the installed PWA's archive files read-only and serves the owner's
range reads (internal/reader-contract.ts): read the encrypted bytes,
decrypt them in place, transfer the buffer back. Decryption is most of a
read's cost (A1: 69% on one worker), so several readers run it in
parallel, off the asset worker that decodes what they return.

===========================================================================
*/

import { chachaXorAt } from "@/engine/foundation/assets/temir";
import type { ReaderReply, ReaderRequest } from "./internal/reader-contract";

/*
================
createArchiveReader

send posts a reply to the owner, transferring the listed buffers.
================
*/
export function createArchiveReader( send: ( reply: ReaderReply, transfer: Transferable[] ) => void ) {
	const files = new Map<string, FileSystemSyncAccessHandle>();

	/*
	================
	closeAll
	================
	*/
	function closeAll() {
		for ( const handle of files.values() ) handle.close();
		files.clear();
	}

	/*
	================
	open

	Opens every file read-only, or none: a browser without the mode opens
	the first read-write and refuses the next, and the owner then reads on
	its own.
	================
	*/
	async function open( directory: string, names: string[] ) {
		try {
			const dir = await (await navigator.storage.getDirectory()).getDirectoryHandle( directory );
			for ( const name of names ) {
				const file = await dir.getFileHandle( name );
				files.set( name, await file.createSyncAccessHandle( { mode: "read-only" } ) );
			}
			send( { kind: "opened" }, [] );
		} catch ( error ) {
			closeAll();
			send( { kind: "open-failed", error: String( error ) }, [] );
		}
	}

	/*
	================
	read
	================
	*/
	function read( request: Extract<ReaderRequest, { kind: "read"; }> ) {
		const handle = files.get( request.file );
		const bytes = new Uint8Array( new ArrayBuffer( request.length ) );
		const got = handle ? handle.read( bytes, { at: request.at } ) : -1;
		if ( got !== request.length ) {
			send( {
				kind: "read-failed",
				id: request.id,
				error: `${request.file}: read ${got} of ${request.length} bytes at ${request.at}`
			}, [] );
			return;
		}
		chachaXorAt( request.key, request.nonce, request.start, bytes );
		send( { kind: "bytes", id: request.id, bytes }, [ bytes.buffer ] );
	}

	return {
		/*
		================
		receive
		================
		*/
		receive( request: ReaderRequest ) {
			if ( request.kind === "open" ) void open( request.directory, request.files );
			else read( request );
		}
	};
}
