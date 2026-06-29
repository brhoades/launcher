package multislogger

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// captureHandler records every slog.Record for inspection.
type captureHandler struct {
	mu      *sync.Mutex
	records *[]capturedRecord
	attrs   []slog.Attr
}

type capturedRecord struct {
	level slog.Level
	msg   string
	attrs []slog.Attr
}

func newCaptureHandler() *captureHandler {
	return &captureHandler{
		mu:      &sync.Mutex{},
		records: &[]capturedRecord{},
	}
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	rec := capturedRecord{level: r.Level, msg: r.Message, attrs: slices.Clone(h.attrs)}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs = append(rec.attrs, a)
		return true
	})

	h.mu.Lock()
	defer h.mu.Unlock()
	*h.records = append(*h.records, rec)
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nh := *h
	nh.attrs = append(slices.Clone(h.attrs), attrs...)
	return &nh
}

func (h *captureHandler) WithGroup(_ string) slog.Handler {
	nh := *h
	return &nh
}

func (h *captureHandler) snapshot() []capturedRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(*h.records)
}

func (h *captureHandler) len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(*h.records)
}

type noopHandler struct{}

func (noopHandler) Enabled(_ context.Context, _ slog.Level) bool  { return true }
func (noopHandler) Handle(_ context.Context, _ slog.Record) error { return nil }
func (noopHandler) WithAttrs(_ []slog.Attr) slog.Handler          { return noopHandler{} }
func (noopHandler) WithGroup(_ string) slog.Handler               { return noopHandler{} }

func attrValue(attrs []slog.Attr, key string) (slog.Value, bool) {
	for _, a := range attrs {
		if a.Key == key {
			return a.Value, true
		}
	}
	return slog.Value{}, false
}

func TestMultiSloggerFanout(t *testing.T) {
	t.Parallel()

	// Regression: all children instantiated from a multilogger should be affected by handler addition.
	t.Run("handler addition affects older child loggers", func(t *testing.T) {
		t.Parallel()

		h1 := newCaptureHandler()
		ms := New(h1)

		child := ms.With("component", "early")
		child.InfoContext(t.Context(), "before")
		require.Equal(t, 1, h1.len())

		h2 := newCaptureHandler()
		ms.AddHandler(h2)
		child.InfoContext(t.Context(), "after")

		h1recs, h2recs := h1.snapshot(), h2.snapshot()
		require.Len(t, h1recs, 2)
		require.Len(t, h2recs, 1)

		for _, rec := range []capturedRecord{h1recs[1], h2recs[0]} {
			require.Equal(t, "after", rec.msg)
			v, ok := attrValue(rec.attrs, "component")
			require.True(t, ok)
			require.Equal(t, "early", v.String())
		}
	})

	t.Run("a single log fans out to all handlers", func(t *testing.T) {
		t.Parallel()

		handlers := []*captureHandler{newCaptureHandler(), newCaptureHandler(), newCaptureHandler()}
		ms := New(handlers[0], handlers[1], handlers[2])

		ms.InfoContext(t.Context(), "msg", "k", "v")

		for i, h := range handlers {
			recs := h.snapshot()
			require.Lenf(t, recs, 1, "handler %d", i)
			require.Equal(t, slog.LevelInfo, recs[0].level)
			require.Equal(t, "msg", recs[0].msg)
			v, ok := attrValue(recs[0].attrs, "k")
			require.True(t, ok)
			require.Equal(t, "v", v.String())
		}
	})

	t.Run("logs are discarded when empty", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		ms := New()
		require.NotPanics(t, func() {
			ms.InfoContext(ctx, "no handlers")
			ms.With("k", "v").WithGroup("g").InfoContext(ctx, "still none")
			NewNopLogger().InfoContext(ctx, "nop")
		})

		// empty -> non-empty: a handler added later starts receiving
		h := newCaptureHandler()
		ms.AddHandler(h)
		ms.InfoContext(ctx, "now captured")
		require.Equal(t, 1, h.len())
	})

	t.Run("attrs are scoped to the derived logger", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		h := newCaptureHandler()
		base := New(h).Logger

		base.With("k", "A").InfoContext(ctx, "a")
		base.With("k", "B").InfoContext(ctx, "b")
		base.InfoContext(ctx, "base")

		byMsg := map[string][]slog.Attr{}
		for _, r := range h.snapshot() {
			byMsg[r.msg] = r.attrs
		}
		require.Len(t, byMsg, 3)

		va, ok := attrValue(byMsg["a"], "k")
		require.True(t, ok)
		require.Equal(t, "A", va.String())

		vb, ok := attrValue(byMsg["b"], "k")
		require.True(t, ok)
		require.Equal(t, "B", vb.String())

		_, ok = attrValue(byMsg["base"], "k")
		require.False(t, ok)
	})

	t.Run("child handlers get a separate slice", func(t *testing.T) {
		t.Parallel()

		handlers := &atomic.Pointer[[]slog.Handler]{}
		handlers.Store(&[]slog.Handler{})

		parentAttrs := make([]slog.Attr, 1, 4)
		parentAttrs[0] = slog.String("base", "0")
		parent := fanoutHandler{handlers: handlers, attrs: parentAttrs}

		c1 := parent.WithAttrs([]slog.Attr{slog.String("child", "one")}).(fanoutHandler)
		c2 := parent.WithAttrs([]slog.Attr{slog.String("child", "two")}).(fanoutHandler)

		v1, ok := attrValue(c1.attrs, "child")
		require.True(t, ok)
		require.Equal(t, "one", v1.String())

		v2, ok := attrValue(c2.attrs, "child")
		require.True(t, ok)
		require.Equal(t, "two", v2.String())
	})

	t.Run("concurrent handler modification does not race", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		ms := New(noopHandler{})
		child := ms.With("component", "x")

		var wg sync.WaitGroup
		done := make(chan struct{})

		for range 8 {
			wg.Go(func() {
				for {
					select {
					case <-done:
						return
					default:
						ms.InfoContext(ctx, "root")
						child.InfoContext(ctx, "child")
					}
				}
			})
		}

		wg.Go(func() {
			for range 32 {
				ms.AddHandler(noopHandler{})
				time.Sleep(time.Millisecond)
			}
			close(done)
		})

		wg.Wait()
	})
}
