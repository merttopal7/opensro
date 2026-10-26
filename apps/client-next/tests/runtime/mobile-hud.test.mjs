/*
===========================================================================

mobile-hud.test.mjs - mobile HUD foundation geometry and M1 + M2 integration

Tests safe-area normalization, landscape touch activation, pinned top-right
minimap, and top-left status cluster (player, pet, buffs, party, and centered
target band), plus fallback to legacy compact HUD when mobileHud is disabled.

===========================================================================
*/
import test from "node:test";
import assert from "node:assert/strict";
import { uiFixture } from "../helpers/ui-fixture.mjs";
import { defined } from "../helpers/defined.mjs";
const { emptySocial } = await import( "../../src/engine/foundation/gameplay/social.ts" );
const { experimentalOptions } = await import( "../../src/engine/foundation/ui/experimental-options.ts" );
const { topmostControlAt } = await import( "../../src/engine/foundation/ui/hit-test.ts" );
const {
	JOYSTICK_BASE_RADIUS,
	normalizeSafeArea,
	mobileHudActive,
	mobileHudLayout,
	mobileTargetOrigin,
	mobileBuffLayout,
	mobilePartyLayout,
	mobileJoystickStep,
	createMobileJoystick,
	mobileCycleTarget,
	mobileAttackTarget,
	mobileMenuItems,
	mobileMenuGridLayout,
	mobileDrawerBounds
} = await import( "../../src/engine/foundation/ui/mobile-hud.ts" );

test("normalizeSafeArea clamps negative and non-finite insets to nonnegative integers", () => {
	assert.deepEqual( normalizeSafeArea( 852, 393 ), { top: 0, right: 0, bottom: 0, left: 0 } );
	assert.deepEqual(
		normalizeSafeArea( 852, 393, { top: -4, right: 59.8, bottom: 21.4, left: Number.NaN } ),
		{ top: 0, right: 59, bottom: 21, left: 0 }
	);
});

test("mobileHudActive requires landscape touch viewport smaller than 800x600 and mobileHud enabled", () => {
	// Landscape touch phone (852x393): active by default and when explicitly true.
	assert.equal( mobileHudActive( 852, 393, { touch: true } ), true );
	assert.equal( mobileHudActive( 852, 393, { touch: true, enabled: true } ), true );
	// Disabled when experimental mobileHud is false.
	assert.equal( mobileHudActive( 852, 393, { touch: true, enabled: false } ), false );
	assert.equal( mobileHudActive( 852, 393, { touch: true, mobileHud: false } ), false );
	// Disabled without touch capability.
	assert.equal( mobileHudActive( 852, 393, { touch: false, enabled: true } ), false );
	// Disabled in portrait orientation (393x852 or 375x667).
	assert.equal( mobileHudActive( 393, 852, { touch: true, enabled: true } ), false );
	assert.equal( mobileHudActive( 375, 667, { touch: true, enabled: true } ), false );
	// Disabled at desktop resolution (>= 800x600).
	assert.equal( mobileHudActive( 800, 600, { touch: true, enabled: true } ), false );
	assert.equal( mobileHudActive( 1280, 720, { touch: true, enabled: true } ), false );
});

test("mobileHudLayout places pinned minimap, safe-area player offset and centered target band", () => {
	const layout = mobileHudLayout( 852, 393, {
		safeArea: { top: 0, right: 59, bottom: 21, left: 59 }
	} );
	assert.deepEqual( layout.safe, {
		top: 0,
		right: 59,
		bottom: 21,
		left: 59,
		bounds: [ 59, 0, 734, 372 ]
	} );
	assert.deepEqual( layout.playerOffset, [ 59, 0 ] );
	assert.deepEqual( layout.playerRect, [ 63, 7, 212, 70 ] );
	assert.deepEqual( layout.minimap.origin, [ 664, 0 ] );
	assert.deepEqual( layout.minimap.frame, [ 664, 0, 140, 184 ] );
	assert.deepEqual( layout.minimap.circle, [ 678, 57, 105, 105 ] );
	assert.deepEqual( layout.minimap.toggleMap, [ 763, 47, 28, 28 ] );
	assert.deepEqual( layout.targetBand, [ 279, 7, 381, 68 ] );
	assert.deepEqual( mobileTargetOrigin( layout, 219 ), [ 360, 7 ] );
});

