/*
===========================================================================
spirit.go - port-only Unbroken Spirit's bounded HP and tier rules.
===========================================================================
*/
package companion

import "opensro.online/server/internal/domain"

const ResolveLimit uint8 = 10

/*
================
Spirit
================
*/
func Spirit(pet *domain.CharacterCOS) (SkillDef, uint8, bool) {
	return Passive(pet, "Unbroken Spirit", ModifiersFor(pet))
}

/*
================
SpiritHPPercent
================
*/
func SpiritHPPercent(pet *domain.CharacterCOS) float64 {
	skill, level, ok := Spirit(pet)
	if !ok {
		return 0
	}
	return float64(min(pet.ResolveStacks, ResolveLimit)) * skill.ValueAt(level)
}
