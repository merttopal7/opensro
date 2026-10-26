/*
===========================================================================

mobile-hud.ts - landscape thumb-centric HUD geometry for touch phones

Port-only, not native (docs/MOBILE-HUD.md). Computes safe-area-inset HUD
regions so the 3D world stays unobstructed in the center while the minimap
remains pinned in the top-right corner and player, buff and party status
stack cleanly on the left.

===========================================================================
*/

import type { SafeAreaInsets, UiRect } from "@/engine/contracts/ui";
import type { EntityState } from "@/engine/contracts/world";
import { worldDelta } from "@/engine/foundation/world/world-space";

const DESKTOP_WIDTH = 800;
const DESKTOP_HEIGHT = 600;
const MIN_INTERIOR_WIDTH = 200;
const MIN_INTERIOR_HEIGHT = 160;
const PLAYER_X = 4;
const PLAYER_Y = 7;
const PLAYER_WIDTH = 212;
const PLAYER_HEIGHT = 70;
const MENU_BUTTON_WIDTH = 48;
const MENU_BUTTON_HEIGHT = 24;
const PET_OFFSET_X = 53;
const PET_OFFSET_Y = 55;
const PET_WIDTH = 154;
const PET_HEIGHT = 40;
const SECTION_GAP = 4;
const BUFF_LEFT_OFFSET = 8;
const BUFF_COLUMNS = 6;
const BUFF_ICON_SIZE = 20;
const BUFF_PITCH_X = 23;
const BUFF_PITCH_Y = 27;
const BUFF_TOTAL_HEIGHT = 24;
const PARTY_LEFT_OFFSET = 4;
const PARTY_SLOT_WIDTH = 122;
const PARTY_SLOT_HEIGHT = 40;
const PARTY_COLUMN_PITCH = 126;
const PARTY_ROW_STEP = 44;
const PARTY_BUFF_ROW_STEP = 58;
const MINIMAP_RIGHT_SPAN = 129;
const MINIMAP_FRAME_WIDTH = 140;
const MINIMAP_FRAME_HEIGHT = 184;
const MINIMAP_CIRCLE_X = 14;
const MINIMAP_CIRCLE_Y = 57;
const MINIMAP_CIRCLE_SIZE = 105;
const MINIMAP_TOGGLE_X = 99;
const MINIMAP_TOGGLE_Y = 47;
const MINIMAP_TOGGLE_SIZE = 28;
const MINIMAP_ZOOM_IN_X = 103;
const MINIMAP_ZOOM_IN_Y = 132;
const MINIMAP_ZOOM_OUT_X = 82;
const MINIMAP_ZOOM_OUT_Y = 150;
const MINIMAP_ZOOM_TOUCH_SIZE = 28;
const TARGET_BAND_HEIGHT = 68;
const EXP_STRIP_HEIGHT = 4;
const CHAT_PREVIEW_WIDTH = 260;
const CHAT_PREVIEW_HEIGHT = 28;
const ATTACK_BUTTON_SIZE = 64;
const SKILL_SLOT_SIZE = 44;
const AUX_BUTTON_SIZE = 38;
const PANEL_DRAWER_MAX_WIDTH = 430;
const PANEL_DRAWER_LEFT_MARGIN = 48;
const MENU_GRID_COLUMNS = 4;
const MENU_GRID_ROWS = 2;
const MENU_GRID_MAX_WIDTH = 396;
const MENU_GRID_MAX_HEIGHT = 212;
const MENU_GRID_HEADER_HEIGHT = 40;
const MENU_GRID_PADDING = 12;
const MENU_GRID_GAP = 8;
const MENU_CLOSE_SIZE = 40;
export const JOYSTICK_BASE_RADIUS = 42;
export const JOYSTICK_KNOB_RADIUS = 18;
export const JOYSTICK_DEADZONE = 8;
const JOYSTICK_STEER_DEADZONE = 10;
const JOYSTICK_FORWARD_MAX_DY = 14;
const MONSTER_DEAD_STATE = 2;

const ARROW_KEY_CODES = [
	[ "up", "ArrowUp" ],
	[ "left", "ArrowLeft" ],
	[ "right", "ArrowRight" ]
] as const;

