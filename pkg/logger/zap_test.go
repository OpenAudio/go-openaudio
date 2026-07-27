package logger

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestWrapAxiomCoreDropsBelowMinLevel(t *testing.T) {
	// the raw adapter core enables all levels; the wrapper must apply the floor
	sink, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(wrapAxiomCore(sink, zapcore.InfoLevel))

	logger.Debug("debug message")
	logger.Info("info message")
	logger.Warn("warn message")

	assert.Equal(t, 0, logs.FilterMessage("debug message").Len())
	assert.Equal(t, 1, logs.FilterMessage("info message").Len())
	assert.Equal(t, 1, logs.FilterMessage("warn message").Len())
}

func TestWrapAxiomCoreSamplesRepeatedMessages(t *testing.T) {
	sink, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(wrapAxiomCore(sink, zapcore.InfoLevel))

	// a hot loop repeating one message must be bounded: within one tick only
	// the first axiomSampleFirst entries plus 1 in axiomSampleThereafter ship
	const spam = 5000
	for i := 0; i < spam; i++ {
		logger.Warn("blob storage disk space below threshold")
	}

	got := logs.FilterMessage("blob storage disk space below threshold").Len()
	maxExpected := axiomSampleFirst + spam/axiomSampleThereafter + 1
	assert.GreaterOrEqual(t, got, axiomSampleFirst, "the first entries must not be dropped")
	assert.LessOrEqual(t, got, maxExpected, "sampler must bound repeated messages")

	// distinct messages are sampled independently and still get through
	logger.Warn("some other message")
	assert.Equal(t, 1, logs.FilterMessage("some other message").Len())
}

func TestWrapAxiomCoreDoesNotAffectOtherTeeBranch(t *testing.T) {
	// stdout branch of the tee must see the full, unsampled stream even while
	// the axiom branch filters and samples
	axiomSink, axiomLogs := observer.New(zapcore.DebugLevel)
	stdoutSink, stdoutLogs := observer.New(zapcore.DebugLevel)

	logger := zap.New(zapcore.NewTee(wrapAxiomCore(axiomSink, zapcore.InfoLevel), stdoutSink))

	logger.Debug("debug message")
	const spam = 2000
	for i := 0; i < spam; i++ {
		logger.Info("repeated message")
	}

	assert.Equal(t, 0, axiomLogs.FilterMessage("debug message").Len())
	assert.Equal(t, 1, stdoutLogs.FilterMessage("debug message").Len())

	assert.Equal(t, spam, stdoutLogs.FilterMessage("repeated message").Len(), "stdout must never be sampled")
	assert.Less(t, axiomLogs.FilterMessage("repeated message").Len(), spam)
}

func TestWrapAxiomCoreWithFields(t *testing.T) {
	// sampling keys on the message, not the fields; fields must pass through
	sink, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(wrapAxiomCore(sink, zapcore.InfoLevel))

	logger.Info("added to mempool", zap.String("tx", "abc123"))
	entries := logs.FilterMessage("added to mempool").All()
	require.Len(t, entries, 1)
	assert.Equal(t, "abc123", entries[0].ContextMap()["tx"])
}

func TestAxiomMinLevelDefaultsToInfo(t *testing.T) {
	level, err := axiomMinLevel(zapcore.DebugLevel)
	require.NoError(t, err)
	assert.Equal(t, zapcore.InfoLevel, level, "debug must not ship to axiom by default")
}

func TestAxiomMinLevelRespectsStricterGlobalLevel(t *testing.T) {
	level, err := axiomMinLevel(zapcore.ErrorLevel)
	require.NoError(t, err)
	assert.Equal(t, zapcore.ErrorLevel, level)
}

func TestAxiomMinLevelEnvOverride(t *testing.T) {
	t.Setenv("OPENAUDIO_AXIOM_LOG_LEVEL", "warn")
	level, err := axiomMinLevel(zapcore.InfoLevel)
	require.NoError(t, err)
	assert.Equal(t, zapcore.WarnLevel, level)
}

func TestAxiomMinLevelInvalidEnv(t *testing.T) {
	t.Setenv("OPENAUDIO_AXIOM_LOG_LEVEL", "bogus")
	_, err := axiomMinLevel(zapcore.InfoLevel)
	assert.Error(t, err)
}

func TestCreateLoggerWithoutAxiom(t *testing.T) {
	// dev defaults to axiom disabled; logger must still work at every level
	t.Setenv("OPENAUDIO_ENABLE_AXIOM", "false")
	for _, level := range []string{"debug", "info", "warn", "error"} {
		logger, err := CreateLogger("dev", level)
		require.NoError(t, err, fmt.Sprintf("level %s", level))
		logger.Info("smoke test")
	}
}

func TestCreateLoggerInvalidLevel(t *testing.T) {
	_, err := CreateLogger("dev", "bogus")
	assert.Error(t, err)
}
