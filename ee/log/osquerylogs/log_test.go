package osquerylogs

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/kolide/launcher/v2/pkg/threadsafebuffer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestExtractOsqueryCaller(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		log      string
		expected string
	}{
		{
			`I1101 19:21:40.292618 84815872 distributed.cpp:133] Executing distributed query: kolide:populate:practices:1: SELECT COUNT(*) AS result FROM (select * from time);`,
			`distributed.cpp:133`,
		},
		{
			`E1201 08:21:54.254618 84815872 foobar.m:47] Penguin`,
			`foobar.m:47`,
		},
		{
			`E1201 08:21:54.254618 84815872 unknown] Penguin`,
			``,
		},
		{
			`Just plain bad`,
			``,
		},
	}

	for _, tt := range testCases {
		t.Run("", func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, extractOsqueryCaller(tt.log))
		})
	}
}

func Test_extractLogLevel(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		testCaseName     string
		defaultLogLevel  slog.Level
		msg              string
		expectedLogLevel slog.Level
	}{
		{
			testCaseName:     "error",
			defaultLogLevel:  slog.LevelDebug,
			msg:              "E0731 11:00:07.412808 135955776 registry_factory.cpp:188] sql registry sql plugin caused exception: map::at:  key not found",
			expectedLogLevel: slog.LevelError,
		},
		{
			testCaseName:     "warn",
			defaultLogLevel:  slog.LevelDebug,
			msg:              "W0801 10:07:54.639108 1888202752 mdfind.mm:74] Could not execute mdfind query",
			expectedLogLevel: slog.LevelWarn,
		},
		{
			testCaseName:     "info",
			defaultLogLevel:  slog.LevelDebug,
			msg:              "I0804 10:12:20.279402 1880748032 config.cpp:1334] Refreshing configuration state",
			expectedLogLevel: slog.LevelInfo,
		},
		{
			testCaseName:     "non-match from END in SQL",
			defaultLogLevel:  slog.LevelDebug,
			msg:              "END AS some_name",
			expectedLogLevel: slog.LevelDebug,
		},
		{
			testCaseName:     "non-match from ELSE in SQL",
			defaultLogLevel:  slog.LevelDebug,
			msg:              "ELSE 'false' END AS some_condition",
			expectedLogLevel: slog.LevelDebug,
		},
		{
			testCaseName:     "non-match from WHEN in SQL",
			defaultLogLevel:  slog.LevelInfo,
			msg:              "WHEN x = 100",
			expectedLogLevel: slog.LevelInfo,
		},
		{
			testCaseName:     "non-match from IN in SQL",
			defaultLogLevel:  slog.LevelDebug,
			msg:              "IN (100, 200)",
			expectedLogLevel: slog.LevelDebug,
		},
	} {
		t.Run(tt.testCaseName, func(t *testing.T) {
			t.Parallel()

			adapter := &OsqueryLogAdapter{
				level: tt.defaultLogLevel,
			}

			require.Equal(t, tt.expectedLogLevel, adapter.extractLogLevel(tt.msg))
		})
	}
}

func TestWrite_splitsMultilineChunks(t *testing.T) {
	t.Parallel()

	var logBytes threadsafebuffer.ThreadSafeBuffer
	adapter := NewOsqueryLogAdapter(
		slog.New(slog.NewJSONHandler(&logBytes, &slog.HandlerOptions{Level: slog.LevelDebug})),
		t.TempDir(),
		WithLevel(slog.LevelDebug),
	)

	// A single read from osquery's pipe routinely contains several glog lines.
	chunk := `E0817 09:01:26.955906 3393024 registry_factory.cpp:188] sql registry sql plugin caused exception: map::at:  key not found
W0801 10:07:54.639108 1888202752 mdfind.mm:74] Could not execute mdfind query
I0804 10:12:20.279402 1880748032 config.cpp:1334] Refreshing configuration state
I1101 19:21:40.292618 84815872 distributed.cpp:133] Accelerating distributed query checkins

`

	n, err := adapter.Write([]byte(chunk))
	require.NoError(t, err)
	require.Equal(t, len(chunk), n, "should report the full chunk as written")

	logLines := strings.Split(strings.TrimSpace(logBytes.String()), "\n")
	require.Len(t, logLines, 3, "expected one record per line, minus the filtered line and blanks")

	expected := []struct {
		level  string
		caller string
	}{
		{"ERROR", "registry_factory.cpp:188"},
		{"WARN", "mdfind.mm:74"},
		{"INFO", "config.cpp:1334"},
	}

	for i, logLine := range logLines {
		var logRecord map[string]any
		require.NoError(t, json.Unmarshal([]byte(logLine), &logRecord))

		assert.Equal(t, expected[i].level, logRecord["level"], "level should come from this line, not the first one")
		assert.Equal(t, expected[i].caller, logRecord["caller"])
		assert.NotContains(t, logRecord["msg"], "\n", "records should not contain multiple glog lines")
	}

	assert.NotContains(t, logBytes.String(), "Accelerating distributed query checkins")
}
