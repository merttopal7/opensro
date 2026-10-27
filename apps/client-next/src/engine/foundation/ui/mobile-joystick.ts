/*
===========================================================================

mobile-joystick.ts - fixed bottom-left radial virtual joystick for Mobile HUD

Port-only, not native (docs/MOBILE-HUD.md). Owns the 360-degree camera-relative
heading math, fixed-origin thumb deflection state machine, and radial scanline
UI quad generation for the landscape mobile virtual joystick.

===========================================================================
*/

import type { UiQuad, UiRect } from "@/engine/contracts/ui";

export const JOYSTICK_BASE_RADIUS = 42;
export const JOYSTICK_KNOB_RADIUS = 18;
export const JOYSTICK_DEADZONE = 8;
const JOYSTICK_STEER_DEADZONE = 10;
const JOYSTICK_FORWARD_MAX_DY = 14;
const HEADING_WORDS = 65535;
const RADIAL_STEP = 2;
const OUTER_RING_THICKNESS = 2.5;
const INNER_RING_RADIUS = 25;
const INNER_RING_THICKNESS = 1.4;
const KNOB_RIM_THICKNESS = 2.2;
const KNOB_HIGHLIGHT_RADIUS = 7;

const ARROW_KEY_CODES = [
	[ "up", "ArrowUp" ],
	[ "left", "ArrowLeft" ],
	[ "right", "ArrowRight" ]
] as const;

/*
================
MobileJoystickArrows
================
*/
export interface MobileJoystickArrows {
	readonly up: boolean;
	readonly left: boolean;
	readonly right: boolean;
}

/*
================
MobileJoystickStep
================
*/
export interface MobileJoystickStep {
	readonly active: boolean;
	readonly dragged: boolean;
	readonly origin: readonly [number, number];
	readonly knob: readonly [number, number];
	readonly vector: readonly [number, number] | null;
	readonly arrows: MobileJoystickArrows;
}

/*
================
MobileJoystickKeyOutput
================
*/
export interface MobileJoystickKeyOutput {
	readonly kind: "key";
	readonly code: "ArrowUp" | "ArrowLeft" | "ArrowRight";
	readonly down: boolean;
}

/*
================
mobileJoystickHeading

Maps a screen-space joystick deflection (dx right, dy down) and the follow
camera's yaw (radians) to a 360-degree native Silkroad heading word (0..65534).
In world.ts (updateCamera) and world-math.ts (cameraBasis), the horizontal
camera forward vector (screen up, dy < 0) is (-sin(yaw), -cos(yaw)) and the
horizontal camera right vector (screen right, dx > 0) is (-cos(yaw), +sin(yaw)).
================
*/
export function mobileJoystickHeading( dx: number, dy: number, cameraYaw: number ): number {
	const twoPi = Math.PI * 2;
	const wx = dy * Math.sin( cameraYaw ) - dx * Math.cos( cameraYaw );
	const wz = dy * Math.cos( cameraYaw ) + dx * Math.sin( cameraYaw );
	const bearing = (Math.atan2( wz, wx ) + twoPi) % twoPi;
	return Math.trunc( bearing / twoPi * HEADING_WORDS ) % HEADING_WORDS;
}