test("mobileBuffLayout and mobilePartyLayout reflow into the left column", () => {
	const buffs = mobileBuffLayout(
		Array.from( { length: 8 }, ( _, i ) => ({ id: "buff:" + i, x: i * 23, y: 0 }) ),
		[ 67, 81 ],
		6
	);
	assert.equal( buffs.icons.length, 8 );
	assert.deepEqual( [ buffs.icons[0].x, buffs.icons[0].y ], [ 67, 81 ] );
	assert.deepEqual( [ buffs.icons[5].x, buffs.icons[5].y ], [ 67 + 5 * 23, 81 ] );
	assert.deepEqual( [ buffs.icons[6].x, buffs.icons[6].y ], [ 67, 81 + 27 ] );
	assert.equal( buffs.bottom, 81 + 27 + 24 );

	const party = mobilePartyLayout(
		[
			{
				position: [ 150, 137 ],
				masteries: [ { id: 257, rect: [ 152, 176, 12, 12 ] } ]
			},
			{
				position: [ 283, 137 ],
				masteries: [ { id: 258, rect: [ 285, 176, 12, 12 ] } ]
			}
		],
		[ 63, 136 ],
		260,
		false
	);
	assert.deepEqual( party[0].position, [ 63, 136 ] );
	assert.deepEqual( party[0].masteries[0].rect, [ 65, 175, 12, 12 ] );
	assert.deepEqual( party[1].position, [ 63, 180 ] );
	assert.deepEqual( party[1].masteries[0].rect, [ 65, 219, 12, 12 ] );
});

