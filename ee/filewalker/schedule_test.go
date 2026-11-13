package filewalker

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// dueStrict should return config names immediately ready to walk.
func Test_dueStrict(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_000_000, 0)
	cfgs := map[string]filewalkConfig{
		"never_walked": {WalkInterval: duration(time.Hour)},
		"future":       {WalkInterval: duration(time.Hour)},
		"not_due":      {WalkInterval: duration(time.Hour)},
		"elapsed":      {WalkInterval: duration(time.Hour)},
	}
	lastWalks := map[string]time.Time{
		"future":  now.Add(24 * time.Hour), // clock moved back
		"not_due": now.Add(-59 * time.Minute),
		"elapsed": now.Add(-time.Hour),
	}

	require.ElementsMatch(t, []string{"elapsed", "future", "never_walked"}, dueStrict(now, cfgs, lastWalks))
}

func Test_dueDiscrete(t *testing.T) {
	t.Parallel()

	epoch := time.Unix(0, 0).Add(24_000 * time.Hour)
	for _, tt := range []struct {
		name string
		// current time offset relative to epoch
		now       time.Duration
		intervals map[string]time.Duration
		// the relative time from epoch last walked
		lastWalks map[string]time.Duration
		due       []string
	}{
		{
			name: "no cfgs",
		},
		{
			name:      "never walked",
			intervals: map[string]time.Duration{"a": time.Hour},
			due:       []string{"a"},
		},
		{
			name:      "one interval too early",
			now:       55 * time.Minute,
			intervals: map[string]time.Duration{"a": time.Hour},
			lastWalks: map[string]time.Duration{"a": 5 * time.Minute},
		},
		{
			name:      "one interval running late",
			now:       5 * time.Minute,
			intervals: map[string]time.Duration{"a": time.Hour},
			lastWalks: map[string]time.Duration{"a": -5 * time.Minute},
			due:       []string{"a"},
		},
		{
			name:      "two longer not due",
			now:       time.Hour + 5*time.Minute,
			intervals: map[string]time.Duration{"a": time.Hour, "b": 3 * time.Hour},
			lastWalks: map[string]time.Duration{"a": 5 * time.Minute, "b": 5 * time.Minute},
			due:       []string{"a"},
		},
		{
			name:      "two coalesced both are due",
			now:       5 * time.Minute,
			intervals: map[string]time.Duration{"a": time.Hour, "b": 3 * time.Hour},
			lastWalks: map[string]time.Duration{"a": -5 * time.Minute, "b": -5 * time.Minute},
			due:       []string{"a", "b"},
		},
		{
			name:      "overdue with one waiting for tick",
			now:       30 * time.Minute,
			intervals: map[string]time.Duration{"a": time.Hour, "b": 3 * time.Hour},
			lastWalks: map[string]time.Duration{"a": 5 * time.Minute, "b": -10 * time.Hour},
		},
		{
			name:      "non-divisible snaps down",
			now:       5 * time.Minute,
			intervals: map[string]time.Duration{"a": 2 * time.Hour, "b": 9 * time.Hour},
			lastWalks: map[string]time.Duration{"a": -5 * time.Minute, "b": -5 * time.Minute},
			due:       []string{"a", "b"},
		},
		{
			name:      "zero interval does something no crash",
			now:       10 * time.Minute,
			intervals: map[string]time.Duration{"a": 0, "b": time.Hour},
			lastWalks: map[string]time.Duration{"a": 5 * time.Minute, "b": 5 * time.Minute},
			due:       []string{"a"},
		},
		{
			name:      "negative interval does something no crash",
			now:       10 * time.Minute,
			intervals: map[string]time.Duration{"a": -time.Hour, "b": time.Hour},
			lastWalks: map[string]time.Duration{"a": 5 * time.Minute, "b": 5 * time.Minute},
			due:       []string{"a"},
		},
		{
			name:      "future last walk still due",
			now:       5 * time.Minute,
			intervals: map[string]time.Duration{"a": time.Hour, "b": time.Hour},
			lastWalks: map[string]time.Duration{"a": 24 * time.Hour, "b": -5 * time.Minute},
			due:       []string{"a", "b"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfgs := make(map[string]filewalkConfig, len(tt.intervals))
			for name, interval := range tt.intervals {
				cfgs[name] = filewalkConfig{WalkInterval: duration(interval)}
			}
			lastWalks := make(map[string]time.Time, len(tt.lastWalks))
			for name, offset := range tt.lastWalks {
				lastWalks[name] = epoch.Add(offset)
			}

			require.ElementsMatch(t, tt.due, dueDiscrete(epoch.Add(tt.now), cfgs, lastWalks))
		})
	}
}

func Test_dueDiscrete_coalesces(t *testing.T) {
	t.Parallel()

	epoch := time.Unix(0, 0).Add(24_000 * time.Hour)
	cfgs := map[string]filewalkConfig{
		"a": {WalkInterval: duration(2 * time.Hour)},
		"b": {WalkInterval: duration(9 * time.Hour)},
	}
	lastWalks := map[string]time.Time{}
	runs := map[string]int{}
	for now := epoch; now.Before(epoch.Add(24 * time.Hour)); now = now.Add(walkCheckInterval) {
		names := dueDiscrete(now, cfgs, lastWalks)
		if slices.Contains(names, "b") {
			require.Contains(t, names, "a", "b ran without a at %s", now.Sub(epoch))
		}
		for _, name := range names {
			lastWalks[name] = now
			runs[name]++
		}
	}

	require.Equal(t, map[string]int{"a": 12, "b": 3}, runs)
}
