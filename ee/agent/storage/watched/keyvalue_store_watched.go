// Package watched allows watching a types.KVStore for modification.
//
// Only create one KVStore instance per store watched by a process. See Changed()
// for details.
package watched

import (
	"sync/atomic"

	"github.com/kolide/launcher/v2/ee/agent/types"
)

var _ types.WatchedKVStore = (*watchedKeyValueStore)(nil)

type watchedKeyValueStore struct {
	types.KVStore
	changed atomic.Pointer[chan struct{}]
}

// NewStore decorates a types.KVStore to be a types.WatchedKVStore, allowing
// opt-in notification of changes to the store.
func NewStore(inner types.KVStore) *watchedKeyValueStore {
	store := &watchedKeyValueStore{
		KVStore: inner,
	}
	notify := make(chan struct{})
	store.changed.Store(&notify)
	return store
}

// Changed returns a oneshot channel that closes after the first store modification
// through this instance's API. Detecting further changes requires obtaining a new
// channel.
//
// Modifications outside of this process or instance's APIs do not close
// the channel returned.
func (s *watchedKeyValueStore) Changed() <-chan struct{} {
	return *s.changed.Load()
}

func (s *watchedKeyValueStore) notify() {
	next := make(chan struct{})
	old := s.changed.Swap(&next)
	close(*old)
}

func (s *watchedKeyValueStore) Set(key, value []byte) error {
	if err := s.KVStore.Set(key, value); err != nil {
		return err
	}
	s.notify()
	return nil
}

func (s *watchedKeyValueStore) Delete(keys ...[]byte) error {
	if err := s.KVStore.Delete(keys...); err != nil {
		return err
	}
	s.notify()
	return nil
}

func (s *watchedKeyValueStore) DeleteAll() error {
	if err := s.KVStore.DeleteAll(); err != nil {
		return err
	}
	s.notify()
	return nil
}

func (s *watchedKeyValueStore) Update(kvPairs map[string]string) ([]string, error) {
	deleted, err := s.KVStore.Update(kvPairs)
	if err != nil {
		return deleted, err
	}
	s.notify()
	return deleted, nil
}

func (s *watchedKeyValueStore) AppendValues(values ...[]byte) error {
	if err := s.KVStore.AppendValues(values...); err != nil {
		return err
	}
	s.notify()
	return nil
}