/*
================
mobileJoystickStep

Pure deflection math for the fixed bottom-left virtual joystick.
Maps thumb displacement around the fixed joystick origin to clamped knob
coordinates, a continuous 2D deflection vector for 360-degree camera-relative
movement, and legacy arrow transitions.
================
*/
export function mobileJoystickStep(
	originX: number,
	originY: number,
	currentX: number,
	currentY: number
): MobileJoystickStep {
	const ox = Math.round( originX );
	const oy = Math.round( originY );
	const dx = currentX - originX;
	const dy = currentY - originY;
	const dist = Math.hypot( dx, dy );
	if ( dist <= JOYSTICK_DEADZONE ) {
		return {
			active: true,
			dragged: false,
			origin: [ ox, oy ],
			knob: [ ox, oy ],
			vector: null,
			arrows: { up: false, left: false, right: false }
		};
	}
	const scale = dist > JOYSTICK_BASE_RADIUS ? JOYSTICK_BASE_RADIUS / dist : 1;
	const clampedDx = dx * scale;
	const clampedDy = dy * scale;
	const knob: readonly [number, number] = [
		Math.round( originX + clampedDx ),
		Math.round( originY + clampedDy )
	];
	const up = dy <= JOYSTICK_FORWARD_MAX_DY;
	const left = dx < -JOYSTICK_STEER_DEADZONE ||
		(dy > JOYSTICK_FORWARD_MAX_DY && Math.abs( dx ) <= JOYSTICK_STEER_DEADZONE);
	const right = dx > JOYSTICK_STEER_DEADZONE;
	return {
		active: true,
		dragged: true,
		origin: [ ox, oy ],
		knob,
		vector: [ clampedDx, clampedDy ],
		arrows: { up, left, right }
	};
}

/*
================
createMobileJoystick

Tracks the bottom-left thumb touch that drives 360-degree camera-relative
direction walking while preserving tap-to-walk when released within the deadzone.
The joystick base remains anchored at its fixed HUD origin throughout the touch.
================
*/
export function createMobileJoystick() {
	let activeId: number | null = null;
	let originX = 0;
	let originY = 0;
	let downX = 0;
	let downY = 0;
	let knobX = 0;
	let knobY = 0;
	let dragged = false;
	let vector: readonly [number, number] | null = null;
	let held: MobileJoystickArrows = { up: false, left: false, right: false };

	/*
	================
	transitionOutputs
	================
	*/
	function transitionOutputs( next: MobileJoystickArrows ): MobileJoystickKeyOutput[] {
		const outputs: MobileJoystickKeyOutput[] = [];
		for ( const [field, code] of ARROW_KEY_CODES ) {
			if ( held[field] !== next[field] ) {
				outputs.push( { kind: "key", code, down: next[field] } );
			}
		}
		held = next;
		return outputs;
	}

	return {
		/*
		================
		owns
		================
		*/
		owns( pointerId: number ): boolean {
			return activeId === pointerId;
		},
		/*
		================
		ownsAny
		================
		*/
		ownsAny(): boolean {
			return activeId !== null;
		},
		/*
		================
		state
		================
		*/
		state() {
			return {
				active: activeId !== null,
				dragged,
				origin: [ Math.round( originX ), Math.round( originY ) ] as const,
				knob: [ Math.round( knobX ), Math.round( knobY ) ] as const,
				vector: activeId !== null ? vector : null
			};
		},
		/*
		================
		heldOutputs
		================
		*/
		heldOutputs(): MobileJoystickKeyOutput[] {
			const outputs: MobileJoystickKeyOutput[] = [];
			for ( const [field, code] of ARROW_KEY_CODES ) {
				if ( held[field] ) outputs.push( { kind: "key", code, down: true } );
			}
			return outputs;
		},
		/*
		================
		down
		================
		*/
		down(
			pointerId: number,
			x: number,
			y: number,
			fixedOrigin?: readonly [number, number]
		): MobileJoystickKeyOutput[] {
			activeId = pointerId;
			originX = fixedOrigin ? fixedOrigin[0] : x;
			originY = fixedOrigin ? fixedOrigin[1] : y;
			downX = x;
			downY = y;
			knobX = originX;
			knobY = originY;
			dragged = false;
			vector = null;
			return transitionOutputs( { up: false, left: false, right: false } );
		},
		/*
		================
		move
		================
		*/
		move( pointerId: number, x: number, y: number ): MobileJoystickKeyOutput[] {
			if ( activeId !== pointerId ) return [];
			if ( !dragged && Math.hypot( x - downX, y - downY ) <= JOYSTICK_DEADZONE ) {
				return [];
			}
			const step = mobileJoystickStep( originX, originY, x, y );
			knobX = step.knob[0];
			knobY = step.knob[1];
			if ( step.dragged || Math.hypot( x - downX, y - downY ) > JOYSTICK_DEADZONE ) {
				dragged = true;
			}
			vector = step.vector;
			return transitionOutputs( step.arrows );
		},
		/*
		================
		up
		================
		*/
		up( pointerId: number, x?: number, y?: number ): {
			readonly outputs: readonly MobileJoystickKeyOutput[];
			readonly tap: boolean;
		} {
			if ( activeId !== pointerId ) return { outputs: [], tap: false };
			if ( x !== undefined && y !== undefined && Math.hypot( x - downX, y - downY ) > JOYSTICK_DEADZONE ) {
				dragged = true;
			}
			const tap = !dragged;
			activeId = null;
			dragged = false;
			knobX = originX;
			knobY = originY;
			vector = null;
			return {
				outputs: transitionOutputs( { up: false, left: false, right: false } ),
				tap
			};
		},
		/*
		================
		interrupt
		================
		*/
		interrupt(): MobileJoystickKeyOutput[] {
			activeId = null;
			dragged = false;
			knobX = originX;
			knobY = originY;
			vector = null;
			return transitionOutputs( { up: false, left: false, right: false } );
		}
	};
}