/*
================
MobileMinimapLayout
================
*/
export interface MobileMinimapLayout {
	readonly origin: readonly [number, number];
	readonly frame: UiRect;
	readonly circle: UiRect;
	readonly zoomIn: UiRect;
	readonly zoomOut: UiRect;
	readonly toggleMap: UiRect;
	readonly bottom: number;
}

/*
================
MobileActionCluster
================
*/
export interface MobileActionCluster {
	readonly attack: UiRect;
	readonly skills: readonly UiRect[];
	readonly targetCycle: UiRect;
	readonly pickup: UiRect;
	readonly pageToggle: UiRect;
}

/*
================
MobileHudLayout
================
*/
export interface MobileHudLayout {
	readonly safe: SafeAreaInsets & { readonly bounds: UiRect; };
	readonly playerOffset: readonly [number, number];
	readonly playerRect: UiRect;
	readonly menuButton: UiRect;
	readonly petRect: UiRect | null;
	readonly buffOrigin: readonly [number, number];
	readonly buffColumns: number;
	readonly partyOrigin: readonly [number, number];
	readonly partyBottomLimit: number;
	readonly minimap: MobileMinimapLayout;
	readonly targetBand: UiRect;
	readonly joystickZone: UiRect;
	readonly actionCluster: MobileActionCluster;
	readonly expBar: UiRect;
	readonly chatPreview: UiRect;
	readonly panelDrawer: UiRect;
}

/*
================
MobileHudLayoutOptions
================
*/
export interface MobileHudLayoutOptions {
	readonly safeArea?: Partial<SafeAreaInsets> | null;
	readonly hasPet?: boolean;
	readonly petBottom?: number;
	readonly buffBottom?: number;
	readonly bottomReserve?: number;
}

/*
================
clampInset
================
*/
function clampInset( value: number | undefined, max: number ): number {
	if ( typeof value !== "number" || !Number.isFinite( value ) || value <= 0 ) return 0;
	return Math.min( Math.floor( value ), Math.max( 0, Math.floor( max ) ) );
}

/*
================
normalizeSafeArea

Clamps browser safe-area insets so malformed or oversized values never
collapse the playable interior rectangle.
================
*/
export function normalizeSafeArea(
	width: number,
	height: number,
	safeArea?: Partial<SafeAreaInsets> | null
): SafeAreaInsets {
	const maxHorizontal = Math.max( 0, width - MIN_INTERIOR_WIDTH );
	const maxVertical = Math.max( 0, height - MIN_INTERIOR_HEIGHT );
	const left = clampInset( safeArea?.left, maxHorizontal / 2 );
	const right = clampInset( safeArea?.right, maxHorizontal - left );
	const top = clampInset( safeArea?.top, maxVertical / 2 );
	const bottom = clampInset( safeArea?.bottom, maxVertical - top );
	return { top, right, bottom, left };
}

/*
================
mobileHudActive

Port-only, not native. Enabled on landscape touch screens smaller than the
800x600 desktop HUD while the Experimental mobileHud preference is on.
================
*/
export function mobileHudActive(
	width: number,
	height: number,
	options?: { readonly enabled?: boolean; readonly mobileHud?: boolean; readonly touch?: boolean; }
): boolean {
	const enabled = options?.enabled ?? options?.mobileHud ?? true;
	if ( !enabled || !options?.touch ) return false;
	if ( width >= DESKTOP_WIDTH && height >= DESKTOP_HEIGHT ) return false;
	return width > height;
}

