import { groundItemDistance, isGoldHeapType } from "@/engine/foundation/gameplay/ground-item";
import type { EntityState } from "@/engine/contracts/world";
import type { Pose } from "@/engine/contracts/gameplay";
// CIItem::Draw 86DDA0: hover bypasses the strict 300-unit range and Z latch.
export function groundItemNameVisible(
	entity: EntityState,
	origin: Pose | EntityState | undefined | null,
	hovered: boolean,
	held: boolean
): boolean {
	if ( !entity.groundItem || entity.groundItem.claimantGid ) return false;
	if ( hovered ) return true;
	if ( !held || !origin ) return false;
	return groundItemDistance( entity, origin ) < 300;
}
// 86E740..86E7DE: amount uses the native signed %d, followed by localized Gold.
export function groundItemName( entity: EntityState, gold: string ): string {
	const item = entity.groundItem;
	return item && isGoldHeapType( item.typeFlags ) ? `${item.goldAmount | 0} ${gold}` : entity.name ?? "";
}