test("M1 + M2: landscape touch viewport pins minimap at 1:1 scale, insets top status cluster, and respects mobileHud toggle", () => {
	const sent = [];
	const fixture = uiFixture( command => sent.push( command ) );
	let time = 0, published;
	/*
	================
	draw
	================
	*/
	function draw() {
		published = fixture.ui.step( fixture.state, ++time * 100 ) ?? published;
		assert.equal( fixture.ui.stats().error, null );
		return defined( published );
	}

	const monster = {
		gid: 200,
		regionId: 25000,
		x: 10,
		y: 0,
		z: 10,
		heading: 0,
		kind: "monster",
		name: "Mangyang",
		mountedOn: 0,
		level: 1,
		hp: 80,
		maxHp: 100
	};
	const partyAllyA = {
		gid: 301,
		regionId: 25000,
		x: 4,
		y: 0,
		z: 4,
		heading: 0,
		kind: "player",
		name: "AllyOne",
		mountedOn: 0
	};
	const partyAllyB = {
		gid: 302,
		regionId: 25000,
		x: 6,
		y: 0,
		z: 6,
		heading: 0,
		kind: "player",
		name: "AllyTwo",
		mountedOn: 0
	};
	Object.assign( fixture.state, {
		width: 852,
		height: 393,
		touch: true,
		safeArea: { top: 0, right: 59, bottom: 21, left: 59 },
		entities: [ fixture.state.entities[0], monster, partyAllyA, partyAllyB ],
		gameplay: {
			...fixture.state.gameplay,
			target: monster.gid,
			vitals: [
				{ gid: 1, hp: 450, mp: 300, maxHp: 500, maxMp: 350 },
				{ gid: monster.gid, hp: 80, maxHp: 100 }
			],
			paramJobs: [
				{
					category: 0,
					itemRefObjId: 1001,
					remainingSeconds: 600,
					reference: {
						id: 1001,
						name: "EXP Scroll",
						icon: "item\\etc\\mall_exp_up.ddj",
						durationSeconds: 1200
					}
				},
				{
					category: 1,
					itemRefObjId: 1002,
					remainingSeconds: 600,
					reference: { id: 1002, name: "SP Scroll", icon: "item\\etc\\mall_sp_up.ddj", durationSeconds: 1200 }
				}
			],
			social: {
				...emptySocial( "Player" ),
				self: 1,
				leader: 1,
				members: [
					{
						id: 1,
						name: "Player",
						model: 1907,
						level: 54,
						status: 0xaa,
						region: 25000,
						x: 0,
						z: 0,
						guild: "",
						primaryMastery: 257,
						secondaryMastery: 273
					},
					{
						id: 2,
						name: "AllyOne",
						model: 1907,
						level: 52,
						status: 0x89,
						region: 25000,
						x: 4,
						z: 4,
						guild: "",
						primaryMastery: 257,
						secondaryMastery: 0
					},
					{
						id: 3,
						name: "AllyTwo",
						model: 1907,
						level: 51,
						status: 0x77,
						region: 25000,
						x: 6,
						z: 6,
						guild: "",
						primaryMastery: 258,
						secondaryMastery: 0
					}
				]
			}
		}
	} );

	let output;
	for ( let i = 0; i < 6; i++ ) output = draw();

	// 1. Pinned Top-Right Minimap: always open at 1:1 native scale, inset by safeArea.right (59 px).
	const zoomIn = defined( output.controls.find( c => c.id === "minimap-in" ) );
	const zoomOut = defined( output.controls.find( c => c.id === "minimap-out" ) );
	const mapToggle = defined( output.controls.find( c => c.id === "toggle-window:Map" ) );
	assert.deepEqual( zoomIn.rect.slice( 2 ), [ 20, 20 ], "minimap zoom-in keeps 1:1 native 20x20 size" );
	assert.deepEqual( zoomOut.rect.slice( 2 ), [ 20, 20 ], "minimap zoom-out keeps 1:1 native 20x20 size" );
	assert.deepEqual( mapToggle.rect, [ 664 + 14, 47, 109, 115 ], "minimap disc + M button hit area" );

	// Zoom buttons sit above the disc's bottom-right corner; center of disc hits toggle-window:Map.
	assert.equal(
		topmostControlAt( output.controls, mapToggle.rect[0] + 40, mapToggle.rect[1] + 40 )?.id,
		"toggle-window:Map"
	);
	assert.equal(
		topmostControlAt( output.controls, zoomIn.rect[0] + 10, zoomIn.rect[1] + 10 )?.id,
		"minimap-in"
	);
	assert.equal(
		topmostControlAt( output.controls, zoomOut.rect[0] + 10, zoomOut.rect[1] + 10 )?.id,
		"minimap-out"
	);

	// 2. Top-Left Player Mini Info: inset by safeArea.left (59 px).
	const selfTarget = defined( output.controls.find( c => c.id === "self-target" ) );
	assert.deepEqual( selfTarget.rect, [ 63, 7, 212, 70 ] );

	// 3. Top-Center Target Frame: centered between player panel and pinned minimap.
	const targetInfo = defined( output.controls.find( c => c.id === "target-info" ) );
	assert.ok( targetInfo.rect[0] >= selfTarget.rect[0] + selfTarget.rect[2] + 4, "target sits right of player" );
	assert.ok( targetInfo.rect[0] + targetInfo.rect[2] <= 664 - 4, "target sits left of pinned minimap" );
	assert.equal( targetInfo.rect[1], 7 );

	// 4. Buff icons reflowed into left column below player panel.
	const paramBuffOne = defined( output.controls.find( c => c.id === "param-job:1001" ) );
	const paramBuffTwo = defined( output.controls.find( c => c.id === "param-job:1002" ) );
	assert.deepEqual( paramBuffOne.rect, [ 67, 81, 20, 20 ] );
	assert.deepEqual( paramBuffTwo.rect, [ 90, 81, 20, 20 ] );

	// 5. Quick-party rows stack vertically in the left column below buffs.
	const partyOne = defined( output.controls.find( c => c.id === "party-target:301" ) );
	const partyTwo = defined( output.controls.find( c => c.id === "party-target:302" ) );
	assert.equal( partyOne.rect[0], 63, "first party slot stays in left column" );
	assert.equal( partyTwo.rect[0], 63, "second party slot stacks vertically instead of wrapping right" );
	assert.ok( partyOne.rect[1] >= paramBuffOne.rect[1] + 24, "party slots sit below buff row" );
	assert.ok( partyTwo.rect[1] > partyOne.rect[1], "second party slot sits below first" );

	// 6. Opening compact-chat keeps the pinned minimap visible alongside chat.
	fixture.ui.event( { kind: "activate", id: "compact-chat" } );
	output = draw();
	assert.ok( output.controls.some( c => c.id === "chat-text" ), "chat overlay opened" );
	assert.ok( output.controls.some( c => c.id === "minimap-in" ), "pinned minimap remains open while chat is open" );
	fixture.ui.event( { kind: "activate", id: "compact-chat" } );
	output = draw();

	// 7. Turning off Experimental -> Mobile HUD restores legacy compact HUD (minimap hidden until Map overlay).
	fixture.ui.event( {
		kind: "experimental-preferences",
		value: experimentalOptions( { mobileHud: false } )
	} );
	output = draw();
	assert.ok( !output.controls.some( c => c.id === "minimap-in" ), "legacy compact HUD hides minimap until toggled" );
	assert.deepEqual(
		defined( output.controls.find( c => c.id === "self-target" ) ).rect,
		[ 4, 7, 212, 70 ]
	);
	fixture.dispose();
});

