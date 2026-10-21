/*
===========================================================================

world-material.test.mjs - tests for world-material.ts

Loads the TypeScript sources directly through the shared native loader
(tests/helpers/native-source-loader.mjs), so the tests exercise the same
modules the client ships, not a per-test bundle.

===========================================================================
*/
import "../helpers/native-source-loader.mjs";
import { pathToFileURL as sourceFileUrl } from "node:url";
import { test } from "node:test";
import assert from "node:assert/strict";
import { avatarToGlb } from "../../../../scripts/build/char/exportGlb.mjs";

const { worldObjectMaterial } = await import(
	sourceFileUrl( "src/engine/foundation/rendering/world-material.ts" ).href
);
const { copyMaterial } = await import( sourceFileUrl( "src/engine/foundation/rendering/geometry.ts" ).href );
const colors = { diffuse: [ .5, .6, .7, 1 ], ambient: [ .1, .2, .3, 1 ] };
test("untextured published BMT materials pass renderer admission with their authored color", () => {
	for ( const texturePublicPath of [ null, undefined ] ) {
		const material = copyMaterial( worldObjectMaterial( { flags: 64, colors, texturePublicPath } ) );
		assert.equal( material.texture, undefined );
		assert.deepEqual( material.color, colors.diffuse );
		assert.equal( material.blend, false );
	}
});
test("native BMT material branches keep cutout, opaque, culling and no-light distinct", () => {
	for (
		const [flags, cutoff, sided, unlit] of [
			[ 0x341, 128 / 255, true, false ],
			[ 0x140, 0, false, false ],
			[ 0x20000341, 0, true, false ],
			[ 0x10000341, 0, false, false ],
			[ 0x349, 128 / 255, true, true ]
		]
	) {
		const m = worldObjectMaterial( { flags, colors, texturePublicPath: "/assets/leaf.png" } );
		assert.equal( m.alphaCutoff, cutoff );
		assert.equal( m.doubleSided, sided );
		assert.equal( m.unlit, unlit );
		assert.equal( m.stageFactor, unlit ? 1 : 2 );
		assert.equal( m.blend, false );
		assert.equal( m.objectLight, .6 );
		assert.equal( m.ambient, undefined );
		assert.equal( m.texture, "/assets/leaf.png" );
	}
	assert.equal( worldObjectMaterial( { flags: 0x40000341, colors } ).fadeAlphaOnly, true );
});
test("GLB publication uses draw-time alpha reference regardless of the serialized BMT float", () => {
	const mesh = {
		rigid: true,
		materialName: "leaf",
		vertexCount: 3,
		triangleCount: 1,
		positions: [ 0, 0, 0, 1, 0, 0, 0, 1, 0 ],
		normals: [ 0, 0, 1, 0, 0, 1, 0, 0, 1 ],
		uvs: [ 0, 0, 1, 0, 0, 1 ],
		indices: [ 0, 1, 2 ]
	};
	for ( const flags of [ 0x341, 0x140, 0x20000341, 0x10000341 ] ) {
		for ( const alphaRef of [ 0, .5, 128, 255 ] ) {
			const b = avatarToGlb( {
				name: "leaf",
				parts: [ { mesh } ],
				skeleton: { bones: [] },
				clips: [],
				materials: new Map( [ [ "leaf", { flags, alphaRef, colors } ] ] )
			} );
			const material = JSON.parse( b.subarray( 20, 20 + b.readUInt32LE( 12 ) ) ).materials[0];
			if ( flags === 0x341 ) {
				assert.equal( material.alphaMode, "MASK" );
				assert.equal( material.alphaCutoff, 128 / 255 );
			} else assert.equal( material.alphaMode, undefined );
		}
	}
});

test("regrouped GLB materials retain native BMT indices and set provenance", () => {
	const part = ( name ) => ({
		mesh: {
			rigid: true,
			materialName: name,
			vertexCount: 3,
			triangleCount: 1,
			positions: [ 0, 0, 0, 1, 0, 0, 0, 1, 0 ],
			normals: [ 0, 0, 1, 0, 0, 1, 0, 0, 1 ],
			uvs: [ 0, 0, 1, 0, 0, 1 ],
			indices: [ 0, 1, 2 ]
		}
	});
	const materials = new Map( [ [ "second", {
		flags: 0,
		colors,
		materialIndex: 9,
		materialSetPath: "prim/mtrl/native.bmt"
	} ], [ "first", { flags: 0, colors, materialIndex: 2, materialSetPath: "prim/mtrl/native.bmt" } ] ] );
	const bytes = avatarToGlb( {
		name: "indexed",
		parts: [ part( "second" ), part( "first" ) ],
		skeleton: { bones: [] },
		clips: [],
		materials
	} );
	const json = JSON.parse( bytes.subarray( 20, 20 + bytes.readUInt32LE( 12 ) ) );
	assert.deepEqual( json.materials.map( m => m.extras.sroMaterialIndex ), [ 9, 2 ] );
	assert.ok( json.materials.every( m => m.extras.sroMaterialSet === "prim/mtrl/native.bmt" ) );
});
