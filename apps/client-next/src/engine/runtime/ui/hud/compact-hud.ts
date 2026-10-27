/*
===========================================================================

compact-hud.ts - fitting the original HUD controls on small browser screens

Port-only, not native. The owner approved automatic adaptation when the
desktop's 800-pixel underbar or 600-pixel canvas no longer fits. Secondary
panels start collapsed without changing saved desktop preferences.

===========================================================================
*/
import type { SafeAreaInsets, UiRect, UiControl } from "@/engine/contracts/ui";
import { mobileHudActive, mobileHudLayout, normalizeSafeArea } from "@/engine/foundation/ui/mobile-hud";
import { closedPanel } from "@/engine/foundation/ui/panels";

const DESKTOP_WIDTH = 800;
const DESKTOP_HEIGHT = 600;
const SLOT_COUNT = 11;
const SLOT_PITCH = 44;
const EXP_HEIGHT = 20;
const TOOL_HEIGHT = 40;
const TOOL_COUNT = 11;
const MARGIN = 4;
const CLOSE_TOUCH_SIZE = 40;

/*
================
compactHudLayout
================
*/
export function compactHudLayout( width: number, height: number, safeArea?: Partial<SafeAreaInsets> | null ) {
	if ( width >= DESKTOP_WIDTH && height >= DESKTOP_HEIGHT ) return null;
	const safe = normalizeSafeArea( width, height, safeArea );
	const safeWidth = Math.max( 1, width - safe.left - safe.right );
	const safeBottomEdge = Math.max( safe.top, height - safe.bottom );
	const capacity = Math.max( 1, Math.floor( (safeWidth - MARGIN * 2) / SLOT_PITCH ) );
	const rows = Math.ceil( SLOT_COUNT / capacity ), columns = Math.ceil( SLOT_COUNT / rows );
	const expHeight = height >= 240 ? EXP_HEIGHT : 0;
	const barHeight = rows * SLOT_PITCH + expHeight + TOOL_HEIGHT + MARGIN * 2;
	const bottom = barHeight + safe.bottom;
	const left = safe.left + Math.floor( (safeWidth - columns * SLOT_PITCH) / 2 ),
		top = Math.max( safe.top, safeBottomEdge - barHeight );
	const slots: UiRect[] = Array.from( { length: SLOT_COUNT }, ( _, index ) => [
		left + index % columns * SLOT_PITCH,
		top + MARGIN + Math.floor( index / columns ) * SLOT_PITCH,
		SLOT_PITCH,
		SLOT_PITCH
	] );
	const expWidth = safeWidth >= 412 ? 406 : safeWidth >= 260 ? 256 : 200;
	const exp: UiRect | null = expHeight > 0 ?
		[
			safe.left + Math.max( 0, Math.floor( (safeWidth - expWidth) / 2 ) ),
			top + MARGIN + rows * SLOT_PITCH,
			Math.min( safeWidth, expWidth ),
			expHeight
		] :
		null;
	const toolWidth = Math.max( 0, Math.min( 48, Math.floor( (safeWidth - MARGIN * 2) / TOOL_COUNT ) ) );
	const tools: UiRect[] = Array.from( { length: TOOL_COUNT }, ( _, index ) => [
		safe.left + Math.floor( (safeWidth - toolWidth * TOOL_COUNT) / 2 ) + index * toolWidth,
		safeBottomEdge - TOOL_HEIGHT - MARGIN,
		toolWidth,
		TOOL_HEIGHT
	] );
	return {
		slots,
		exp,
		tools,
		bottom,
		top,
		bounds: [ safe.left, safe.top, safeWidth, Math.max( 0, top - safe.top ) ] as UiRect
	};
}

/*
================
createCompactHud

One secondary overlay at a time keeps narrow screens usable. Rotation
retains the open overlay; returning from desktop starts collapsed again.
================
*/
export function createCompactHud() {
	let active = false, overlay = "", touch = false, safeArea: SafeAreaInsets | undefined;
	return {
		/*
		================
		environment
		================
		*/
		environment( nextTouch: boolean, nextSafeArea?: SafeAreaInsets ) {
			touch = nextTouch;
			safeArea = nextSafeArea;
		},
		/*
		================
		layout
		================
		*/
		layout(
			width: number,
			height: number,
			options?: {
				readonly mobileHud?: boolean;
				readonly touch?: boolean;
				readonly safeArea?: SafeAreaInsets;
				readonly hasPet?: boolean;
			}
		) {
			const effectiveSafeArea = options?.safeArea ?? safeArea;
			const effectiveTouch = options?.touch ?? touch;
			const layout = compactHudLayout( width, height, effectiveSafeArea );
			if ( !!layout !== active ) overlay = "";
			active = !!layout;
			if ( !layout ) return null;
			const mobile = mobileHudActive( width, height, {
					enabled: options?.mobileHud,
					touch: effectiveTouch
				} ) ?
				mobileHudLayout( width, height, {
					safeArea: effectiveSafeArea,
					hasPet: options?.hasPet
				} ) :
				null;
			if ( mobile ) {
				return {
					...layout,
					top: mobile.expBar[1],
					bottom: height - mobile.expBar[1],
					bounds: [
						mobile.safe.bounds[0],
						mobile.safe.bounds[1],
						mobile.safe.bounds[2],
						Math.max( 0, mobile.expBar[1] - mobile.safe.bounds[1] )
					] as UiRect,
					overlay,
					mobile
				};
			}
			return { ...layout, overlay, mobile: null };
		},
		/*
		================
		open
		================
		*/
		open( next: string ) {
			if ( active ) overlay = next;
		},
		/*
		================
		toggle
		================
		*/
		toggle( next: string ) {
			overlay = overlay === next ? "" : next;
		},
		/*
		================
		reset
		================
		*/
		reset() {
			active = false;
			overlay = "";
		}
	};
}

/*
================
compactWindowDrag

Window positions remain desktop preferences. Gameplay carries and shortcut
binding drags use the published hit rectangles and must remain enabled. The
extended bar is no window position: its handle moves a saved quickslot
preference, so it stays draggable on compact.
================
*/
export function compactWindowDrag( control: UiControl ): boolean {
	if ( control.id === "ext-drag" ) return false;
	return control.id.endsWith( "-drag" ) || control.id.startsWith( "window-drag:" ) || control.id === "map-pan";
}

/*
================
compactCloseControl

Fitting large artwork must not make its only exit too small to touch.
The close artwork stays native; its hit area grows into the title bar.
================
*/
export function compactCloseControl( control: UiControl, screen: UiRect ): UiControl {
	if ( closedPanel( control.id ) === undefined && !control.id.endsWith( "-close" ) ) return control;
	const [x, y, width, height] = control.rect;
	const touchWidth = Math.min( screen[2], Math.max( width, CLOSE_TOUCH_SIZE ) );
	const touchHeight = Math.min( screen[3], Math.max( height, CLOSE_TOUCH_SIZE ) );
	return {
		...control,
		rect: [
			Math.max( screen[0], Math.min( x + width / 2 - touchWidth / 2, screen[0] + screen[2] - touchWidth ) ),
			Math.max( screen[1], Math.min( y + height / 2 - touchHeight / 2, screen[1] + screen[3] - touchHeight ) ),
			touchWidth,
			touchHeight
		]
	};
}