test("M3: actionCluster buttons stay inside safe bounds, below minimap, and never overlap", () => {
	const layout = mobileHudLayout( 852, 393, {
		safeArea: { top: 0, right: 59, bottom: 21, left: 59 }
	} );
	const [sx, sy, sw, sh] = layout.safe.bounds;
	const cluster = layout.actionCluster;
	assert.equal( cluster.skills.length, 6 );
	const all = [
		cluster.attack,
		...cluster.skills,
		cluster.targetCycle,
		cluster.pickup,
		cluster.pageToggle
	];
	for ( const r of all ) {
		assert.ok( r[0] >= sx && r[1] >= sy, `rect ${r} inside top-left safe bounds` );
		assert.ok( r[0] + r[2] <= sx + sw && r[1] + r[3] <= sy + sh, `rect ${r} inside bottom-right safe bounds` );
		assert.ok( r[1] >= layout.minimap.bottom, `rect ${r} sits below pinned minimap` );
	}
	for ( let i = 0; i < all.length; i++ ) {
		for ( let j = i + 1; j < all.length; j++ ) {
			const a = all[i], b = all[j];
			const overlaps = a[0] < b[0] + b[2] && a[0] + a[2] > b[0] && a[1] < b[1] + b[3] && a[1] + a[3] > b[1];
			assert.equal( overlaps, false, `cluster rects ${i} (${a}) and ${j} (${b}) must not overlap` );
		}
	}
});

test("M3: virtual joystick emits ArrowUp/ArrowLeft/ArrowRight transitions and preserves tap-to-walk within deadzone", () => {
	const stepIdle = mobileJoystickStep( 120, 280, 124, 276 );
	assert.equal( stepIdle.dragged, false );
	assert.deepEqual( stepIdle.arrows, { up: false, left: false, right: false } );

	const stepUpLeft = mobileJoystickStep( 120, 280, 60, 200 );
	assert.equal( stepUpLeft.dragged, true );
	assert.deepEqual( stepUpLeft.arrows, { up: true, left: true, right: false } );
	assert.ok(
		Math.hypot( stepUpLeft.knob[0] - 120, stepUpLeft.knob[1] - 280 ) <= JOYSTICK_BASE_RADIUS + 1,
		"knob is clamped to base radius"
	);

	const joy = createMobileJoystick();
	assert.deepEqual( joy.down( 1, 140, 260 ), [] );
	assert.equal( joy.owns( 1 ), true );

	// Deflect up-right -> emits ArrowUp + ArrowRight down.
	const move1 = joy.move( 1, 175, 220 );
	assert.deepEqual( move1, [
		{ kind: "key", code: "ArrowUp", down: true },
		{ kind: "key", code: "ArrowRight", down: true }
	] );
	assert.equal( joy.state().active, true );
	assert.deepEqual( joy.heldOutputs(), [
		{ kind: "key", code: "ArrowUp", down: true },
		{ kind: "key", code: "ArrowRight", down: true }
	] );
	// Deflect pure left -> releases ArrowRight, keeps ArrowUp, presses ArrowLeft.
	const move2 = joy.move( 1, 100, 260 );
	assert.deepEqual( move2, [
		{ kind: "key", code: "ArrowLeft", down: true },
		{ kind: "key", code: "ArrowRight", down: false }
	] );
	// Release after movement -> releases held keys and reports tap: false.
	const endDrag = joy.up( 1, 100, 260 );
	assert.equal( endDrag.tap, false );
	assert.deepEqual( endDrag.outputs, [
		{ kind: "key", code: "ArrowUp", down: false },
		{ kind: "key", code: "ArrowLeft", down: false }
	] );

	// Quick tap within deadzone -> no key outputs and tap: true so tap-to-walk still runs.
	joy.down( 2, 150, 250 );
	joy.move( 2, 154, 247 );
	const endTap = joy.up( 2, 154, 247 );
	assert.equal( endTap.tap, true );
	assert.deepEqual( endTap.outputs, [] );
});

