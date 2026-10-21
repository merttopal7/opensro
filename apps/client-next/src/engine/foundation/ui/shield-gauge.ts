/*
===========================================================================
shield-gauge.ts - port-only light-gray absorption segment beside actual health

Scale both portions together only when HP plus shield exceeds max HP. Shield
never changes HP, max HP, healing or the native no-shield gauge presentation.
===========================================================================
*/
import type { UiQuad, UiRect } from "@/engine/contracts/ui";
import type { EntityState } from "@/engine/contracts/world";

/*
================
shieldGauge
================
*/
export function shieldGauge( entity: Pick<EntityState, "shield"> | undefined, hpRatio: number, now: number ) {
	const s = entity?.shield;
	const ratio = s && s.untilMs > now ? s.amount / s.maxHp : 0;
	const hp = Math.max( 0, Math.min( 1, hpRatio ) );
	const denominator = Math.max( 1, hp + ratio );
	return {
		health: hp / denominator,
		start: hp / denominator,
		shield: ratio / denominator,
		amount: ratio > 0 ? s!.amount : 0
	};
}

/*
================
shieldGaugeFill

A pale, raised highlight follows the native HP bar's vertical shading.
================
*/
export function shieldGaugeFill( r: UiRect, clip: UiRect ): UiQuad[] {
	const result: UiQuad[] = [];
	if ( r[2] <= 0 || r[3] <= 0 ) return result;
	for ( let y = 0; y < r[3]; y++ ) {
		const v = (y + .5) / r[3];
		const gray = .63 + .23 * Math.sin( Math.PI * v ) - .06 * v;
		result.push( {
			rect: [ r[0], r[1] + y, r[2], Math.min( 1, r[3] - y ) ],
			color: [ gray, gray, gray, 1 ],
			texture: "",
			uv: [ 0, 0, 1, 1 ],
			clip
		} );
	}
	return result;
}
