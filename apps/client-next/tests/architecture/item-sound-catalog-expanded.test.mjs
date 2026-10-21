/*
===========================================================================

item-sound-catalog-expanded.test.mjs - the item sound catalog gate on native and expanded builds

A native build must match the checked-in catalog byte for byte. An expanded
build (an Alexandria variant) adds rules for its imported items; there the
gate only requires every native row to survive in place.

===========================================================================
*/
import { test } from "node:test";
import assert from "node:assert/strict";
import { itemSoundCatalogCurrent, itemSoundSource } from "../../tools/generate-item-sounds.mjs";

function rule( event1, publicPath, handle = "SND_EQUIP" ) {
	return { object: "ITEM", handle, skillId: "-", event1, event2: "-", event3: "-", publicPath, volume: 1 };
}
const bytes = ( rules ) => Buffer.from( JSON.stringify( { rules } ) );
const NATIVE = [
	rule( "SWORD", "/assets/audio/a.mp3" ),
	rule( "SWORD", "/assets/audio/b.mp3" ),
	rule( "BOW", "/assets/audio/c.mp3" )
];
const checkedIn = itemSoundSource( bytes( NATIVE ) );

test("a native build must match the checked-in catalog exactly", () => {
	assert.equal( itemSoundCatalogCurrent( checkedIn, bytes( NATIVE ), false ), true );
	assert.equal(
		itemSoundCatalogCurrent( checkedIn, bytes( [ ...NATIVE, rule( "AXE", "/assets/audio/d.mp3" ) ] ), false ),
		false
	);
});

test("an expanded build may add rules and new keys but never change a native row", () => {
	const added = [ ...NATIVE, rule( "SWORD", "/assets/audio/x.mp3" ), rule( "AXE", "/assets/audio/d.mp3" ) ];
	assert.equal( itemSoundCatalogCurrent( checkedIn, bytes( added ), true ), true );
	assert.equal(
		itemSoundCatalogCurrent( checkedIn, bytes( NATIVE.slice( 1 ) ), true ),
		false,
		"a native row removed"
	);
	assert.equal(
		itemSoundCatalogCurrent( checkedIn, bytes( [ NATIVE[1], NATIVE[0], NATIVE[2] ] ), true ),
		false,
		"a native row moved"
	);
	assert.equal(
		itemSoundCatalogCurrent( checkedIn, bytes( [ rule( "SWORD", "/assets/audio/z.mp3" ), ...NATIVE ] ), true ),
		false,
		"a row ahead of the native ones"
	);
});

test("the drop table is held to the same rule", () => {
	const drops = [ rule( "SWORD", "/assets/audio/a.mp3", "SND_DROPITEM" ) ];
	const base = itemSoundSource( bytes( [ ...NATIVE, ...drops ] ) );
	assert.equal(
		itemSoundCatalogCurrent(
			base,
			bytes( [ ...NATIVE, ...drops, rule( "SWORD", "/assets/audio/y.mp3", "SND_DROPITEM" ) ] ),
			true
		),
		true
	);
	assert.equal( itemSoundCatalogCurrent( base, bytes( NATIVE ), true ), false );
});
