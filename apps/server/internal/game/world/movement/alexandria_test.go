/*
===========================================================================

alexandria_test.go - movement authority with SRO_ALEXANDRIA off and on

Port-only, not native. A hidden overlay region is absent from movement as a
native build's missing region is; on, the same region resolves normally.

===========================================================================
*/
package movement

import (
	"os"
	"path/filepath"
	"testing"

	"opensro.online/server/internal/game/world/alexandria"
)

/*
================
TestAlexandriaRegionsFollowTheFlag
================
*/
func TestAlexandriaRegionsFollowTheFlag(t *testing.T) {
	textdata := t.TempDir()
	overlay := `{"format":"sro-server-alexandria-overlay","version":1,` +
		`"regions":[27471],"teleports":["GATE_SD1"],"buildings":["STORE_SD_GATE1"]}`
	if err := os.WriteFile(filepath.Join(textdata, alexandria.OverlayFile), []byte(overlay), 0o644); err != nil {
		t.Fatal(err)
	}
	root := syntheticHeightRoot(t)
	for _, tc := range []struct {
		value   string
		visible bool
	}{{"", false}, {"off", false}, {"on", true}} {
		t.Setenv(alexandria.EnvAlexandria, tc.value)
		hidden, err := alexandria.Hidden(textdata)
		if err != nil {
			t.Fatal(err)
		}
		validator := NewWaterValidator(root)
		validator.HideAlexandria(hidden)
		_, height := validator.TerrainHeightAt(0x6B4F, 100, 100)
		if height != tc.visible || validator.SpawnRegionAvailable(0x6B4F) != tc.visible {
			t.Fatalf("SRO_ALEXANDRIA=%q: height %v, spawn %v, want %v",
				tc.value, height, validator.SpawnRegionAvailable(0x6B4F), tc.visible)
		}
	}
}
