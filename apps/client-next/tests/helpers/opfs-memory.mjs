/*
===========================================================================

opfs-memory.mjs - the Origin Private File System, in memory

The installed PWA's archive code writes and reads OPFS files through
FileSystemSyncAccessHandle, which Node does not have. memoryHandle is one
handle over a growable buffer; memoryStorage is a StorageManager whose
directory tree keeps each file's bytes across handles, the way a reopened
OPFS file keeps its contents.

===========================================================================
*/

/*
================
memoryFile

A file's bytes, shared by every handle opened on it.
================
*/
export function memoryFile() {
	return { bytes: new Uint8Array( 0 ) };
}

/*
================
memoryHandle

A FileSystemSyncAccessHandle over file (a fresh one when omitted). flushed
counts flush calls; closed records close(). Like the real handle, a closed
one refuses to read (InvalidStateError), so code that reads through a
handle it gave up fails here too.
================
*/
export function memoryHandle( file = memoryFile() ) {
	const open = () => {
		if ( !handle.closed ) return;
		const error = new Error( "The access handle was closed" );
		error.name = "InvalidStateError";
		throw error;
	};
	const handle = {
		flushed: 0,
		closed: false,
		bytes: () => file.bytes,
		getSize: () => file.bytes.length,
		/**
		 * @param {Uint8Array} buffer
		 * @param {{ at?: number }} [options]
		 */
		read( buffer, { at = 0 } = {} ) {
			open();
			const part = file.bytes.subarray( at, at + buffer.length );
			buffer.set( part );
			return part.length;
		},
		/**
		 * @param {Uint8Array} buffer
		 * @param {{ at?: number }} [options]
		 */
		write( buffer, { at = 0 } = {} ) {
			if ( at + buffer.length > file.bytes.length ) {
				const grown = new Uint8Array( at + buffer.length );
				grown.set( file.bytes );
				file.bytes = grown;
			}
			file.bytes.set( buffer, at );
			return buffer.length;
		},
		/** @param {number} size */
		truncate( size ) {
			const next = new Uint8Array( size );
			next.set( file.bytes.subarray( 0, size ) );
			file.bytes = next;
		},
		flush() {
			handle.flushed++;
		},
		close() {
			handle.closed = true;
		}
	};
	return handle;
}

/*
================
memoryStorage

A StorageManager with getDirectory and estimate. quota bounds estimate();
files maps every file's path ("dir/name") to its bytes. The first
lockedOpens createSyncAccessHandle calls fail as a file another worker
still holds does (NoModificationAllowedError). readOnly false is a browser
without the read-only mode: such an open fails the same way, as the second
read-write open of a file would.
================
*/
export function memoryStorage( { quota = 1 << 30, lockedOpens = 0, readOnly = true } = {} ) {
	let locked = lockedOpens;
	/** @type {Map<string, ReturnType<typeof memoryFile>>} */
	const files = new Map();
	/** @param {string} path */
	const directory = path => ({
		/** @param {string} name */
		getDirectoryHandle: async name => directory( path + name + "/" ),
		/** @param {string} name */
		removeEntry: async name => {
			if ( !files.delete( path + name ) ) {
				const error = new Error( "A requested file or directory could not be found" );
				error.name = "NotFoundError";
				throw error;
			}
		},
		/** @param {string} name */
		getFileHandle: async name => {
			const key = path + name;
			if ( !files.has( key ) ) files.set( key, memoryFile() );
			return {
				/** @param {{ mode?: string }} [options] */
				createSyncAccessHandle: async options => {
					const refused = !readOnly && options?.mode === "read-only";
					if ( locked > 0 || refused ) {
						if ( !refused ) locked--;
						const error = new Error(
							"Access Handles cannot be created if there is another open Access Handle"
						);
						error.name = "NoModificationAllowedError";
						throw error;
					}
					return memoryHandle( files.get( key ) );
				}
			};
		}
	});
	return {
		files,
		getDirectory: async () => directory( "" ),
		estimate: async () => {
			let usage = 0;
			for ( const file of files.values() ) usage += file.bytes.length;
			return { quota, usage };
		}
	};
}