/*
================
mobileHudLayout

Pure geometry for every region of the landscape mobile HUD.
================
*/
export function mobileHudLayout(
	width: number,
	height: number,
	options: MobileHudLayoutOptions = {}
): MobileHudLayout {
	const safe = normalizeSafeArea( width, height, options.safeArea );
	const safeWidth = Math.max( 1, width - safe.left - safe.right );
	const safeHeight = Math.max( 1, height - safe.top - safe.bottom );
	const safeBounds: UiRect = [ safe.left, safe.top, safeWidth, safeHeight ];

	const playerOffset = [ safe.left, safe.top ] as const;
	const playerRect: UiRect = [
		safe.left + PLAYER_X,
		safe.top + PLAYER_Y,
		PLAYER_WIDTH,
		PLAYER_HEIGHT
	];
	const menuButton: UiRect = [
		playerRect[0],
		playerRect[1] + PLAYER_HEIGHT - MENU_BUTTON_HEIGHT,
		MENU_BUTTON_WIDTH,
		MENU_BUTTON_HEIGHT
	];
	const petRect: UiRect | null = options.hasPet ?
		[
			playerRect[0] + PET_OFFSET_X,
			playerRect[1] + PET_OFFSET_Y,
			PET_WIDTH,
			PET_HEIGHT
		] :
		null;
	const statusBottom = Math.max(
		playerRect[1] + playerRect[3],
		menuButton[1] + menuButton[3],
		petRect ? petRect[1] + petRect[3] : 0,
		options.petBottom ?? 0
	);
	const buffOrigin = [ safe.left + BUFF_LEFT_OFFSET, statusBottom + SECTION_GAP ] as const;
	const afterBuffs = options.buffBottom !== undefined && options.buffBottom > buffOrigin[1] ?
		options.buffBottom + SECTION_GAP :
		buffOrigin[1];
	const partyOrigin = [ safe.left + PARTY_LEFT_OFFSET, afterBuffs ] as const;

	const mx = Math.max( safe.left, width - safe.right - MINIMAP_RIGHT_SPAN );
	const my = safe.top;
	const minimap: MobileMinimapLayout = {
		origin: [ mx, my ],
		frame: [ mx, my, MINIMAP_FRAME_WIDTH, MINIMAP_FRAME_HEIGHT ],
		circle: [ mx + MINIMAP_CIRCLE_X, my + MINIMAP_CIRCLE_Y, MINIMAP_CIRCLE_SIZE, MINIMAP_CIRCLE_SIZE ],
		zoomIn: [ mx + MINIMAP_ZOOM_IN_X, my + MINIMAP_ZOOM_IN_Y, MINIMAP_ZOOM_TOUCH_SIZE, MINIMAP_ZOOM_TOUCH_SIZE ],
		zoomOut: [
			mx + MINIMAP_ZOOM_OUT_X,
			my + MINIMAP_ZOOM_OUT_Y,
			MINIMAP_ZOOM_TOUCH_SIZE,
			MINIMAP_ZOOM_TOUCH_SIZE
		],
		toggleMap: [ mx + MINIMAP_TOGGLE_X, my + MINIMAP_TOGGLE_Y, MINIMAP_TOGGLE_SIZE, MINIMAP_TOGGLE_SIZE ],
		bottom: my + MINIMAP_FRAME_HEIGHT
	};

	const targetLeft = playerRect[0] + playerRect[2] + SECTION_GAP;
	const targetRight = Math.max( targetLeft, mx - SECTION_GAP );
	const targetBand: UiRect = [
		targetLeft,
		safe.top + PLAYER_Y,
		Math.max( 0, targetRight - targetLeft ),
		TARGET_BAND_HEIGHT
	];

	const expBar: UiRect = [
		safe.left,
		height - safe.bottom - EXP_STRIP_HEIGHT,
		safeWidth,
		EXP_STRIP_HEIGHT
	];
	const chatPreviewWidth = Math.min( CHAT_PREVIEW_WIDTH, Math.floor( safeWidth * 0.42 ) );
	const chatPreview: UiRect = [
		safe.left + BUFF_LEFT_OFFSET,
		expBar[1] - CHAT_PREVIEW_HEIGHT - SECTION_GAP,
		chatPreviewWidth,
		CHAT_PREVIEW_HEIGHT
	];

	const joystickTop = Math.floor( safe.top + safeHeight * 0.45 );
	const joystickZone: UiRect = [
		safe.left,
		joystickTop,
		Math.floor( safeWidth * 0.44 ),
		Math.max( 1, chatPreview[1] - joystickTop - SECTION_GAP )
	];

	const clusterBottom = expBar[1] - SECTION_GAP;
	const rightColumnX = width - safe.right - SKILL_SLOT_SIZE - SECTION_GAP;
	const attackX = rightColumnX - ATTACK_BUTTON_SIZE - 6;
	const attackY = clusterBottom - ATTACK_BUTTON_SIZE;
	const midRowY = clusterBottom - AUX_BUTTON_SIZE - SECTION_GAP - SKILL_SLOT_SIZE;
	const topRowY = midRowY - SECTION_GAP - SKILL_SLOT_SIZE;
	const leftSkillX = attackX - SKILL_SLOT_SIZE - 6;
	const farLeftSkillX = leftSkillX - SKILL_SLOT_SIZE - SECTION_GAP;
	const skills: UiRect[] = [
		[ farLeftSkillX, midRowY, SKILL_SLOT_SIZE, SKILL_SLOT_SIZE ],
		[ leftSkillX, topRowY, SKILL_SLOT_SIZE, SKILL_SLOT_SIZE ],
		[ attackX + 10, attackY - SKILL_SLOT_SIZE - 6, SKILL_SLOT_SIZE, SKILL_SLOT_SIZE ],
		[ leftSkillX, midRowY, SKILL_SLOT_SIZE, SKILL_SLOT_SIZE ],
		[ rightColumnX, topRowY, SKILL_SLOT_SIZE, SKILL_SLOT_SIZE ],
		[ rightColumnX, midRowY, SKILL_SLOT_SIZE, SKILL_SLOT_SIZE ]
	];
	const auxY = clusterBottom - AUX_BUTTON_SIZE;
	const targetCycleX = attackX - AUX_BUTTON_SIZE - 6;
	const actionCluster: MobileActionCluster = {
		attack: [ attackX, attackY, ATTACK_BUTTON_SIZE, ATTACK_BUTTON_SIZE ],
		skills,
		targetCycle: [ targetCycleX, auxY, AUX_BUTTON_SIZE, AUX_BUTTON_SIZE ],
		pickup: [ rightColumnX + 3, auxY, AUX_BUTTON_SIZE, AUX_BUTTON_SIZE ],
		pageToggle: [ targetCycleX - AUX_BUTTON_SIZE - 6, auxY, AUX_BUTTON_SIZE, AUX_BUTTON_SIZE ]
	};

	const drawerWidth = Math.min( PANEL_DRAWER_MAX_WIDTH, Math.max( 200, safeWidth - PANEL_DRAWER_LEFT_MARGIN ) );
	const panelDrawer: UiRect = [
		width - safe.right - drawerWidth,
		safe.top,
		drawerWidth,
		Math.max( 1, expBar[1] - safe.top )
	];

	const bottomReserve = options.bottomReserve ?? EXP_STRIP_HEIGHT;
	const partyBottomLimit = Math.max( partyOrigin[1] + PARTY_SLOT_HEIGHT, height - safe.bottom - bottomReserve );

	return {
		safe: { ...safe, bounds: safeBounds },
		playerOffset,
		playerRect,
		menuButton,
		petRect,
		buffOrigin,
		buffColumns: BUFF_COLUMNS,
		partyOrigin,
		partyBottomLimit,
		minimap,
		targetBand,
		joystickZone,
		actionCluster,
		expBar,
		chatPreview,
		panelDrawer
	};
}

