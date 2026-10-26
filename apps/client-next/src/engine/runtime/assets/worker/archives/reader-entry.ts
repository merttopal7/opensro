/*
===========================================================================

reader-entry.ts - entry of an archive reader worker (reader.ts)

===========================================================================
*/

import { createArchiveReader } from "./reader";
import type { ReaderReply, ReaderRequest } from "./internal/reader-contract";

const port = globalThis as unknown as {
	onmessage: (( event: MessageEvent<ReaderRequest> ) => void) | null;
	postMessage( message: ReaderReply, transfer: Transferable[] ): void;
};
const reader = createArchiveReader( function sendToArchives( reply, transfer ) {
	port.postMessage( reply, transfer );
} );
port.onmessage = event => reader.receive( event.data );
