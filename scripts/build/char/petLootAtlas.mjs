/*
===========================================================================
petLootAtlas.mjs - original brass, leather, silk and turquoise texture recipe

Port-only caravan collar atlas, generated without external image inputs.
===========================================================================
*/
import { crc32, deflateSync } from "node:zlib";

/*
================
petLootAtlas
================
*/
export function petLootAtlas() {
	const width = 768, height = 512;
	const palette = [ [ 45, 76, 71 ], [ 167, 127, 58 ], [ 46, 141, 139 ], [ 55, 36, 24 ], [ 136, 98, 42 ], [
		126,
		37,
		29
	] ];
	const raw = Buffer.alloc( height * (1 + width * 4) );
	for ( let y = 0; y < height; y++ ) {
		for ( let x = 0; x < width; x++ ) {
			const color = palette[Math.floor( y / 256 ) * 3 + Math.floor( x / 256 )];
			const grain = ((x * 17 + y * 31) % 13 - 6) * 0.8;
			const seam = x % 256 < 8 || y % 256 < 8 ? -25 : 0;
			const motif = Math.abs( (x % 64) - 32 ) + Math.abs( (y % 64) - 32 );
			const emboss = motif > 26 && motif < 31 ? 25 : 0;
			const at = y * (1 + width * 4) + 1 + x * 4;
			for ( let c = 0; c < 3; c++ ) {
				raw[at + c] = Math.max( 0, Math.min( 255, color[c] + grain + seam + emboss ) );
			}
			raw[at + 3] = 255;
		}
	}
	/*
	================
	chunk
	================
	*/
	function chunk( type, bytes ) {
		const tag = Buffer.from( type );
		const size = Buffer.alloc( 4 ), checksum = Buffer.alloc( 4 );
		size.writeUInt32BE( bytes.length );
		checksum.writeUInt32BE( crc32( Buffer.concat( [ tag, bytes ] ) ) );
		return Buffer.concat( [ size, tag, bytes, checksum ] );
	}
	const header = Buffer.alloc( 13 );
	header.writeUInt32BE( width );
	header.writeUInt32BE( height, 4 );
	header[8] = 8;
	header[9] = 6;
	return Buffer.concat( [
		Buffer.from( "89504e470d0a1a0a", "hex" ),
		chunk( "IHDR", header ),
		chunk( "IDAT", deflateSync( raw ) ),
		chunk( "IEND", Buffer.alloc( 0 ) )
	] );
}
