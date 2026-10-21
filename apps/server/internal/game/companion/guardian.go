/*
===========================================================================
guardian.go - Guardian's Bond recovery comes from the first learned book
===========================================================================
*/
package companion

import "opensro.online/server/internal/domain"

/*
================
GuardianRevival

Tier I: 10..100% HP/EXP restoration and 600..300 seconds. Later books
require the first maxed, so inherit its recovery without resetting it.
================
*/
func GuardianRevival(pet *domain.CharacterCOS) (uint8, int64) {
	for _, skill := range SkillsForPet(pet) {
		if skill.EffectName() == "Guardian's Bond" && skill.Option == 1 {
			rank := min(uint8(10), EffectiveLevel(pet, skill, ModifiersFor(pet)))
			if rank == 0 {
				return 0, 600000
			}
			return rank, 600000 - int64(rank-1)*300000/9
		}
	}
	return 0, 600000
}
