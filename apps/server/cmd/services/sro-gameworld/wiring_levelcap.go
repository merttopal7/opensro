/*
===========================================================================

wiring_levelcap.go - the optional level cap on the progression runtime

wiring_gameplay.go is at the source-size limit, so the SRO_LEVEL_CAP lookup
lives here. The native cap is progression.LevelCap; the 110 cap is port-only,
not native, and ships off.

===========================================================================
*/
package main

import (
	log "github.com/sirupsen/logrus"

	"opensro.online/server/internal/game/progression"
)

/*
================
applyLevelCap

Sets the runtime's level limit from the environment and says so when it
differs from the native cap.
================
*/
func applyLevelCap(stats *progression.Runtime) error {
	limit, err := progression.LevelCapFromEnv()
	if err != nil {
		return err
	}
	stats.LevelLimit = limit
	if limit != progression.LevelCap {
		log.Infof("progression: level cap %d with the installed v1.150 curve (%s)", limit, progression.EnvLevelCap)
	}
	return nil
}
