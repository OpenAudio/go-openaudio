package logger

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/common"
	oaenv "github.com/OpenAudio/go-openaudio/pkg/env"
	"github.com/axiomhq/axiom-go/axiom"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	adapter "github.com/axiomhq/axiom-go/adapters/zap"
)

const (
	AxiomTokenProd  = "eGFhdC1lNDRjZjRmMS02NGY1LTQyZWMtOGM4MC05MzA4ZjU1NmE0ZmRhem93ZXJuYXNkZm9pYQ=="
	AxiomTokenStage = "eGFhdC02YTk0NTQ1NC01YWRiLTRjMmYtYjkzNi0zN2RlZDNlOTI2MzNhem93ZXJuYXNkZm9pYQ=="
	AxiomTokenDev   = "eGFhdC0zMGVhM2FiNy02NWJkLTQ2MzYtYjk5Ny02YzBjMDg5MzM2M2Nhem93ZXJuYXNkZm9pYQ=="

	// Sampling for the Axiom sink only: per tick, the first axiomSampleFirst
	// entries of each distinct message ship, then 1 in axiomSampleThereafter.
	// This bounds ingest when a hot loop repeats one message at high rate
	// (during the July 2026 outage single messages hit ~2,500/sec). Stdout is
	// never sampled.
	axiomSampleTick       = time.Second
	axiomSampleFirst      = 10
	axiomSampleThereafter = 100
)

func CreateLogger(env, level string) (*zap.Logger, error) {
	enableAxiomDefault := strconv.FormatBool(env != "dev")
	enableAxiom := oaenv.Get(enableAxiomDefault, "OPENAUDIO_ENABLE_AXIOM") == "true"

	consoleEncoder := zapcore.NewConsoleEncoder(zap.NewProductionEncoderConfig())

	zapLevel, err := zapcore.ParseLevel(level)
	if err != nil {
		return nil, fmt.Errorf("failed to parse zap level: %v", err)
	}
	stdoutCore := zapcore.NewCore(consoleEncoder, zapcore.AddSync(os.Stdout), zapLevel)

	var axiomToken string
	switch env {
	case "prod":
		axiomToken = AxiomTokenProd
	case "stage":
		axiomToken = AxiomTokenStage
	case "dev":
		axiomToken = AxiomTokenDev
	default:
		axiomToken = ""
	}

	if axiomToken != "" && enableAxiom {
		axiomToken, err = common.Deobfuscate(axiomToken)
		if err != nil {
			return nil, fmt.Errorf("failed to deobfuscate axiom token: %v", err)
		}

		axiomCore, err := adapter.New(
			adapter.SetDataset(fmt.Sprintf("core-%s", env)),
			adapter.SetClientOptions(axiom.SetAPITokenConfig(axiomToken)),
		)
		if err != nil {
			return nil, err
		}

		axiomLevel, err := axiomMinLevel(zapLevel)
		if err != nil {
			return nil, err
		}
		combinedCore := zapcore.NewTee(wrapAxiomCore(axiomCore, axiomLevel), stdoutCore)
		return zap.New(combinedCore), nil
	}

	return zap.New(stdoutCore), nil
}

// axiomMinLevel returns the minimum level shipped to Axiom. The raw adapter
// core has no level filter of its own, so without this floor every debug log
// leaves the node. Defaults to info; OPENAUDIO_AXIOM_LOG_LEVEL overrides it,
// and the global level wins when it is stricter.
func axiomMinLevel(globalLevel zapcore.Level) (zapcore.Level, error) {
	minLevel, err := zapcore.ParseLevel(oaenv.Get("info", "OPENAUDIO_AXIOM_LOG_LEVEL"))
	if err != nil {
		return 0, fmt.Errorf("failed to parse axiom log level: %v", err)
	}
	if globalLevel > minLevel {
		minLevel = globalLevel
	}
	return minLevel, nil
}

// wrapAxiomCore applies the Axiom-only level floor and per-message sampler to
// the raw Axiom sink core. Filtering happens on this branch of the tee only,
// so the stdout core always sees the full log stream.
func wrapAxiomCore(core zapcore.Core, minLevel zapcore.Level) zapcore.Core {
	leveled, err := zapcore.NewIncreaseLevelCore(core, zap.NewAtomicLevelAt(minLevel))
	if err != nil {
		// the underlying core already enables only levels >= minLevel
		leveled = core
	}
	return zapcore.NewSamplerWithOptions(leveled, axiomSampleTick, axiomSampleFirst, axiomSampleThereafter)
}
