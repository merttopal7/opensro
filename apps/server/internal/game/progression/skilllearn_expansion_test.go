/*
===========================================================================

skilllearn_expansion_test.go - expansion ranks retain ordinary learning gates

Character level 110 does not waive mastery, prerequisites, or SP spending.
Exercise the production handler above the former skill tier's level 100.

===========================================================================
*/
package progression

import (
	"testing"

	"opensro.online/server/internal/game/enterworld"
	"opensro.online/server/internal/game/item/wire"
)

/*
================
TestExpansionSkillLearningRequiresItsMasteryAndChargesSP
================
*/
func TestExpansionSkillLearningRequiresItsMasteryAndChargesSP(t *testing.T) {
	const expansionRank uint32 = 33001
	character := testCharacter()
	*character.Level = 110
	setMastery(character, chMastery, 100)
	rt := newSkillTestRuntime(character)
	rows := rt.deps.SkillData().(staticSkills)
	rows[expansionRank] = enterworld.SkillRow{
		ID: expansionRank, Group: 9301, Level: 1, SPCost: 10,
		Masteries: [2]enterworld.SkillRequirement{{ID: chMastery, Level: 110}},
	}
	assertSkillRefusal(t, rt.HandleSkillLearn(testDivision, character, skillPayload(expansionRank)), skillLearnMasteryLevelRefusal)
	setMastery(character, chMastery, 110)
	result := rt.HandleSkillLearn(testDivision, character, skillPayload(expansionRank))
	if len(result.Frames) != 2 || result.Frames[0].Payload[0] != wire.ResultSuccess {
		t.Fatalf("expansion skill learn failed: %+v", result.Frames)
	}
	if len(character.Skills) != 1 || character.Skills[0] != expansionRank || *character.SkillPoints != 90 {
		t.Fatalf("learned=%v SP=%d", character.Skills, *character.SkillPoints)
	}
}

/*
================
TestShardLevelLimitIsolatesSkillsAndMasteriesPerShard
================
*/
func TestShardLevelLimitIsolatesSkillsAndMasteriesPerShard(t *testing.T) {
	const cap100Skill uint32 = 33002
	character := testCharacter()
	*character.Level = 100
	setMastery(character, chMastery, 90)
	rt := newSkillTestRuntime(character)
	rt.LevelLimit = 90
	rows := rt.deps.SkillData().(staticSkills)
	rows[cap100Skill] = enterworld.SkillRow{
		ID: cap100Skill, Group: 9302, Level: 1, SPCost: 10,
		Masteries: [2]enterworld.SkillRequirement{{ID: chMastery, Level: 92}},
	}

	// On a 90-cap shard, raising mastery 90 -> 91 is refused even if character level is 100.
	assertMasteryRefusal(t, rt.HandleMasteryLevelUp(testDivision, character, masteryPayload(chMastery, 1)), character, 90)

	// On a 90-cap shard, learning a 92-mastery skill is refused as unavailable even if mastery is 100.
	setMastery(character, chMastery, 100)
	assertSkillRefusal(t, rt.HandleSkillLearn(testDivision, character, skillPayload(cap100Skill)), skillLearnUnavailable)

	// Switching the shard cap to 100 unlocks the 91-100 skill tier immediately.
	rt.LevelLimit = 100
	result := rt.HandleSkillLearn(testDivision, character, skillPayload(cap100Skill))
	if len(result.Frames) != 2 || result.Frames[0].Payload[0] != wire.ResultSuccess {
		t.Fatalf("100-cap shard skill learn failed: %+v", result.Frames)
	}
}
