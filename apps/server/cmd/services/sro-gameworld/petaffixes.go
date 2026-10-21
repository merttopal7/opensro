/*
===========================================================================

petaffixes.go - apply the shard DB's blue roll policy to future pet loot

===========================================================================
*/
package main

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"opensro.online/server/internal/data/store"
	"opensro.online/server/internal/game/action"
	"opensro.online/server/internal/game/companion"
)

/*
================
loadPetGearAffixes
================
*/
func loadPetGearAffixes(authority *store.Store) ([]companion.AffixDef, error) {
	affixes := append([]companion.AffixDef(nil), companion.D.Affixes...)
	defaults := make([]store.PetAffixRule, 0, len(affixes))
	for _, a := range affixes {
		maximum := a.Max
		if a.Group == "Cooldown" && a.Tier == 3 {
			maximum = 50
		}
		defaults = append(defaults, store.PetAffixRule{Code: a.Code, Name: a.Name, Group: a.Group,
			Minimum: a.Min, Maximum: maximum, Weight: a.Weight, RollPower: 3})
	}
	rules, err := authority.PetAffixRules(defaults)
	if err != nil {
		return nil, err
	}
	for i, r := range rules {
		affixes[i].Min, affixes[i].Max, affixes[i].Weight, affixes[i].RollPower = r.Minimum, r.Maximum, r.Weight, r.RollPower
	}
	return affixes, nil
}

/*
================
loadPetGearPolicy

DB balance belongs to the pet policy composition, not general gameplay wiring.
================
*/
func loadPetGearPolicy(items *action.Runtime, authorityStore *store.Store) error {
	var err error
	items.PetGearDropFactor, err = authorityStore.PetGearDropFactor()
	if err != nil {
		return fmt.Errorf("pet loot rules: %w", err)
	}
	log.Infof("pet loot: DB factor %.4g per normal equipment drop", items.PetGearDropFactor)
	items.PetGearAffixes, err = loadPetGearAffixes(authorityStore)
	if err != nil {
		return fmt.Errorf("pet blue rules: %w", err)
	}
	return nil
}
