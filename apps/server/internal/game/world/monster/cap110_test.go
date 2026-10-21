/*
===========================================================================
cap110_test.go - optional population admission and native-off boundaries.
===========================================================================
*/
package monster

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"opensro.online/server/internal/game/world/alexandria"
)

/*
================
TestCap110PopulationJoinsInstalledReferences
================
*/
func TestCap110PopulationJoinsInstalledReferences(t *testing.T) {
	const id = 1234
	ref := MonsterRef{RefObjID: id, Codename: "MOB_SD_TEST", Level: 105}
	native := TemplateFromParts(map[uint32]MonsterRef{id: ref}, []NestRow{{MaxCount: 1}})
	control := TacticsControls{ObjectID: 9999, ID: 77, ChangeTarget: 2, SightRange: 130, ChampionID: 78}
	champion := control
	champion.ID, champion.ChampionID = 78, 0
	source := struct {
		Version int
		Rows    []cap110Nest
	}{1, []cap110Nest{{Codename: ref.Codename,
		NestID: 9, RegionID: 0x534b, X: 100, Y: 25, Z: 200, Radius: 350, GenerateRadius: 300,
		MaxCount: 4, RespawnDelayMinSec: 8, RespawnDelayMaxSec: 10, Respawn: true,
		ChampionGenPercentage: 10, Controls: control, Champion: &champion}}}
	data, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	got, err := appendCap110Data(native, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(native.Nests) != 1 || len(got.Nests) != 2 {
		t.Fatal("population did not append immutably")
	}
	nest := got.Nests[1]
	if nest.RefObjID != id || nest.MaxCount != 4 || !nest.HasChampionTactics || nest.ChampionTactics.Controls.ID != 78 || nest.TargetPolicy != 2 || !nest.Aggressive {
		t.Fatalf("authored population/tactics lost: %+v", nest)
	}
	if _, err := AppendCap110(native, t.TempDir(), false); err != nil {
		t.Fatalf("off read optional data: %v", err)
	}
	t.Setenv(alexandria.EnvAlexandria, "on")
	if _, err := AppendCap110(native, t.TempDir(), true); err == nil {
		t.Fatal("on admitted missing input")
	}
	// The nests stand in Alexandria's regions: valid data is refused while
	// the city switch is off.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, cap110PopulationFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendCap110(native, dir, true); err != nil {
		t.Fatalf("on with the city: %v", err)
	}
	t.Setenv(alexandria.EnvAlexandria, "")
	if _, err := AppendCap110(native, dir, true); err == nil || !strings.Contains(err.Error(), alexandria.EnvAlexandria) {
		t.Fatalf("on without the city admitted: %v", err)
	}
	source.Rows = append(source.Rows, source.Rows[0])
	data, err = json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appendCap110Data(native, data); err == nil {
		t.Fatal("duplicate source nest admitted")
	}
}

/*
================
TestCap110PopulationRejectsMalformedData
================
*/
func TestCap110PopulationRejectsMalformedData(t *testing.T) {
	for _, data := range []string{`{"Version":2,"Rows":[]}`, `{"Version":1,"Rows":[],"Unknown":1}`, `{"Version":1,"Rows":[]} {}`} {
		if _, err := appendCap110Data(Template{}, []byte(data)); err == nil {
			t.Fatal("malformed optional data admitted")
		}
	}
}

/*
================
TestCap110ApisKeepsSourceGradeAndRespawn
================
*/
func TestCap110ApisKeepsSourceGradeAndRespawn(t *testing.T) {
	ref := MonsterRef{RefObjID: 1234, Codename: "MOB_SD_APIS", Level: 103, MonsterType: 8, MaxHP: 21068995}
	template := TemplateFromParts(map[uint32]MonsterRef{ref.RefObjID: ref}, nil)
	source := struct {
		Version int
		Rows    []cap110Nest
	}{1, []cap110Nest{{Codename: ref.Codename, NestID: 29205, RegionID: 19017,
		X: 1578.03, Y: 184, Z: 1780.5601, Radius: 600, GenerateRadius: 400, MaxCount: 1,
		Respawn: true, RespawnDelayMinSec: 10800, RespawnDelayMaxSec: 21600,
		Controls: TacticsControls{ChangeTarget: 2, SightRange: 130}}}}
	data, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	got, err := appendCap110Data(template, data)
	if err != nil {
		t.Fatal(err)
	}
	nest := got.Nests[0]
	if got.Refs[ref.RefObjID].MonsterType != 8 || nest.MaxCount != 1 || nest.HasChampionTactics ||
		nest.RespawnDelayMinSec != 10800 || nest.RespawnDelayMaxSec != 21600 {
		t.Fatalf("Apis source values lost: %+v", nest)
	}
	ref.Codename = "MOB_SD_OTHER_UNIQUE"
	source.Rows[0].Codename = ref.Codename
	data, err = json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appendCap110Data(TemplateFromParts(map[uint32]MonsterRef{ref.RefObjID: ref}, nil), data); err == nil {
		t.Fatal("unselected special monster admitted")
	}
}