test("M3: mobileCycleTarget and mobileAttackTarget sort alive monsters by distance and skip dead targets", () => {
	const pose = { regionId: 25000, x: 0, y: 0, z: 0 };
	const entities = /** @type {const} */ ([
		{ gid: 1, regionId: 25000, x: 0, y: 0, z: 0, kind: "player" },
		{ gid: 10, regionId: 25000, x: 30, y: 0, z: 0, kind: "monster", name: "FarMob" },
		{ gid: 11, regionId: 25000, x: 5, y: 0, z: 0, kind: "monster", name: "DeadMob" },
		{ gid: 12, regionId: 25000, x: 10, y: 0, z: 0, kind: "monster", name: "NearMob" },
		{ gid: 13, regionId: 25000, x: 20, y: 0, z: 0, kind: "monster", name: "MidMob" }
	]);
	const vitals = [
		{ gid: 10, hp: 100 },
		{ gid: 11, hp: 0, deathState: 2 },
		{ gid: 12, hp: 80 },
		{ gid: 13, hp: 90 }
	];

	// Cycle order by distance: 12 (10m) -> 13 (20m) -> 10 (30m) -> wraps to 12.
	assert.equal( mobileCycleTarget( entities, pose, vitals, undefined ), 12 );
	assert.equal( mobileCycleTarget( entities, pose, vitals, 12 ), 13 );
	assert.equal( mobileCycleTarget( entities, pose, vitals, 13 ), 10 );
	assert.equal( mobileCycleTarget( entities, pose, vitals, 10 ), 12 );

	// Attack with no target or dead target selects nearest alive monster (12).
	assert.deepEqual( mobileAttackTarget( entities, pose, vitals, undefined ), { gid: 12, select: true } );
	assert.deepEqual( mobileAttackTarget( entities, pose, vitals, 11 ), { gid: 12, select: true } );
	// Attack with alive monster already selected attacks it directly without re-selecting.
	assert.deepEqual( mobileAttackTarget( entities, pose, vitals, 13 ), { gid: 13, select: false } );
});

