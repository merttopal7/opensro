/*
===========================================================================

asset-budget.ts - per-decode byte ceilings for asset worker requests

Shared request limits for the asset worker dispatcher and its callers.

===========================================================================
*/
import type { AssetRequest } from "@/engine/contracts/assets";

/*
================
assetRequestBudget
================
*/
export function assetRequestBudget( decode: Extract<AssetRequest, { kind: "load"; }>["decode"] ): number {
	// `effect` reads the shared EasyFX catalog, not one decoded character model.
	// Its native resource closure exceeds 32 MiB. Model/image residency keeps its
	// own independent bounds; the encoded catalog is retained once per worker.
	return (
		decode === "world" || decode === "frontend-world" || decode === "navigation" ?
			128 :
			decode === "effect" || decode === "effects" ?
			64 :
			16
	) * 1048576;
}