/*
================
pushRadialDisc

Appends horizontal chord slices approximating a filled circle of radius r
centered at (cx, cy).
================
*/
function pushRadialDisc(
	quads: UiQuad[],
	cx: number,
	cy: number,
	radius: number,
	color: readonly [number, number, number, number],
	clip: UiRect
): void {
	const r = Math.max( 1, Math.round( radius ) );
	for ( let dy = -r; dy < r; dy += RADIAL_STEP ) {
		const h = Math.min( RADIAL_STEP, r - dy );
		const sampleY = dy + h * 0.5;
		const halfW = Math.round( Math.sqrt( Math.max( 0, r * r - sampleY * sampleY ) ) );
		if ( halfW <= 0 ) continue;
		quads.push( {
			rect: [ cx - halfW, cy + dy, halfW * 2, h ],
			color,
			texture: "",
			uv: [ 0, 0, 1, 1 ],
			clip
		} );
	}
}

/*
================
pushRadialRing

Appends horizontal chord segments approximating a circular ring with outer
radius outerRadius and radial thickness thickness centered at (cx, cy).
================
*/
function pushRadialRing(
	quads: UiQuad[],
	cx: number,
	cy: number,
	outerRadius: number,
	thickness: number,
	color: readonly [number, number, number, number],
	clip: UiRect
): void {
	const outerR = Math.max( 2, Math.round( outerRadius ) );
	const innerR = Math.max( 0, outerRadius - thickness );
	for ( let dy = -outerR; dy < outerR; dy += RADIAL_STEP ) {
		const h = Math.min( RADIAL_STEP, outerR - dy );
		const sampleY = dy + h * 0.5;
		const outerHalf = Math.round( Math.sqrt( Math.max( 0, outerR * outerR - sampleY * sampleY ) ) );
		if ( outerHalf <= 0 ) continue;
		if ( Math.abs( sampleY ) >= innerR ) {
			quads.push( {
				rect: [ cx - outerHalf, cy + dy, outerHalf * 2, h ],
				color,
				texture: "",
				uv: [ 0, 0, 1, 1 ],
				clip
			} );
			continue;
		}
		const innerHalf = Math.round( Math.sqrt( Math.max( 0, innerR * innerR - sampleY * sampleY ) ) );
		const span = Math.max( 2, outerHalf - innerHalf );
		quads.push(
			{
				rect: [ cx - outerHalf, cy + dy, span, h ],
				color,
				texture: "",
				uv: [ 0, 0, 1, 1 ],
				clip
			},
			{
				rect: [ cx + outerHalf - span, cy + dy, span, h ],
				color,
				texture: "",
				uv: [ 0, 0, 1, 1 ],
				clip
			}
		);
	}
}

