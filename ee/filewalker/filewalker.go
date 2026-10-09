package filewalker

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/fs"
	"iter"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kolide/launcher/v2/ee/agent/types"
	"github.com/kolide/launcher/v2/ee/observability"
)

// filewalker performs filewalks at the configured interval, storing results in its resultsStore.
type filewalker struct {
	// Configuration
	name string
	spec walkSpec

	// Internals
	slogger      *slog.Logger
	resultsStore types.GetterSetterDeleter
}

func newFilewalker(spec walkSpec, resultsStore types.GetterSetterDeleter, slogger *slog.Logger) *filewalker {
	names := make([]string, 0, len(spec.matchers))
	for _, m := range spec.matchers {
		names = append(names, m.name)
	}
	name := strings.Join(names, ",")
	return &filewalker{
		name:         name,
		spec:         spec,
		slogger:      slogger.With("filewalker_name", name),
		resultsStore: resultsStore,
	}
}

func overlayFiltersMatch(overlayFilters map[string]string, goos string) bool {
	// Currently, the only filter we expect is for OS.
	if filterGoos, goosFound := overlayFilters["goos"]; goosFound {
		return filterGoos == goos
	}
	return false
}

// Filewalk executes a filewalk with the configured settings, and then stores the results and walk time.
func (f *filewalker) Filewalk(ctx context.Context) {
	ctx, span := observability.StartSpan(ctx, "filewalk_name", f.name)
	defer span.End()

	walkStart := time.Now()
	results := make(map[string][]string, len(f.spec.matchers))
	for _, m := range f.spec.matchers {
		results[m.name] = make([]string, 0)
	}
	errorCounts := make(map[string]int)
	var pathsWalked, dirsSkipped int

	for _, match := range f.spec.roots {
		// active holds the matchers enabled for path; what we return is what path's children get.
		if err := walkDirWithState(match, nil, func(path string, d fs.DirEntry, err error, activeMatchers matcherSet) (matcherSet, error) {
			if err != nil {
				key, expected := classifyWalkError(err)
				errorCounts[key]++
				if !expected {
					f.slogger.Log(ctx, slog.LevelWarn,
						"error while filewalking",
						"start_dir", match,
						"path", path,
						"err", err,
					)
				}
				return nil, nil
			}
			if ctx.Err() != nil {
				f.slogger.Log(ctx, slog.LevelDebug, "file walk interrupted by context error")
				return nil, filepath.SkipAll
			}

			pathsWalked++

			// Nested roots switch their matchers on partway down.
			activeMatchers = activeMatchers.with(f.spec.activate[canonicalize(path)])

			// Prune skipped directories before any other filter, so that we don't descend unnecessarily
			if d.IsDir() {
				activeMatchers = activeMatchers.withoutSkipping(path)
				// Keep descending with nothing active if another config's root lies below.
				if len(activeMatchers) == 0 && !f.spec.hasRootBelow(path) {
					dirsSkipped++
					return nil, fs.SkipDir
				}
			}

			// Add this file to our results
			for m := range activeMatchers.matching(path, d) {
				results[m.name] = append(results[m.name], path)
			}
			return activeMatchers, nil
		}); err != nil {
			// Log error, but continue on to process other root dirs
			f.slogger.Log(ctx, slog.LevelError,
				"could not complete filewalk in directory",
				"start_dir", match,
				"err", err,
			)
		}
		if ctx.Err() != nil {
			return
		}
	}

	span.AddEvent("walk_complete")

	filesMatched := make(map[string]int, len(results))
	for _, m := range f.spec.matchers {
		f.storeResults(ctx, m.name, results[m.name])
		filesMatched[m.name] = len(results[m.name])
	}

	span.AddEvent("walk_results_stored")

	f.slogger.Log(ctx, slog.LevelInfo,
		"completed filewalk",
		"walk_duration", time.Since(walkStart).String(),
		"error_counts", errorCounts,
		"paths_walked", pathsWalked,
		"dirs_skipped", dirsSkipped,
		"files_matched", filesMatched,
	)
}

// storeResults stores one matcher's results, then its last walk time.
func (f *filewalker) storeResults(ctx context.Context, name string, paths []string) {
	resultsRaw, err := json.Marshal(paths)
	if err != nil {
		f.slogger.Log(ctx, slog.LevelError,
			"could not marshal filewalk results for storage",
			"err", err,
		)
		return
	}
	if err := f.resultsStore.Set([]byte(name), resultsRaw); err != nil {
		f.slogger.Log(ctx, slog.LevelError,
			"could not set filewalk results in storage",
			"err", err,
		)
		return
	}

	// Since we've successfully walked and stored the results, store the last walk time
	lastWalkTimeBuffer := &bytes.Buffer{}
	if err := binary.Write(lastWalkTimeBuffer, binary.NativeEndian, time.Now().Unix()); err != nil {
		f.slogger.Log(ctx, slog.LevelError,
			"could not convert last walk timestamp to bytes",
			"err", err,
		)
		return
	}
	if err := f.resultsStore.Set(LastWalkTimeKey(name), lastWalkTimeBuffer.Bytes()); err != nil {
		f.slogger.Log(ctx, slog.LevelError,
			"could not set last walk time in storage",
			"err", err,
		)
	}
}

// LastWalkTimeKey gives the key to query the results store to retrieve the last walk time for the given filewalker.
func LastWalkTimeKey(filewalkName string) []byte {
	return fmt.Appendf(nil, "%s_last_walk", filewalkName)
}

// matcherSet is never modified in place; methods return a new set when it changes.
// Sets are shared between a directory and its children during a walk.
type matcherSet []*matcher

// with returns s plus add, or s itself if add contributes nothing.
func (s matcherSet) with(add matcherSet) matcherSet {
	if len(add) == 0 {
		return s
	}
	out := slices.Clone(s)
	for _, m := range add {
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

// withoutSkipping returns s minus matchers that skip dir, or s itself if none do.
func (s matcherSet) withoutSkipping(dir string) matcherSet {
	skips := func(m *matcher) bool { return m.shouldSkipDir(dir) }
	if !slices.ContainsFunc(s, skips) {
		return s
	}
	return slices.DeleteFunc(slices.Clone(s), skips)
}

func (s matcherSet) matching(path string, d fs.DirEntry) iter.Seq[*matcher] {
	return func(yield func(*matcher) bool) {
		for _, m := range s {
			if m.matches(path, d) && !yield(m) {
				return
			}
		}
	}
}

func (m *matcher) shouldSkipDir(dir string) bool {
	for _, skipDirRegex := range m.skipDirs {
		if skipDirRegex.MatchString(dir) {
			return true
		}
	}
	return false
}

func (m *matcher) matches(path string, d fs.DirEntry) bool {
	if m.fileType != nil && !m.fileType.matches(d.Type()) {
		return false
	}

	if m.fileName != nil && !m.fileName.MatchString(filepath.Base(path)) {
		return false
	}

	return true
}
