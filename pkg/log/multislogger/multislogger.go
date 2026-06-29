package multislogger

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kolide/launcher/v2/ee/agent/flags/keys"
	"github.com/kolide/launcher/v2/ee/agent/types"
	"github.com/kolide/launcher/v2/pkg/log/dedup"
	slogmulti "github.com/samber/slog-multi"
)

type contextKey string

func (c contextKey) String() string {
	return string(c)
}

const (
	// KolideSessionIdKey this the also the saml session id
	KolideSessionIdKey contextKey = "kolide_session_id"
	SpanIdKey          contextKey = "span_id"
	TraceIdKey         contextKey = "trace_id"
	TraceSampledKey    contextKey = "trace_sampled"
)

// ctxValueKeysToAdd is a list of context keys that will be
// added as log attributes
var ctxValueKeysToAdd = []contextKey{
	SpanIdKey,
	TraceIdKey,
	KolideSessionIdKey,
	TraceSampledKey,
}

type MultiSlogger struct {
	*slog.Logger

	handlers *atomic.Pointer[[]slog.Handler]
	addMu    sync.Mutex // for handlers

	// middlewares with state that must persist across rebuilds
	dedupEngine *dedup.Engine

	// flags interface for reading flag values when they change
	flags types.Flags

	// lifecycle coordination when managed by a rungroup
	interrupted atomic.Bool
	stopCh      chan struct{}
}

// New creates a new multislogger if no handlers are passed in, it will
// create a logger that discards all logs
func New(h ...slog.Handler) *MultiSlogger {
	return NewWithDedup(0, h...)
}

// NewWithDedup creates a new multislogger with configurable deduplication window
func NewWithDedup(duplicateLogWindow time.Duration, h ...slog.Handler) *MultiSlogger {
	ms := &MultiSlogger{
		// setting to fanout with no handlers is noop
		stopCh:   make(chan struct{}),
		handlers: &atomic.Pointer[[]slog.Handler]{},
	}
	ms.handlers.Store(&[]slog.Handler{})
	ms.AddHandler(h...)

	// Initialize deduper once at construction; it will emit summaries using the
	// downstream middleware 'next' observed during handling. Call Start(ctx)
	// to begin its background maintenance lifecycle.
	ms.dedupEngine = dedup.New(dedup.WithDuplicateLogWindow(duplicateLogWindow))

	ms.Logger = slog.New(
		slogmulti.
			Pipe(slogmulti.NewHandleInlineMiddleware(utcTimeMiddleware)).
			Pipe(slogmulti.NewHandleInlineMiddleware(ctxValuesMiddleWare)).
			Pipe(ms.dedupEngine.NewMiddleware()).
			Pipe(slogmulti.NewHandleInlineMiddleware(reportedErrorMiddleware)).
			Handler(newFanoutHandler(ms.handlers)),
	)

	return ms
}

// NewNopLogger returns a slogger with no handlers, discarding all logs.
func NewNopLogger() *slog.Logger {
	return New().Logger
}

// AddHandler adds a handler to the multislogger, this creates a branch new
// slog.Logger under the the hood, and overwrites old Logger memory address,
// this means any attributes added with Logger.With will be lost
func (m *MultiSlogger) AddHandler(handler ...slog.Handler) {
	m.addMu.Lock()
	defer m.addMu.Unlock()
	handlers := *m.handlers.Load()
	handlers = append(handlers, handler...)
	m.handlers.Store(&handlers)
}

// Stop releases background resources owned by the multislogger, such as the
// deduplication engine cleanup goroutine.
func (m *MultiSlogger) Stop() {
	if m == nil {
		return
	}
	if m.dedupEngine != nil {
		m.dedupEngine.Stop()
	}
}

// ExecuteWithContext returns a function suitable for a rungroup actor that starts
// the multislogger background work and blocks until interrupted.
func (m *MultiSlogger) ExecuteWithContext(ctx context.Context) func() error {
	return func() error {
		if m == nil {
			return nil
		}
		// reset interruption state on each run
		m.interrupted.Store(false)
		// recreate stop channel if it was previously closed
		select {
		case <-m.stopCh:
			// channel was closed, create a new one
			m.stopCh = make(chan struct{})
		default:
			// channel is open, use existing one
		}
		// start background middleware (dedup cleanup loop)
		m.Start(ctx)
		// block until interrupted
		select {
		case <-ctx.Done():
		case <-m.stopCh:
		}
		return nil
	}
}

// Interrupt implements a rungroup-compatible interrupt handler. It is safe to
// call multiple times; only the first call closes the internal stop channel and
// stops background resources.
func (m *MultiSlogger) Interrupt(_ error) {
	if m == nil {
		return
	}
	// ensure only first interrupt proceeds
	if m.interrupted.Swap(true) {
		return
	}
	if m.stopCh != nil {
		select {
		case <-m.stopCh:
			// already closed
		default:
			close(m.stopCh)
		}
	}
	// stop background middleware (idempotent)
	m.Stop()
}

