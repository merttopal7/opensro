/*
===========================================================================

opfs-sync-access.d.ts - the Origin Private File System's synchronous handle

FileSystemSyncAccessHandle exists only in dedicated workers, so TypeScript
declares it in lib.webworker, not in the DOM library this project compiles
against. The installed PWA keeps its .temir archives in OPFS and reads and
writes them through it (docs/PWA-OPFS-MIGRATION.md); these declarations
copy lib.webworker's, plus the access mode Chromium 121+ accepts (several
"read-only" handles may share one file, which the parallel readers use).

===========================================================================
*/

interface FileSystemReadWriteOptions {
	at?: number;
}

interface FileSystemSyncAccessHandle {
	close(): void;
	flush(): void;
	getSize(): number;
	read( buffer: AllowSharedBufferSource, options?: FileSystemReadWriteOptions ): number;
	truncate( newSize: number ): void;
	write( buffer: AllowSharedBufferSource, options?: FileSystemReadWriteOptions ): number;
}

interface FileSystemCreateSyncAccessHandleOptions {
	mode?: "readwrite" | "read-only" | "readwrite-unsafe";
}

interface FileSystemFileHandle {
	createSyncAccessHandle( options?: FileSystemCreateSyncAccessHandleOptions ): Promise<FileSystemSyncAccessHandle>;
}
