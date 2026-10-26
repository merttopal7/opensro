/*
===========================================================================

reader-contract.ts - messages between the archives and their reader workers

archives.ts (the owner) starts a few reader workers once the archives are
whole (docs/PWA-OPFS-MIGRATION.md K8). Each opens every archive file with a
read-only sync access handle, which several workers may hold at once, and
reads and decrypts the byte ranges the owner works out from its tables of
contents. The owner keeps the keys and the tables; a reader knows only
files, offsets and keystream parameters.

===========================================================================
*/

/*
================
ReaderRequest
================
*/
export type ReaderRequest = {
	// Open every named file of the OPFS directory read-only.
	kind: "open";
	directory: string;
	files: string[];
} | {
	// Read length bytes of file at at, then XOR them with the ChaCha20
	// keystream of key and nonce from stream offset start (temir.ts
	// chachaXorAt): the plaintext of one member range.
	kind: "read";
	id: number;
	file: string;
	at: number;
	length: number;
	key: Uint32Array;
	nonce: Uint8Array;
	start: number;
};

/*
================
ReaderReply
================
*/
export type ReaderReply =
	| { kind: "opened"; }
	| { kind: "open-failed"; error: string; }
	| { kind: "bytes"; id: number; bytes: Uint8Array<ArrayBuffer>; }
	| { kind: "read-failed"; id: number; error: string; };
