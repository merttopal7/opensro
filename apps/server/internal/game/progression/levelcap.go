/*
===========================================================================

levelcap.go - optional 110-cap progression (port-only, not native)

Only the stopping level changes. The installed v1.150 curve remains the
shared server/client curve; importing a different curve requires a separate
data conversion. Unset or off retains the native cap. Read configuration
at composition time, never during a character transaction.

===========================================================================
*/
package progression

import (
	"fmt"
	"os"
	"strings"
)

const EnvLevelCap = "SRO_LEVEL_CAP"
const ExpansionLevelCap int64 = 110

/*
================
LevelCapFromEnv

Reject unsupported values rather than silently starting at the wrong cap.
================
*/
func LevelCapFromEnv() (int64, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvLevelCap))) {
	case "", "0", "off", "false":
		if strings.TrimSpace(os.Getenv("SRO_MAX_LEVEL")) != "" {
			return CurrentLevelCap(), nil
		}
		return LevelCap, nil
	case "90":
		return LevelCap, nil
	case "110":
		return ExpansionLevelCap, nil
	default:
		return 0, fmt.Errorf("%s must be off, 90 or 110", EnvLevelCap)
	}
}

/*
================
levelCap

A standalone runtime's zero value keeps the native stopping level.
================
*/
func (rt *Runtime) levelCap() int64 {
	if v := activeLevelCap.Load(); v >= 1 && v <= 140 {
		return v
	}
	if rt.LevelLimit >= 1 && rt.LevelLimit <= 140 {
		return rt.LevelLimit
	}
	return CurrentLevelCap()
}
