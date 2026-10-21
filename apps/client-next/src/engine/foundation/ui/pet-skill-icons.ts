/*
===========================================================================
pet-skill-icons.ts - port-only owned pet art, row help and server stack badges.

Each tier atlas has five skill families per pet. Simpler first/second books
lead into the unchanged third-book art, also used for the row information icon.
===========================================================================
*/
import type { PetTraining, PetTrainingSkill } from "@/engine/contracts/gameplay";
import type { UiQuad, UiRect } from "@/engine/contracts/ui";
import { PET_DESIGN } from "./pet-design-data";
import { petRowCooldown, type PetSkillDesign } from "./pet-training";

export const PET_SKILL_ATLAS = "/assets/images/Media_extracted/icon/port/pet-skills.png";

/*
================
petSkillIcon
================
*/
export function petSkillIcon( design: PetSkillDesign ): string {
	return design.option === 3 ? PET_SKILL_ATLAS : PET_SKILL_ATLAS.replace( ".png", `-${design.option}.png` );
}

/*
================
petSkillIconUV
================
*/
export function petSkillIconUV( design: PetSkillDesign ): UiRect {
	const row = [ "Wolf", "Bear", "Penguin", "Crow" ].indexOf( design.pet );
	return [ design.row / 5, row / 4, 1 / 5, 1 / 4 ];
}

/*
================
petSkillStack

Counters are observed server state, never inferred from animation or elapsed time.
================
*/
export function petSkillStack( state: PetTraining, design: PetSkillDesign, skill: PetTrainingSkill ): number | null {
	if ( !skill.live || skill.level === 0 || !skill.action && !skill.slotted ) return null;
	if ( design.family === "Mark Prey" ) return state.rows[design.row]?.left ?? 0;
	const c = state.counters;
	if ( !c ) return null;
	if ( design.family === "Unbroken Spirit" ) return c.resolve;
	if ( design.family === "Crushing Paw" ) return c.bearHits;
	if ( design.family === "Murder of Crows" ) {
		return c.attacks % (design.option > 1 || skill.effectiveLevel >= PET_DESIGN.rules.rank2Level ? 8 : 10);
	}
	return null;
}

/*
================
petStackQuads

Same authored tab and numerals as skill levels, placed at the top right.
================
*/
export function petStackQuads( count: number | null, r: UiRect, clip: UiRect ): UiQuad[] {
	if ( count === null ) return [];
	const root = "/assets/images/Media_extracted/interface/skill/";
	const base = { uv: [ 0, 0, 1, 1 ] as UiRect, color: [ 1, 1, 1, 1 ] as const, clip };
	return [
		{ ...base, rect: [ r[0] + 18, r[1] - 2, 16, 16 ], texture: root + "skl_level_tab.png" },
		...String( count ).split( "" ).map( ( digit, i ): UiQuad => ({
			...base,
			rect: [ r[0] + (count < 10 ? 26 : 23 + i * 5), r[1] + 2, 8, 8 ],
			texture: root + "skl_lv_number_" + digit + ".png"
		}) )
	];
}

/*
================
petRowHelp
================
*/
export function petRowHelp( state: PetTraining, row: number, now: number ): string {
	const skills = state.skills.filter( s => PET_DESIGN.treeSkills.find( d => d.id === s.id )?.row === row );
	const chosen = skills.find( s => s.slotted || s.queued );
	const d = PET_DESIGN.treeSkills.find( entry => entry.id === (chosen ?? skills[0])?.id );
	const lines = [
		d?.family ?? "Pet skills",
		...(row < 2 ?
			[
				"Click to cast.",
				"Drag to numbered quickslots.",
				"Row books share recharge.",
				"Recharge: " + (petRowCooldown( state, row, now ) || "Ready")
			] :
			[
				"Click to select a learned book.",
				"One passive book per row.",
				chosen ? "Active: " + d?.name : "No passive active"
			]),
		"Unlocks at pet 10 / 40 / 90.",
		"Master each book to advance."
	];
	if ( d?.family === "Crushing Paw" ) {
		lines.push( `Bear hits: ${state.counters?.bearHits ?? 0} / 10` );
		if ( d.option > 1 ) lines.push( `Owner hits: ${state.counters?.ownerHits ?? 0} / 10` );
		lines.push(
			"Top right: bear hits.",
			"Physical/spell hits; shields count.",
			"Blocks and DoTs do not count.",
			"Unique monsters resist stun."
		);
	}
	if ( d?.family === "Unbroken Spirit" ) {
		lines.push(
			"Top right: Resolve stacks.",
			"Landed monster hits grant stacks.",
			"Blocks and DoTs do not count.",
			"Expiry/cast removes bonus HP."
		);
	}
	if ( d?.family === "Mark Prey" ) lines.push( "Top right: attacks to recharge." );
	if ( d?.family === "Murder of Crows" ) lines.push( "Top right: attacks toward burst." );
	if ( d?.pet === "Bear" ) lines.push( "PvE only; unavailable in town.", "No jobs, PvP or instances." );
	if ( d?.family === "Challenging Roar" ) {
		lines.push( "No target required; no damage.", "Unique monsters resist taunt." );
	}
	if ( d?.family === "Guardian's Bond" ) {
		lines.push(
			"Revival recharge is shared.",
			"Persists through resummon/relog.",
			"Damage transfer follows shields."
		);
	}
	return lines.join( "\n" );
}