/*
================
mobileJoystickQuads

Builds the radial virtual joystick quads: a circular dark slate well, a dual
concentric gold radial ring with cardinal N/E/S/W directional chevrons, and
a spherical radial thumb knob with a top-left specular highlight.
================
*/
export function mobileJoystickQuads(
	origin: readonly [number, number],
	knob: readonly [number, number],
	active: boolean,
	clip: UiRect
): readonly UiQuad[] {
	const quads: UiQuad[] = [];
	const ox = Math.round( origin[0] );
	const oy = Math.round( origin[1] );
	const kx = Math.round( knob[0] );
	const ky = Math.round( knob[1] );

	const baseAlpha = active ? 0.52 : 0.28;
	const outerRingAlpha = active ? 0.86 : 0.46;
	const innerRingAlpha = active ? 0.42 : 0.2;
	const tickAlpha = active ? 0.88 : 0.45;
	const knobAlpha = active ? 0.94 : 0.56;

	// 1. Radial dark well + concentric gold rings.
	pushRadialDisc( quads, ox, oy, JOYSTICK_BASE_RADIUS - 1, [ 0.04, 0.06, 0.1, baseAlpha ], clip );
	pushRadialRing(
		quads,
		ox,
		oy,
		JOYSTICK_BASE_RADIUS,
		OUTER_RING_THICKNESS,
		[ 0.84, 0.71, 0.36, outerRingAlpha ],
		clip
	);
	pushRadialRing(
		quads,
		ox,
		oy,
		INNER_RING_RADIUS,
		INNER_RING_THICKNESS,
		[ 0.76, 0.64, 0.32, innerRingAlpha ],
		clip
	);

	// 2. Cardinal directional chevrons inside the outer ring (N, S, W, E).
	const tickColor: readonly [number, number, number, number] = [ 0.94, 0.82, 0.46, tickAlpha ];
	const tickOffset = JOYSTICK_BASE_RADIUS - 7;
	// North chevron
	quads.push(
		{ rect: [ ox - 2, oy - tickOffset - 2, 4, 2 ], color: tickColor, texture: "", uv: [ 0, 0, 1, 1 ], clip },
		{ rect: [ ox - 4, oy - tickOffset, 8, 2 ], color: tickColor, texture: "", uv: [ 0, 0, 1, 1 ], clip },
		// South chevron
		{ rect: [ ox - 4, oy + tickOffset - 2, 8, 2 ], color: tickColor, texture: "", uv: [ 0, 0, 1, 1 ], clip },
		{ rect: [ ox - 2, oy + tickOffset, 4, 2 ], color: tickColor, texture: "", uv: [ 0, 0, 1, 1 ], clip },
		// West chevron
		{ rect: [ ox - tickOffset - 2, oy - 2, 2, 4 ], color: tickColor, texture: "", uv: [ 0, 0, 1, 1 ], clip },
		{ rect: [ ox - tickOffset, oy - 4, 2, 8 ], color: tickColor, texture: "", uv: [ 0, 0, 1, 1 ], clip },
		// East chevron
		{ rect: [ ox + tickOffset - 2, oy - 4, 2, 8 ], color: tickColor, texture: "", uv: [ 0, 0, 1, 1 ], clip },
		{ rect: [ ox + tickOffset, oy - 2, 2, 4 ], color: tickColor, texture: "", uv: [ 0, 0, 1, 1 ], clip }
	);

	// 3. Radial thumb knob (shadow rim, metallic gold core, and spherical specular cap).
	pushRadialDisc( quads, kx, ky, JOYSTICK_KNOB_RADIUS, [ 0.46, 0.34, 0.12, knobAlpha ], clip );
	pushRadialDisc(
		quads,
		kx,
		ky,
		JOYSTICK_KNOB_RADIUS - KNOB_RIM_THICKNESS,
		[ 0.9, 0.74, 0.3, knobAlpha ],
		clip
	);
	pushRadialRing(
		quads,
		kx,
		ky,
		JOYSTICK_KNOB_RADIUS,
		KNOB_RIM_THICKNESS,
		[ 0.98, 0.86, 0.48, knobAlpha ],
		clip
	);
	pushRadialDisc(
		quads,
		kx - 2,
		ky - 2,
		KNOB_HIGHLIGHT_RADIUS,
		[ 1, 0.96, 0.78, knobAlpha * 0.92 ],
		clip
	);

	return quads;
}
