package filewalker

// Adapted from filepath.WalkDir (Go 1.26): Copyright 2009 The Go Authors, BSD-style license.
// The only change is threading state: fn returns the state its directory's children receive.

import (
	"io/fs"
	"os"
	"path/filepath"
)

type walkDirWithStateFunc[S any] func(path string, d fs.DirEntry, err error, state S) (S, error)

func walkDirWithState[S any](root string, state S, fn walkDirWithStateFunc[S]) error {
	info, err := os.Lstat(root)
	if err != nil {
		_, err = fn(root, nil, err, state)
	} else {
		err = walkDirWithStateAt(root, fs.FileInfoToDirEntry(info), state, fn)
	}
	if err == filepath.SkipDir || err == filepath.SkipAll {
		return nil
	}
	return err
}

func walkDirWithStateAt[S any](path string, d fs.DirEntry, state S, fn walkDirWithStateFunc[S]) error {
	childState, err := fn(path, d, nil, state)
	if err != nil || !d.IsDir() {
		if err == filepath.SkipDir && d.IsDir() {
			// Successfully skipped directory.
			err = nil
		}
		return err
	}

	dirs, err := os.ReadDir(path)
	if err != nil {
		// Second call, to report ReadDir error. Receives the same state as the first.
		_, err = fn(path, d, err, state)
		if err != nil {
			if err == filepath.SkipDir && d.IsDir() {
				err = nil
			}
			return err
		}
	}

	for _, d1 := range dirs {
		path1 := filepath.Join(path, d1.Name())
		if err := walkDirWithStateAt(path1, d1, childState, fn); err != nil {
			if err == filepath.SkipDir {
				break
			}
			return err
		}
	}
	return nil
}
