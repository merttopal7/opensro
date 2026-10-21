/*
===========================================================================

unboundCharacterVertices.test.mjs - preserve native unbound mesh vertices

===========================================================================
*/
import assert from "node:assert/strict";
import { test } from "node:test";
import { avatarToGlb } from "../../build/char/exportGlb.mjs";

test("mixed native meshes bind unbound vertices to model space without changing animated bones", () => {
	const glb = avatarToGlb( {
		name: "mixed-native-bindings",
		skeleton: {
			bones: [ { name: "Head", parentIndex: -1, local: { q: [ 0, 0, 0, 1 ], t: [ 0, 10, 0 ] } } ],
			byName: new Map( [ [ "Head", 0 ] ] )
		},
		clips: [],
		materials: new Map( [ [ "default", {
			colors: { diffuse: [ 1, 1, 1, 1 ], ambient: [ 1, 1, 1, 1 ] },
			flags: 0
		} ] ] ),
		parts: [ {
			localToGlobal: [ 0 ],
			mesh: {
				vertexCount: 3,
				triangleCount: 1,
				positions: new Float32Array( [ 0, 0, 0, 1, 0, 0, 0, 1, 0 ] ),
				normals: new Float32Array( [ 0, 0, 1, 0, 0, 1, 0, 0, 1 ] ),
				uvs: new Float32Array( 6 ),
				boneIndices: new Uint16Array( [ 0, 0, 255, 255, 0, 0 ] ),
				boneWeights: new Float32Array( [ 1, 0, 0, 0, 0.5, 0.5 ] ),
				indices: new Uint16Array( [ 0, 1, 2 ] )
			}
		} ]
	} );
	const jsonLength = glb.readUInt32LE( 12 );
	const json = JSON.parse( glb.subarray( 20, 20 + jsonLength ).toString( "utf8" ) );
	const bin = glb.subarray( 28 + jsonLength );
	const attributes = json.meshes[0].primitives[0].attributes;
	const weightView = json.bufferViews[json.accessors[attributes.WEIGHTS_0].bufferView];
	const jointView = json.bufferViews[json.accessors[attributes.JOINTS_0].bufferView];
	assert.equal( bin.readFloatLE( weightView.byteOffset + 16 ), 1 );
	assert.equal( bin.readUInt16LE( jointView.byteOffset + 8 ), 1 );
	const identity = json.nodes[json.skins[0].joints[1]];
	assert.deepEqual( identity.translation, [ 0, 0, 0 ] );
	assert.deepEqual( identity.rotation, [ 0, 0, 0, 1 ] );
	assert.deepEqual( json.nodes[0].translation, [ 0, 10, 0 ] );
	assert.ok( json.scenes[0].nodes.includes( json.skins[0].skeleton ) );
	assert.deepEqual( json.nodes[json.skins[0].skeleton].children, json.skins[0].joints );
});
