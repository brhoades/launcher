package watched

import (
	"testing"

	"github.com/kolide/launcher/v2/ee/agent/storage/inmemory"
	"github.com/kolide/launcher/v2/ee/agent/types"
	"github.com/stretchr/testify/require"
)

func requireClosed(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	default:
		t.Fatal("expected channel to be closed")
	}
}

func requireOpen(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal("expected channel to be open")
	default:
	}
}

// Changes against a WatchedKVStore should close the Changed() channel.
// Subsequent calls return a fresh channel.
func TestChanged_Writes(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		write func(s types.WatchedKVStore) error
	}{
		{name: "Set", write: func(s types.WatchedKVStore) error { return s.Set([]byte("k"), []byte("v")) }},
		{name: "Delete", write: func(s types.WatchedKVStore) error { return s.Delete([]byte("k")) }},
		{name: "DeleteAll", write: func(s types.WatchedKVStore) error { return s.DeleteAll() }},
		{name: "Update", write: func(s types.WatchedKVStore) error {
			_, err := s.Update(map[string]string{"k": "v"})
			return err
		}},
		{name: "AppendValues", write: func(s types.WatchedKVStore) error { return s.AppendValues([]byte("v")) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := NewStore(inmemory.NewStore())
			before := s.Changed()
			requireOpen(t, before)

			require.NoError(t, tt.write(s))
			requireClosed(t, before)

			after := s.Changed()
			require.NotEqual(t, before, after)
			requireOpen(t, after)
		})
	}
}

func TestChanged_FailedWriteDoesNotNotify(t *testing.T) {
	t.Parallel()

	s := NewStore(inmemory.NewStore())
	ch := s.Changed()

	require.Error(t, s.Set([]byte(""), []byte("v")))
	requireOpen(t, ch)
}

func TestChanged_ReadsDoNotNotify(t *testing.T) {
	t.Parallel()

	s := NewStore(inmemory.NewStore())
	require.NoError(t, s.Set([]byte("k"), []byte("v")))
	ch := s.Changed()

	_, err := s.Get([]byte("k"))
	require.NoError(t, err)
	require.NoError(t, s.ForEach(func(_, _ []byte) error { return nil }))
	_, err = s.Count()
	require.NoError(t, err)

	requireOpen(t, ch)
}

// Multiple writes should still notify existing watchers.
func TestChanged_CoalescesWrites(t *testing.T) {
	t.Parallel()

	s := NewStore(inmemory.NewStore())
	ch := s.Changed()

	require.NoError(t, s.Set([]byte("a"), []byte("1")))
	require.NoError(t, s.Set([]byte("b"), []byte("2")))

	requireClosed(t, ch)
	requireOpen(t, s.Changed())
}