// Start wires the lifecycle context for background middleware work (e.g.,
// dedup engine cleanup). If called more than once, subsequent calls are no-ops.
func (m *MultiSlogger) Start(ctx context.Context) {
	if m == nil {
		return
	}
	if m.dedupEngine != nil {
		m.dedupEngine.Start(ctx)
	}
}

// UpdateDuplicateLogWindow updates the dedup engine's duplicate log window duration.
// When set to zero or negative, deduplication is effectively disabled.
func (m *MultiSlogger) UpdateDuplicateLogWindow(window time.Duration) {
	if m == nil || m.dedupEngine == nil {
		return
	}
	m.dedupEngine.SetDuplicateLogWindow(window)
}

// SetFlags sets the flags interface for this MultiSlogger to listen for flag changes.
func (m *MultiSlogger) SetFlags(flags types.Flags) {
	if m == nil {
		return
	}
	m.flags = flags
}

// FlagsChanged implements types.FlagsChangeObserver to respond to flag changes.
// When the DuplicateLogWindow flag changes, it updates the dedup engine configuration.
func (m *MultiSlogger) FlagsChanged(ctx context.Context, flagKeys ...keys.FlagKey) {
	if m == nil || m.flags == nil {
		return
	}

	// Check if DuplicateLogWindow flag is among the changed flags
	for _, key := range flagKeys {
		if key != keys.DuplicateLogWindow {
			continue
		}
		newWindow := m.flags.DuplicateLogWindow()
		m.UpdateDuplicateLogWindow(newWindow)
		return
	}
}

func utcTimeMiddleware(ctx context.Context, record slog.Record, next func(context.Context, slog.Record) error) error {
	record.Time = record.Time.UTC()
	return next(ctx, record)
}

func ctxValuesMiddleWare(ctx context.Context, record slog.Record, next func(context.Context, slog.Record) error) error {
	for _, key := range ctxValueKeysToAdd {
		if v := ctx.Value(key); v != nil {
			record.AddAttrs(slog.Attr{
				Key:   key.String(),
				Value: slog.AnyValue(v),
			})
		}
	}

	return next(ctx, record)
}

func reportedErrorMiddleware(ctx context.Context, record slog.Record, next func(context.Context, slog.Record) error) error {
	if record.Level != slog.LevelError {
		return next(ctx, record)
	}

	// We tag LevelError logs for GCP.
	// See: https://cloud.google.com/error-reporting/docs/formatting-error-messages
	record.AddAttrs(
		// We must set @type so that GCP knows it's a ReportedError
		slog.Attr{
			Key:   "@type",
			Value: slog.StringValue("type.googleapis.com/google.devtools.clouderrorreporting.v1beta1.ReportedErrorEvent"),
		},
		// GCP must have a "message", "stack_trace", or "exception" field, none of which we're guaranteed here
		// (we report up record.Message under key "msg"). Duplicate record.Message to key "message" so the error will be recorded.
		slog.Attr{
			Key:   "message",
			Value: slog.StringValue(record.Message),
		},
	)

	return next(ctx, record)
}

// A fanout handler which loads dynamic handlers from its internal list. As an implementation detail
// of a MultiSlogger, it enables all children to inherit handlers dynamically.
type fanoutHandler struct {
	handlers *atomic.Pointer[[]slog.Handler] // shared with its MultiSlogger
	attrs    []slog.Attr
	group    string
}

func newFanoutHandler(handlers *atomic.Pointer[[]slog.Handler]) slog.Handler {
	return fanoutHandler{
		handlers: handlers,
	}
}

func (fanoutHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

// Handle incoming logs, fanning out to dynamically sourced list of child handlers.
// This is effectively slogmulti.Fanout, but sourced from a dynamic handler list with attributes
// applied on-the-fly.
func (f fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range *f.handlers.Load() {
		if h.Enabled(ctx, r.Level) {
			if len(f.attrs) != 0 {
				h = h.WithAttrs(f.attrs)
			}
			if f.group != "" {
				h = h.WithGroup(f.group)
			}
			if err := h.Handle(ctx, r.Clone()); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errors.Join(errs...)
}

func (f fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return fanoutHandler{
		handlers: f.handlers,
		attrs:    append(slices.Clone(f.attrs), attrs...),
		group:    f.group,
	}
}

func (f fanoutHandler) WithGroup(name string) slog.Handler {
	return fanoutHandler{
		handlers: f.handlers,
		attrs:    slices.Clone(f.attrs),
		group:    name,
	}
}