/*
================
mobileTargetOrigin

Places the target frame centered inside the top band between the player
panel and the pinned minimap, or below the player status if the center band
is narrower than the target frame.
================
*/
export function mobileTargetOrigin(
	layout: MobileHudLayout,
	targetWidth: number
): readonly [number, number] {
	const [bandX, bandY, bandWidth] = layout.targetBand;
	if ( bandWidth >= targetWidth ) {
		return [ bandX + Math.floor( (bandWidth - targetWidth) / 2 ), bandY ];
	}
	return [ layout.playerRect[0], layout.buffOrigin[1] ];
}

/*
================
mobileBuffLayout

Reflows buffBoard entries into the compact left-column grid beneath the
player portrait and reports the bottom Y coordinate of the last active row.
================
*/
export function mobileBuffLayout<T extends { readonly id: string; }>(
	icons: readonly T[],
	origin: readonly [number, number],
	columns = BUFF_COLUMNS
): {
	readonly icons: readonly (T & { readonly x: number; readonly y: number; readonly rect: UiRect; })[];
	readonly bottom: number;
} {
	const cols = Math.max( 1, columns );
	let bottom = origin[1];
	const placed = icons.map( ( icon, index ) => {
		const x = origin[0] + (index % cols) * BUFF_PITCH_X;
		const y = origin[1] + Math.floor( index / cols ) * BUFF_PITCH_Y;
		const rect: UiRect = [ x, y, BUFF_ICON_SIZE, BUFF_ICON_SIZE ];
		bottom = Math.max( bottom, rect[1] + BUFF_TOTAL_HEIGHT );
		return { ...icon, x, y, rect };
	} );
	return { icons: placed, bottom };
}

