/*
===========================================================================

alexandria.go - whether the server registers the optional Alexandria city

Port-only, not native. A game root made by build_alexandria_variant.py adds
Alexandria's regions, teleport gates and NPC placements to the shipped
textdata, and the server projection then carries alexandria-overlay.json
beside that textdata (buildServerGameDataBundle.mjs). The data exists either
way; SRO_ALEXANDRIA decides whether the shard registers it:

  - off (the default) hides every overlay region, teleport and gate
    building, so the NPC roster, portal catalogue and movement authority
    equal a native build's;
  - on registers them, and refuses to start without the overlay.

The variant only appends rows (its regions are VSRO-only terrain no native
row uses), so hiding them leaves every native row and identity unchanged.

===========================================================================
*/
package alexandria

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// EnvAlexandria is the shard switch (shardsettings, nomad var alexandria).
	EnvAlexandria = "SRO_ALEXANDRIA"
	// OverlayFile sits in the projection's textdata directory.
	OverlayFile = "alexandria-overlay.json"

	overlayFormat  = "sro-server-alexandria-overlay"
	overlayVersion = 1
	// maxOverlayBytes bounds the read; the real overlay is a few kilobytes.
	maxOverlayBytes = 1 << 20
)

/*
================
Overlay

What the shard hides while the flag is off. A nil *Overlay hides nothing:
the flag is on, or the data is native.
================
*/
type Overlay struct {
	regions   map[uint16]bool
	teleports map[string]bool
	buildings map[string]bool
}

type overlayFileJSON struct {
	Format    string   `json:"format"`
	Version   int      `json:"version"`
	Regions   []uint16 `json:"regions"`
	Teleports []string `json:"teleports"`
	Buildings []string `json:"buildings"`
}

/*
================
EnabledFromEnv

The same on values the importer accepts (import_vsro_alexandria.py
flag_enabled); any other non-empty value is refused, as LevelCapFromEnv
refuses one, so a typo cannot silently run the native shard.
================
*/
func EnabledFromEnv() (bool, error) {
	switch value := strings.ToLower(strings.TrimSpace(os.Getenv(EnvAlexandria))); value {
	case "1", "on", "true":
		return true, nil
	case "", "0", "off", "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s=%q: want on or off", EnvAlexandria, value)
	}
}

/*
================
Check

The boot gate: a valid flag, a readable overlay when present, and the
overlay required while the flag is on.
================
*/
func Check(textdataDir string) error {
	enabled, err := EnabledFromEnv()
	if err != nil {
		return err
	}
	overlay, err := readOverlay(textdataDir)
	if err != nil {
		return err
	}
	if enabled && overlay == nil {
		return fmt.Errorf("%s is on but %s has no %s (build the game data from an Alexandria variant)",
			EnvAlexandria, textdataDir, OverlayFile)
	}
	return nil
}

/*
================
Hidden

What the loaders over textdataDir must skip. A flag value Check would
refuse reads as off, so a loader that runs before the boot gate still
shows only native content.
================
*/
func Hidden(textdataDir string) (*Overlay, error) {
	if enabled, err := EnabledFromEnv(); err == nil && enabled {
		return nil, nil
	}
	return readOverlay(textdataDir)
}

/*
================
readOverlay

A missing file is native data (nil); a present file must parse exactly.
================
*/
func readOverlay(textdataDir string) (*Overlay, error) {
	path := filepath.Join(textdataDir, OverlayFile)
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("alexandria overlay: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxOverlayBytes {
		return nil, fmt.Errorf("alexandria overlay %s: not a regular file of at most %d bytes", path, maxOverlayBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("alexandria overlay: %w", err)
	}
	var raw overlayFileJSON
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("alexandria overlay %s: %w", path, err)
	}
	if raw.Format != overlayFormat || raw.Version != overlayVersion {
		return nil, fmt.Errorf("alexandria overlay %s: format %q v%d", path, raw.Format, raw.Version)
	}
	if len(raw.Regions) == 0 || len(raw.Teleports) == 0 {
		return nil, fmt.Errorf("alexandria overlay %s: no regions or teleports", path)
	}
	overlay := &Overlay{
		regions:   make(map[uint16]bool, len(raw.Regions)),
		teleports: make(map[string]bool, len(raw.Teleports)),
		buildings: make(map[string]bool, len(raw.Buildings)),
	}
	for _, region := range raw.Regions {
		overlay.regions[region] = true
	}
	for _, code := range raw.Teleports {
		overlay.teleports[code] = true
	}
	for _, code := range raw.Buildings {
		overlay.buildings[code] = true
	}
	return overlay, nil
}

/*
================
HidesRegion
================
*/
func (o *Overlay) HidesRegion(region uint16) bool {
	return o != nil && o.regions[region]
}

/*
================
HidesTeleport

A teleportdata row by its codename (column 2).
================
*/
func (o *Overlay) HidesTeleport(code string) bool {
	return o != nil && o.teleports[code]
}

/*
================
HidesBuilding

A teleportbuilding row by its codename (column 2).
================
*/
func (o *Overlay) HidesBuilding(code string) bool {
	return o != nil && o.buildings[code]
}
