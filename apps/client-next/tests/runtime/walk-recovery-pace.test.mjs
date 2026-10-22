/*
===========================================================================

walk-recovery-pace.test.mjs - a hitch's walking lag is repaid without a sprint

The movement owner, the sample builder and the pose owner run on a fixed
worker tick and a 60 Hz display clock, with the world journal's one batch
in flight. A main-thread hitch longer than the 100 ms presentation corridor
leaves the display a few units behind; the camera follows that display, so
repaying it at the full recovery cap read as a sprint after every hitch.

===========================================================================
*/
import "../helpers/native-source-loader.mjs";
import { test } from "node:test";
import assert from "node:assert/strict";
import { product } from "../helpers/navigation-fixture.mjs";
const { createMovement } = await import(
	"../../src/engine/runtime/simulation/worker/session/world/gameplay/movement/movement.ts"
);
const { createPosePresentation } = await import( "../../src/engine/runtime/characters/pose-presentation.ts" );
const { createPresentationSamples } = await import( "../../src/engine/runtime/characters/presentation-samples.ts" );
const GID = 7;
const TICK_MS = 16;
const FRAME_MS = 1000 / 60;
const RUN_SPEED = 50;
const FROM = { regionId: 257, x: 100, y: 0, z: 100, angle: 0 };

/*
================
walkThroughHitch

Walks along +x and returns the drawn x speed of every frame after a display
hitch, with the drawn and logical x at the end. The worker publishes a batch
only after the previous one was consumed by a frame, as the journal does.
================
*/
function walkThroughHitch( hitch ) {
	const movement = createMovement( () => {} ), navigation = product(), presentation = createPosePresentation();
	navigation.objects = [];
	movement.seed( FROM );
	movement.navigation( 257, navigation );
	movement.request( { ...FROM, x: 1100 }, 0 );
	presentation.origin( 0 );
	let posted = null, inflight = false, shown = null, frameAt = 0, previous = null, drawn = null;
	const speeds = [];
	for ( let tick = 0; tick <= hitch.at + 1500; tick += TICK_MS ) {
		while ( frameAt < tick ) {
			if ( posted ) {
				// The local player's movement state is the gameplay slice the sample builder reads.
				const gameplay = /** @type {import("../../src/engine/contracts/gameplay.ts").GameplayState} */ (
					/** @type {unknown} */ ({ ...posted, localGid: GID })
				);
				const { samples } = createPresentationSamples( [], gameplay );
				presentation.samples( samples );
				shown = posted;
				posted = null;
				inflight = false;
			}
			if ( shown?.pose ) {
				drawn = presentation.pose( GID, shown.pose, frameAt / 1000 );
				if ( previous && frameAt > hitch.at + hitch.ms ) {
					speeds.push( (drawn.x - previous.x) / (frameAt - previous.at) * 1000 );
				}
				previous = { x: drawn.x, at: frameAt };
			}
			frameAt += FRAME_MS;
			if ( frameAt >= hitch.at && frameAt < hitch.at + FRAME_MS ) frameAt += hitch.ms;
		}
		movement.step( tick );
		if ( !inflight ) {
			posted = structuredClone( movement.state() );
			inflight = true;
		}
	}
	const logical = movement.state().pose;
	assert.ok( drawn && logical );
	return { speeds, drawn: drawn.x, logical: logical.x };
}

test("a display hitch beyond the presentation corridor is repaid without a sprint", () => {
	const { speeds, drawn, logical } = walkThroughHitch( { at: 400, ms: 110 } );
	assert.ok( speeds.length > 60 );
	const fastest = Math.max( ...speeds );
	assert.ok( fastest > RUN_SPEED * 1.05, `the hitch's lag is repaid at all: ${fastest}` );
	assert.ok( fastest < RUN_SPEED * 1.35, `recovery after a small hitch stays near the run speed: ${fastest}` );
	assert.ok( Math.min( ...speeds.slice( 1 ) ) >= 0, "recovery never walks the display backwards" );
	assert.ok( logical - drawn < 1, `the lag is repaid before the walk goes on: ${logical - drawn}` );
});