/*
================
mobilePartyLayout

Stacks quick-party member slots vertically in the left safe column without
the desktop 230-pixel bottom reserve that wrapped every member into a new
horizontal column on 393-pixel phone screens.
================
*/
export function mobilePartyLayout<
	T extends {
		readonly position: readonly [number, number];
		readonly masteries: readonly { readonly id: number; readonly rect: UiRect; }[];
	}
>(
	rows: readonly T[],
	origin: readonly [number, number],
	bottomLimit: number,
	buffs: boolean
): readonly T[] {
	const stepY = buffs ? PARTY_BUFF_ROW_STEP : PARTY_ROW_STEP;
	let column = 0, rowY = 0;
	return rows.map( row => {
		if ( rowY > 0 && origin[1] + rowY + PARTY_SLOT_HEIGHT > bottomLimit ) {
			column += PARTY_COLUMN_PITCH;
			rowY = 0;
		}
		const nextX = origin[0] + column;
		const nextY = origin[1] + rowY;
		const dx = nextX - row.position[0];
		const dy = nextY - row.position[1];
		rowY += stepY;
		return {
			...row,
			position: [ nextX, nextY ] as const,
			masteries: row.masteries.map( entry => ({
				...entry,
				rect: [ entry.rect[0] + dx, entry.rect[1] + dy, entry.rect[2], entry.rect[3] ] as const
			}) )
		};
	} );
}

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
mobileJoystickStep

Pure deflection math for the floating bottom-left virtual joystick.
Maps thumb displacement to clamped knob coordinates and the existing
ArrowUp / ArrowLeft / ArrowRight locomotion inputs.
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
			arrows: { up: false, left: false, right: false }
		};
	}
	const scale = dist > JOYSTICK_BASE_RADIUS ? JOYSTICK_BASE_RADIUS / dist : 1;
	const knob: readonly [number, number] = [
		Math.round( originX + dx * scale ),
		Math.round( originY + dy * scale )
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
		arrows: { up, left, right }
	};
}

/*
================
createMobileJoystick

Tracks the bottom-left thumb touch that drives ArrowUp / ArrowLeft / ArrowRight
while preserving tap-to-walk when released within the deadzone.
================
*/
export function createMobileJoystick() {
	let activeId: number | null = null;
	let originX = 0;
	let originY = 0;
	let knobX = 0;
	let knobY = 0;
	let dragged = false;
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
				active: activeId !== null && dragged,
				origin: [ Math.round( originX ), Math.round( originY ) ] as const,
				knob: [ Math.round( knobX ), Math.round( knobY ) ] as const
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
		down( pointerId: number, x: number, y: number ): MobileJoystickKeyOutput[] {
			const released = transitionOutputs( { up: false, left: false, right: false } );
			activeId = pointerId;
			originX = x;
			originY = y;
			knobX = x;
			knobY = y;
			dragged = false;
			return released;
		},
		/*
		================
		move
		================
		*/
		move( pointerId: number, x: number, y: number ): MobileJoystickKeyOutput[] {
			if ( activeId !== pointerId ) return [];
			const step = mobileJoystickStep( originX, originY, x, y );
			knobX = step.knob[0];
			knobY = step.knob[1];
			if ( step.dragged ) dragged = true;
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
			if ( x !== undefined && y !== undefined && Math.hypot( x - originX, y - originY ) > JOYSTICK_DEADZONE ) {
				dragged = true;
			}
			const tap = !dragged;
			activeId = null;
			dragged = false;
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
			return transitionOutputs( { up: false, left: false, right: false } );
		}
	};
}

/*
================
mobileMonsterTargets

Returns alive monsters ordered from nearest to farthest around the player's
pose so the mobile Attack and Target Cycle buttons act on nearby threats first.
================
*/
export type MobileTargetCandidate = Pick<
	EntityState,
	"gid" | "kind" | "regionId" | "x" | "y" | "z" | "appearanceState"
