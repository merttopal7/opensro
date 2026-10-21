/*
===========================================================================

alexandriaOverlay.test.mjs - the server projection's Alexandria overlay

Port-only, not native. A variant manifest projects the regions, teleport
codes and gate buildings the server hides while SRO_ALEXANDRIA is off; a
native game root projects nothing. Synthetic manifests; no game data is read.

===========================================================================
*/
import { test } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

import { buildAlexandriaOverlayProjection } from "../../build/server/buildServerGameDataBundle.mjs";

/*
================
withBundle

A temporary bundle root and a variant manifest path beside it.
================
*/
async function withBundle( manifest, check ) {
	const root = fs.mkdtempSync( path.join( os.tmpdir(), "alexandria-overlay-" ) );
	try {
		const variant = path.join( root, "variant.json" );
		if ( manifest ) fs.writeFileSync( variant, JSON.stringify( manifest ) );
		await check( path.join( root, "bundle" ), variant );
	} finally {
		fs.rmSync( root, { recursive: true, force: true } );
	}
}

test("a native game root adds no file to the projection", async () => {
	await withBundle( null, async ( bundle, variant ) => {
		await buildAlexandriaOverlayProjection( bundle, variant );
		assert.equal( fs.existsSync( bundle ), false );
	} );
});

test("a variant projects sorted region ids, teleport codes and gate buildings", async () => {
	const manifest = {
		format: "opensro-alexandria-variant-v1",
		regions: [ { sectorX: 49, sectorY: 90 }, { sectorX: 48, sectorY: 90 }, { sectorX: 48, sectorY: 90 } ],
		gates: { gates: [ "GATE_SD2", "GATE_SD1" ], buildingCodes: [ "STORE_SD_GATE2", "STORE_SD_GATE1" ] }
	};
	await withBundle( manifest, async ( bundle, variant ) => {
		await buildAlexandriaOverlayProjection( bundle, variant );
		const overlay = JSON.parse(
			fs.readFileSync( path.join( bundle, "textdata", "alexandria-overlay.json" ), "utf8" )
		);
		assert.deepEqual( overlay, {
			format: "sro-server-alexandria-overlay",
			version: 1,
			regions: [ (90 << 8) | 48, (90 << 8) | 49 ],
			teleports: [ "GATE_SD1", "GATE_SD2" ],
			buildings: [ "STORE_SD_GATE1", "STORE_SD_GATE2" ]
		} );
	} );
});

test("a variant manifest without its gate buildings is refused", async () => {
	const manifest = {
		format: "opensro-alexandria-variant-v1",
		regions: [ { sectorX: 48, sectorY: 90 } ],
		gates: { gates: [ "GATE_SD1" ] }
	};
	await withBundle( manifest, async ( bundle, variant ) => {
		await assert.rejects( buildAlexandriaOverlayProjection( bundle, variant ), /buildingCodes/ );
	} );
});