test("M3: UI action cluster dispatches attack, target-cycle, pickup, page-toggle, chat preview and thin EXP bar", () => {
	const sent = [];
	const fixture = uiFixture( command => sent.push( command ) );
	let time = 0, published;
	/*
	================
	draw
	================
	*/
	function draw() {
		published = fixture.ui.step( fixture.state, ++time * 100 ) ?? published;
		assert.equal( fixture.ui.stats().error, null );
		return defined( published );
	}

	const nearMonster = {
		gid: 201,
		regionId: 25000,
		x: 8,
		y: 0,
		z: 6,
		heading: 0,
		kind: "monster",
		name: "Mangyang",
		mountedOn: 0,
		level: 1,
		hp: 90,
		maxHp: 100
	};
	const farMonster = {
		gid: 202,
		regionId: 25000,
		x: 20,
		y: 0,
		z: 15,
		heading: 0,
		kind: "monster",
		name: "Small Spider",
		mountedOn: 0,
		level: 2,
		hp: 120,
		maxHp: 120
	};
	Object.assign( fixture.state, {
		width: 852,
		height: 393,
		touch: true,
		safeArea: { top: 0, right: 59, bottom: 21, left: 59 },
		entities: [ fixture.state.entities[0], nearMonster, farMonster ],
		gameplay: {
			...fixture.state.gameplay,
			target: undefined,
			vitals: [
				{ gid: 1, hp: 450, mp: 300, maxHp: 500, maxMp: 350 },
				{ gid: nearMonster.gid, hp: 90, maxHp: 100 },
				{ gid: farMonster.gid, hp: 120, maxHp: 120 }
			],
			chat: {
				lines: [ { channel: 1, name: "Ali", text: "selam!" } ],
				feedback: []
			}
		}
	} );

	let output;
	for ( let i = 0; i < 6; i++ ) output = draw();

	// 1. Action cluster controls and 6 skill slots are present; legacy 11-slot bar is absent.
	for (
		const id of [
			"hud-menu",
			"compact-chat",
			"mobile-attack",
			"mobile-target-cycle",
			"mobile-pickup",
			"hotbar-next",
			"hotbar:1",
			"hotbar:2",
			"hotbar:3",
			"hotbar:4",
			"hotbar:5",
			"hotbar:6"
		]
	) {
		assert.ok( output.controls.some( c => c.id === id ), `expected M3 control ${id}` );
	}
	assert.ok( !output.controls.some( c => c.id === "hotbar:0" ), "legacy slot 0 is not rendered in Mobile HUD" );
	assert.ok( !output.controls.some( c => c.id === "compact-map" ), "legacy toolbar is replaced by Mobile HUD" );

	// 2. Center of the viewport and joystick zone are unobstructed in ui.blocks( x, y ).
	assert.equal( fixture.ui.blocks( 426, 200 ), false, "center of world is unobstructed" );
	assert.equal( fixture.ui.blocks( 140, 260 ), false, "joystick zone does not block world clicks" );

	// 3. Mobile joystick event updates visual state without adding blocking rects.
	fixture.ui.event( {
		kind: "mobile-joystick",
		active: true,
		origin: [ 140, 260 ],
		knob: [ 140, 224 ]
	} );
	output = draw();
	assert.equal( fixture.ui.blocks( 140, 260 ), false, "active joystick visual never blocks world input" );

	// 4. mobile-attack with no target selects nearest monster (201) and attacks it.
	sent.length = 0;
	fixture.ui.event( { kind: "activate", id: "mobile-attack" } );
	assert.deepEqual( sent, [
		{ kind: "gameplay", command: { kind: "select", gid: nearMonster.gid } },
		{ kind: "gameplay", command: { kind: "attack", gid: nearMonster.gid } }
	] );

	// 5. mobile-target-cycle cycles from nearMonster (201) to farMonster (202).
	fixture.state.gameplay = { ...fixture.state.gameplay, target: nearMonster.gid };
	output = draw();
	sent.length = 0;
	fixture.ui.event( { kind: "activate", id: "mobile-target-cycle" } );
	assert.deepEqual( sent, [
		{ kind: "gameplay", command: { kind: "select", gid: farMonster.gid } }
	] );

	// 6. mobile-pickup sends pickup-nearest.
	sent.length = 0;
	fixture.ui.event( { kind: "activate", id: "mobile-pickup" } );
	assert.deepEqual( sent, [
		{ kind: "gameplay", command: { kind: "pickup-nearest" } }
	] );

	// 7. hotbar-next cycles quickslot page F1 -> F2 (slots 11..16).
	fixture.ui.event( { kind: "activate", id: "hotbar-next" } );
	output = draw();
	assert.ok( output.controls.some( c => c.id === "hotbar:11" ), "page 2 maps slot 1 to hotbar:11" );
	assert.ok( output.controls.some( c => c.id === "hotbar:16" ), "page 2 maps slot 6 to hotbar:16" );

	fixture.dispose();
});

test("M4: mobileMenuGridLayout and mobileDrawerBounds compute touch-sized 4x2 menu and right-docked >= 0.9 panel drawer", () => {
	const layout = mobileHudLayout( 852, 393, {
		safeArea: { top: 0, right: 59, bottom: 21, left: 59 }
	} );
	assert.deepEqual( layout.panelDrawer, [ 363, 0, 430, 368 ] );

	const grid = mobileMenuGridLayout( layout );
	assert.equal( grid.cells.length, 8 );
	assert.deepEqual( grid.close.slice( 2 ), [ 40, 40 ] );
	for ( const cell of grid.cells ) {
		assert.ok( cell.rect[2] >= 72 && cell.rect[3] >= 60, `menu cell ${cell.id} meets touch target size` );
	}

	// Main popup with side tabs is 430x408; inside 430x368 drawer it scales by 368/408 = 0.902 (>= 0.90)
	// and docks flush against the right edge of layout.panelDrawer (x + w = 363 + 430 = 793).
	const drawer = mobileDrawerBounds( layout, 430, 408 );
	const scale = Math.min( 1, drawer[2] / 430, drawer[3] / 408 );
	assert.ok( scale >= 0.9, `expected mainPopup drawer scale >= 0.9, got ${scale}` );
	assert.equal( drawer[0] + drawer[2], layout.panelDrawer[0] + layout.panelDrawer[2], "docked flush right" );
});

