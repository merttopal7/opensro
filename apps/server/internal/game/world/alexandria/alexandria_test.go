/*
===========================================================================

alexandria_test.go - the SRO_ALEXANDRIA switch and its overlay

===========================================================================
*/
package alexandria

import (
	"os"
	"path/filepath"
	"testing"
)

const testOverlay = `{"format":"sro-server-alexandria-overlay","version":1,` +
	`"regions":[23088],"teleports":["GATE_SD1"],"buildings":["STORE_SD_GATE1"]}`

/*
================
writeOverlay
================
*/
func writeOverlay(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, OverlayFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

/*
================
TestEnabledFromEnvAcceptsOnlyKnownValues
================
*/
func TestEnabledFromEnvAcceptsOnlyKnownValues(t *testing.T) {
	for value, want := range map[string]bool{"": false, "0": false, "off": false, "FALSE": false,
		"1": true, " on ": true, "true": true} {
		t.Setenv(EnvAlexandria, value)
		if got, err := EnabledFromEnv(); err != nil || got != want {
			t.Fatalf("%q = %v, %v; want %v", value, got, err, want)
		}
	}
	t.Setenv(EnvAlexandria, "yes")
	if _, err := EnabledFromEnv(); err == nil {
		t.Fatal("an unknown value must be refused")
	}
}

/*
================
TestCheckRequiresTheOverlayOnlyWhenOn
================
*/
func TestCheckRequiresTheOverlayOnlyWhenOn(t *testing.T) {
	native, variant := t.TempDir(), writeOverlay(t, testOverlay)
	for _, tc := range []struct {
		value, dir string
		ok         bool
	}{
		{"", native, true}, {"", variant, true}, {"on", variant, true},
		{"on", native, false}, {"maybe", variant, false},
	} {
		t.Setenv(EnvAlexandria, tc.value)
		if err := Check(tc.dir); (err == nil) != tc.ok {
			t.Fatalf("SRO_ALEXANDRIA=%q over %s: %v", tc.value, tc.dir, err)
		}
	}
	for _, body := range []string{`{`, `{"format":"other","version":1}`,
		`{"format":"sro-server-alexandria-overlay","version":1,"regions":[],"teleports":["X"]}`,
		`{"format":"sro-server-alexandria-overlay","version":1,"regions":[1],"teleports":["X"],"extra":1}`} {
		t.Setenv(EnvAlexandria, "")
		if err := Check(writeOverlay(t, body)); err == nil {
			t.Fatalf("malformed overlay %s accepted", body)
		}
	}
}

/*
================
TestHiddenAnswersEachKind
================
*/
func TestHiddenAnswersEachKind(t *testing.T) {
	dir := writeOverlay(t, testOverlay)
	for _, value := range []string{"", "off", "typo"} {
		t.Setenv(EnvAlexandria, value)
		hidden, err := Hidden(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !hidden.HidesRegion(23088) || !hidden.HidesTeleport("GATE_SD1") || !hidden.HidesBuilding("STORE_SD_GATE1") {
			t.Fatalf("SRO_ALEXANDRIA=%q must hide the overlay", value)
		}
		if hidden.HidesRegion(25000) || hidden.HidesTeleport("GATE_CH") || hidden.HidesBuilding("STORE_CH_GATE") {
			t.Fatalf("SRO_ALEXANDRIA=%q hid native content", value)
		}
	}
	t.Setenv(EnvAlexandria, "on")
	if hidden, err := Hidden(dir); err != nil || hidden.HidesRegion(23088) {
		t.Fatalf("on must hide nothing: %v", err)
	}
	t.Setenv(EnvAlexandria, "")
	if hidden, err := Hidden(t.TempDir()); err != nil || hidden != nil {
		t.Fatalf("native data has no overlay: %v %v", hidden, err)
	}
}