>;

export function mobileMonsterTargets(
	entities: readonly MobileTargetCandidate[],
	pose?: Pick<EntityState, "regionId" | "x" | "y" | "z"> | null,
	vitals?: readonly { readonly gid: number; readonly hp?: number; readonly deathState?: boolean | number; }[]
): readonly MobileTargetCandidate[] {
	const alive = entities.filter( entity => {
		if ( entity.kind !== "monster" || entity.gid <= 0 ) return false;
		if ( entity.appearanceState?.[0] === MONSTER_DEAD_STATE ) return false;
		const vital = vitals?.find( row => row.gid === entity.gid );
		if ( vital && (vital.deathState || (vital.hp !== undefined && vital.hp <= 0)) ) return false;
		return true;
	} );
	if ( !pose ) return alive;
	return [ ...alive ].sort( ( a, b ) => {
		const [adx, adz] = worldDelta( pose, a );
		const [bdx, bdz] = worldDelta( pose, b );
		const ady = a.y - pose.y;
		const bdy = b.y - pose.y;
		const distA = adx * adx + ady * ady + adz * adz;
		const distB = bdx * bdx + bdy * bdy + bdz * bdz;
		return distA !== distB ? distA - distB : a.gid - b.gid;
	} );
}

/*
================
mobileCycleTarget

Selects the next alive monster in distance order, wrapping around to the
nearest monster after the last one.
================
*/
export function mobileCycleTarget(
	entities: readonly MobileTargetCandidate[],
	pose?: Pick<EntityState, "regionId" | "x" | "y" | "z"> | null,
	vitals?: readonly { readonly gid: number; readonly hp?: number; readonly deathState?: boolean | number; }[],
	currentTarget?: number
): number {
	const monsters = mobileMonsterTargets( entities, pose, vitals );
	if ( !monsters.length ) return 0;
	const index = monsters.findIndex( row => row.gid === currentTarget );
	return monsters[(index + 1) % monsters.length]!.gid;
}

/*
================
mobileAttackTarget

Resolves the monster GID the mobile Attack button should engage: keeps the
current target if it is already an alive monster, otherwise selects the
nearest alive monster.
================
*/
export function mobileAttackTarget(
	entities: readonly MobileTargetCandidate[],
	pose?: Pick<EntityState, "regionId" | "x" | "y" | "z"> | null,
	vitals?: readonly { readonly gid: number; readonly hp?: number; readonly deathState?: boolean | number; }[],
	currentTarget?: number
): { readonly gid: number; readonly select: boolean; } {
	const monsters = mobileMonsterTargets( entities, pose, vitals );
	if ( currentTarget && monsters.some( row => row.gid === currentTarget ) ) {
		return { gid: currentTarget, select: false };
	}
	if ( monsters.length > 0 ) {
		return { gid: monsters[0]!.gid, select: true };
	}
	return { gid: 0, select: false };
}

/*
================
MobileMenuItem
================
*/
export interface MobileMenuItem {
	readonly key: "Inventory" | "Character" | "Skills" | "Quests" | "Party" | "Guild" | "Mall" | "System";
	readonly id: string;
	readonly label: string;
	readonly short: string;
}

/*
================
mobileMenuItems
================
*/
export function mobileMenuItems(): readonly MobileMenuItem[] {
	return [
		{ key: "Inventory", id: "mobile-menu:Inventory", label: "Inventory", short: "INV" },
		{ key: "Character", id: "mobile-menu:Character", label: "Character", short: "CHR" },
		{ key: "Skills", id: "mobile-menu:Skills", label: "Skills", short: "SKL" },
		{ key: "Quests", id: "mobile-menu:Quests", label: "Quests", short: "QST" },
		{ key: "Party", id: "mobile-menu:Party", label: "Party", short: "PTY" },
		{ key: "Guild", id: "mobile-menu:Guild", label: "Guild", short: "GLD" },
		{ key: "Mall", id: "mobile-menu:Mall", label: "Mall", short: "MAL" },
		{ key: "System", id: "mobile-menu:System", label: "Settings", short: "SET" }
	];
}

