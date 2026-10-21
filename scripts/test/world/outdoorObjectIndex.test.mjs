/*
===========================================================================

outdoorObjectIndex.test.mjs - source coverage of the shared outdoor object cache

Adding sectors must invalidate a valid but incomplete resource index before
a player can enter a region whose navigation references the new objects.

===========================================================================
*/
import assert from "node:assert/strict";
import { test } from "node:test";
import { outdoorObjectIndexMatchesDefinitions } from "../../build/world/buildOutdoorWorldRegionResources.mjs";

test("new sectors cannot reuse an index without their navigation objects", () => {
	const tree = { objectId: 2076, sourcePath: "res/tree.bsr", flags: 0, placementCount: 8 };
	const gate = { objectId: 2198, sourcePath: "res/gate.bsr", flags: 0, placementCount: 2 };
	const index = { bsr: [ { ...tree, objectFlags: tree.flags } ] };
	assert.equal( outdoorObjectIndexMatchesDefinitions( index, [ tree ] ), true );
	assert.equal( outdoorObjectIndexMatchesDefinitions( index, [ tree, gate ] ), false );
	index.bsr.push( { ...gate, objectFlags: gate.flags } );
	assert.equal( outdoorObjectIndexMatchesDefinitions( index, [ tree, gate ] ), true );
	// Removing the extra sectors leaves objects the build no longer uses.
	assert.equal( outdoorObjectIndexMatchesDefinitions( index, [ tree ] ), false );
});

test("catalog changes and new placements invalidate the shared object index", () => {
	const definition = { objectId: 2076, sourcePath: "res/tree.bsr", flags: 0, placementCount: 8 };
	const resource = { ...definition, objectFlags: definition.flags };
	const index = { bsr: [ resource ] };
	for ( const change of [ { sourcePath: "res/other.bsr" }, { flags: 1 }, { placementCount: 9 } ] ) {
		assert.equal( outdoorObjectIndexMatchesDefinitions( index, [ { ...definition, ...change } ] ), false );
	}
	assert.equal(
		outdoorObjectIndexMatchesDefinitions( index, [ { ...definition, sourcePath: "RES\\TREE.BSR" } ] ),
		true
	);
	assert.equal( outdoorObjectIndexMatchesDefinitions( { bsr: [ resource, resource ] }, [ definition ] ), false );
});