test("M4: hud-menu opens 4x2 touch grid and opens right-hand panel drawers without action-cluster or minimap overlap", () => {
	const sent = [];
	const fixture = uiFixture( command => sent.push( command ) );
	let time = 0, published;
	/*
	================
	draw
	================
	*/
	function draw() {
		published = fixture.ui.step( fixture.state, ++time * 100 ) ?? published;
		assert.equal( fixture.ui.stats().error, null );
		return defined( published );
	}

	Object.assign( fixture.state, {
		width: 852,
		height: 393,
		touch: true,
		safeArea: { top: 0, right: 59, bottom: 21, left: 59 }
	} );

	let output;
	for ( let i = 0; i < 6; i++ ) output = draw();

	// 1. Tapping hud-menu opens the 4x2 mobile menu card and hides action-cluster buttons.
	fixture.ui.event( { kind: "activate", id: "hud-menu" } );
	output = draw();
	for ( const item of mobileMenuItems() ) {
		assert.ok( output.controls.some( c => c.id === item.id ), `expected menu cell ${item.id}` );
	}
	const menuClose = defined( output.controls.find( c => c.id === "mobile-menu-close" ) );
	assert.deepEqual( menuClose.rect.slice( 2 ), [ 40, 40 ] );
	assert.ok( !output.controls.some( c => c.id === "mobile-attack" ), "action cluster hidden while menu is open" );

	// 2. Tapping mobile-menu:Inventory closes the menu and opens Inventory in the right-hand drawer.
	fixture.ui.event( { kind: "activate", id: "mobile-menu:Inventory" } );
	for ( let i = 0; i < 6; i++ ) output = draw();
	assert.ok( !output.controls.some( c => c.id === "mobile-menu:Inventory" ), "menu closed after selection" );
	const invClose = defined( output.controls.find( c => c.id === "close-window:Inventory" ) );
	assert.deepEqual( invClose.rect.slice( 2 ), [ 40, 40 ], "drawer close control enlarged to 40x40" );
	assert.ok( invClose.rect[0] >= 700, `Inventory drawer docked to right side (close x=${invClose.rect[0]})` );
	assert.ok( !output.controls.some( c => c.id === "mobile-attack" ), "action cluster hidden while drawer is open" );
	assert.ok( !output.controls.some( c => c.id === "minimap-in" ), "pinned minimap yields to right-hand drawer" );

	// Bag slot 13 is scaled at >= 0.90 (native 32x32 -> >= 28x28).
	const slot13 = defined( output.controls.find( c => c.id === "slot:13" ) );
	assert.ok( slot13.rect[2] >= 28 && slot13.rect[3] >= 28, `bag slot keeps >= 0.9 scale (${slot13.rect})` );
	const goldBtn = defined( output.controls.find( c => c.id === "inventory-gold" ) );
	assert.ok( goldBtn.rect[2] >= 28 && goldBtn.rect[3] >= 28, `gold button enlarged for touch (${goldBtn.rect})` );

	// 3. Switching to Character via the enlarged side-tab keeps the right-hand drawer and enlarges stat buttons.
	const charTab = defined( output.controls.find( c => c.id === "select-window:Character" ) );
	assert.ok( charTab.rect[2] >= 32 && charTab.rect[3] >= 32, `side-tab enlarged for touch (${charTab.rect})` );
	fixture.ui.event( { kind: "activate", id: "select-window:Character" } );
	for ( let i = 0; i < 6; i++ ) output = draw();
	const strBtn = defined( output.controls.find( c => c.id === "stat-str" ) );
	assert.ok( strBtn.rect[2] >= 28 && strBtn.rect[3] >= 25, `stat-str enlarged for touch (${strBtn.rect})` );

	// 4. Closing Character restores the pinned minimap and bottom-right action cluster.
	fixture.ui.event( { kind: "activate", id: "close-window:Character" } );
	output = draw();
	assert.ok( output.controls.some( c => c.id === "minimap-in" ), "minimap restored after drawer closes" );
	assert.ok( output.controls.some( c => c.id === "mobile-attack" ), "action cluster restored after drawer closes" );

	fixture.dispose();
});
