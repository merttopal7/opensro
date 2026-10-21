/*
===========================================================================

world-material.ts - native world material inputs to the renderer contract

Static and animated world objects share the BMT draw policy. The published
resource projection represents an authored material without a texture as
null; the renderer represents that same state with an absent texture.

===========================================================================
*/
import type { WorldMaterial } from "@/engine/contracts/scene";

/*
================
WorldObjectMaterialSource
================
*/
export interface WorldObjectMaterialSource {
	readonly flags: number;
	readonly colors: { readonly diffuse: [number, number, number, number]; };
	readonly texturePublicPath?: string | null;
}

// A5C510/A5C930 -> AAE3E0: the native draw state uses ALPHAREF=128.
// A GLB's PBR cutoff and doubleSided are export hints, not this contract.
// Static and animated world objects must enter the same material policy.
/*
================
worldObjectMaterial
================
*/
export function worldObjectMaterial( mat: WorldObjectMaterialSource ): WorldMaterial {
	const skip = !!(mat.flags & 0x10000000), alpha = !skip && !!(mat.flags & 0x200) && !(mat.flags & 0x20000000);
	return {
		objectFade: true,
		fadeAlphaOnly: !!(mat.flags & 0x40000000),
		texture: mat.texturePublicPath ?? undefined,
		color: mat.colors.diffuse,
		alphaCutoff: alpha ? 128 / 255 : 0,
		blend: false,
		doubleSided: !skip && !!(mat.flags & 1),
		stageFactor: mat.flags & 8 ? 1 : 2,
		objectLight: 0.6,
		unlit: !!(mat.flags & 8)
	};
}