/*
================
MobileMenuCell
================
*/
export interface MobileMenuCell extends MobileMenuItem {
	readonly rect: UiRect;
	readonly iconRect: UiRect;
	readonly labelRect: UiRect;
}

/*
================
MobileMenuGridLayout
================
*/
export interface MobileMenuGridLayout {
	readonly frame: UiRect;
	readonly close: UiRect;
	readonly cells: readonly MobileMenuCell[];
}

/*
================
mobileMenuGridLayout

Computes the centered 4x2 touch menu grid opened by the top-left Menu button,
replacing the legacy 11-button compact bottom toolbar.
================
*/
export function mobileMenuGridLayout( layout: MobileHudLayout ): MobileMenuGridLayout {
	const [sx, sy, sw] = layout.safe.bounds;
	const sh = Math.max( 1, layout.expBar[1] - sy );
	const frameWidth = Math.min( MENU_GRID_MAX_WIDTH, Math.max( 240, sw - MENU_GRID_PADDING * 2 ) );
	const frameHeight = Math.min( MENU_GRID_MAX_HEIGHT, Math.max( 148, sh - MENU_GRID_PADDING * 2 ) );
	const fx = sx + Math.floor( (sw - frameWidth) / 2 );
	const fy = sy + Math.floor( (sh - frameHeight) / 2 );
	const frame: UiRect = [ fx, fy, frameWidth, frameHeight ];
	const close: UiRect = [ fx + frameWidth - MENU_CLOSE_SIZE, fy, MENU_CLOSE_SIZE, MENU_CLOSE_SIZE ];
	const gridTop = fy + MENU_GRID_HEADER_HEIGHT;
	const gridWidth = Math.max( MENU_GRID_COLUMNS, frameWidth - MENU_GRID_PADDING * 2 );
	const gridHeight = Math.max( MENU_GRID_ROWS, frameHeight - MENU_GRID_HEADER_HEIGHT - MENU_GRID_PADDING );
	const cellWidth = Math.max(
		1,
		Math.floor( (gridWidth - MENU_GRID_GAP * (MENU_GRID_COLUMNS - 1)) / MENU_GRID_COLUMNS )
	);
	const cellHeight = Math.max(
		1,
		Math.floor( (gridHeight - MENU_GRID_GAP * (MENU_GRID_ROWS - 1)) / MENU_GRID_ROWS )
	);
	const iconSize = Math.min( 28, Math.max( 18, cellHeight - 28 ) );
	const cells: MobileMenuCell[] = mobileMenuItems().map( ( item, index ) => {
		const col = index % MENU_GRID_COLUMNS;
		const row = Math.floor( index / MENU_GRID_COLUMNS );
		const cx = fx + MENU_GRID_PADDING + col * (cellWidth + MENU_GRID_GAP);
		const cy = gridTop + row * (cellHeight + MENU_GRID_GAP);
		return {
			...item,
			rect: [ cx, cy, cellWidth, cellHeight ],
			iconRect: [ cx + Math.floor( (cellWidth - iconSize) / 2 ), cy + 8, iconSize, iconSize ],
			labelRect: [ cx + 4, cy + cellHeight - 22, Math.max( 1, cellWidth - 8 ), 18 ]
		};
	} );
	return { frame, close, cells };
}

/*
================
mobileDrawerBounds

Computes the right-aligned target rectangle inside layout.panelDrawer so
fitUiGroup docks single-panel windows flush against the right safe-area edge
while preserving >= 0.9 scale on standard phone viewports.
================
*/
export function mobileDrawerBounds(
	layout: MobileHudLayout,
	sourceWidth: number,
	sourceHeight: number
): UiRect {
	const [drawerX, drawerY, drawerWidth, drawerHeight] = layout.panelDrawer;
	const scale = Math.min(
		1,
		Math.max( 1, drawerWidth ) / Math.max( 1, sourceWidth ),
		Math.max( 1, drawerHeight ) / Math.max( 1, sourceHeight )
	);
	const fittedWidth = Math.min( drawerWidth, Math.max( 1, Math.ceil( sourceWidth * scale ) ) );
	return [
		drawerX + drawerWidth - fittedWidth,
		drawerY,
		fittedWidth,
		drawerHeight
	];
}
