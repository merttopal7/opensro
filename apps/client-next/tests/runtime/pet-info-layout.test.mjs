/*
===========================================================================
pet-info-layout.test.mjs - training pages must not mask the native pet stats page.
===========================================================================
*/
import "../helpers/native-source-loader.mjs";
import { test } from "node:test";
import assert from "node:assert/strict";
import { uiFixture } from "../helpers/ui-fixture.mjs";
const { experimentalOptions } = await import( "../../src/engine/foundation/ui/experimental-options.ts" );

test("trained attack pets retain HP labels and hide pickup-only info", t => {
	const f = uiFixture();
	t.after( () => f.dispose() );
	Object.assign( f.state.gameplay, {
		cosRecords: [ { gid: 7, refObjId: 100, band: 3, hp: 100, mp: 0, status: 0, dead: false } ]
	} );
	f.ui.step( f.state, 0 );
	f.ui.event( { kind: "experimental-preferences", value: experimentalOptions( { attackPetTraining: true } ) } );
	f.ui.event( { kind: "activate", id: "open-window:COS inventory" } );
	f.ui.step( f.state, 0 );
	f.ui.event( { kind: "activate", id: "cos-tab:0" } );
	for ( let i = 1; i <= 4; i++ ) f.ui.step( f.state, i * 100 );
	assert.equal( f.ui.stats().error, null );
	assert.ok( f.hasText( "HP" ), "attack pet HP label must remain visible" );
	assert.ok( !f.hasText( "Rental period" ), "pickup pet rental label must stay hidden" );
	assert.ok( !f.hasText( "<Stat does not exist>" ), "attack pet stats must not show the no-ability placeholder" );
});

test("pet mini shield stays inside the native three-pixel HP fill", t => {
	const f = uiFixture();
	t.after( () => f.dispose() );
	Object.assign( f.state.gameplay, {
		cosRecords: [ { gid: 7, refObjId: 100, band: 3, hp: 100, mp: 0, status: 0, dead: false } ]
	} );
	Object.assign( f.state, {
		simulationTimeMs: 0,
		entities: [ ...f.state.entities, {
			...f.state.entities[0],
			gid: 7,
			shield: { amount: 50, maxHp: 100, atMs: 0, untilMs: 5000, revision: 1, burst: false }
		} ]
	} );
	for ( let i = 0; i < 8; i++ ) f.ui.step( f.state, i * 100 );
	const fill = f.scenes.at( -1 ).quads.filter( q =>
		q.texture === "" && q.color[0] > .55 && q.color[0] < .9 && q.color[0] === q.color[1] &&
		q.color[1] === q.color[2]
	);
	assert.ok(
		fill.some( q => q.clip[2] === 107 && q.clip[3] === 3 ),
		"gray fill must exclude transparent texture padding and the black frame"
	);
	assert.equal( f.ui.stats().error, null );
});
