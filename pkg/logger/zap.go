package logger

import (
	"fmt"
	"os"
	"strconv"

	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/axiomhq/axiom-go/axiom"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	adapter "github.com/axiomhq/axiom-go/adapters/zap"
)

const (
	AxiomTokenProd  = "eGFhdC1lNDRjZjRmMS02NGY1LTQyZWMtOGM4MC05MzA4ZjU1NmE0ZmRhem93ZXJuYXNkZm9pYQ=="
	AxiomTokenStage = "eGFhdC02YTk0NTQ1NC01YWRiLTRjMmYtYjkzNi0zN2RlZDNlOTI2MzNhem93ZXJuYXNkZm9pYQ=="
	AxiomTokenDev   = "eGFhdC0zMGVhM2FiNy02NWJkLTQ2MzYtYjk5Ny02YzBjMDg5MzM2M2Nhem93ZXJuYXNkZm9pYQ=="
)

func CreateLogger(env, level string) (*zap.Logger, error) {
	enableAxiomDefault := strconv.FormatBool(env != "dev")
	enableAxiom := config.GetEnvWithDefault("OPENAUDIO_ENABLE_AXIOM", enableAxiomDefault) == "true"

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
		combinedCore := zapcore.NewTee(axiomCore, stdoutCore)
		return zap.New(combinedCore), nil
	}

	return zap.New(stdoutCore), nil
}
