package filewalker

import (
	"math"
	"time"
)

// The sole non-default due strategy, dueStrict
const dueStrategyStrict = "strict"

// dueStrict returns the filewalkConfig names which are immediately due for walking. Anything
// last walked in the future, not walked, or failing to parse when it walked are included.
func dueStrict(now time.Time, cfgs map[string]filewalkConfig, lastWalks map[string]time.Time) []string {
	var names []string
	for name, cfg := range cfgs {
		elapsed := now.Sub(lastWalks[name])
		if elapsed < 0 || elapsed >= time.Duration(cfg.WalkInterval) {
			names = append(names, name)
		}
	}
	return names
}

// dueDiscrete returns filewalkConfig names to walk. The minimum walk interval is used to coalesce
// due walks: all intervals may only execute when minimum interval does. Any intervals which do not divide
// evenly are rounded down and ran sooner. Walks without a lastWalks entry run immediately.
//
// Works well when cfgs have roots with heavy overlap.
func dueDiscrete(now time.Time, cfgs map[string]filewalkConfig, lastWalks map[string]time.Time) []string {
	base := int64(math.MaxInt64)
	for _, cfg := range cfgs {
		base = min(base, int64(cfg.WalkInterval))
	}
	// weird, so do something boring
	if base <= 0 {
		return dueStrict(now, cfgs, lastWalks)
	}

	// the last time any walk ran
	var lastWalk int64
	for _, walkTime := range lastWalks {
		// clock drift or changes
		if now.Sub(walkTime) < 0 {
			continue
		}
		lastWalk = max(lastWalk, walkTime.UnixNano())
	}

	var names []string
	for name, cfg := range cfgs {
		if _, ok := lastWalks[name]; !ok {
			// new walk
			names = append(names, name)
			continue
		}

		// snaps interval to be on our minimum walk's runs, rounded down
		// since every run occurs in base-sized ticks, we count if
		// any ticks have been missed
		period := int64(cfg.WalkInterval) / base * base
		if now.UnixNano()/period > lastWalk/period {
			names = append(names, name)
		}
	}

	return names
}
