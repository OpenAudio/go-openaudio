// Command performance-snapshot validates finalized Core performance inputs,
// publishes deterministic Solana relayer artifacts, and emits signer payloads.
package main

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/performance"
	"github.com/urfave/cli/v2"
)

const defaultPrivateKeyEnv = "OPENAUDIO_DELEGATE_PRIVATE_KEY"

func main() {
	app := newApp(os.Stdout, os.Stderr, os.Getenv)
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func newApp(stdout, stderr io.Writer, getenv func(string) string) *cli.App {
	app := &cli.App{
		Name:      "performance-snapshot",
		Usage:     "Build and sign deterministic validator-node performance reward artifacts",
		Writer:    stdout,
		ErrWriter: stderr,
	}
	app.Commands = []*cli.Command{
		prepareInputCommand(stdout),
		signInputCommand(stdout, getenv),
		generateCommand(stdout),
		signSnapshotCommand(stdout, getenv),
	}
	return app
}

func sourceFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "input", Usage: "Finalized epoch manifest JSON", Required: true},
		&cli.StringFlag{Name: "source-id", Usage: "Registered consensus source ID expected in the manifest", EnvVars: []string{"OPENAUDIO_PERFORMANCE_SOURCE_ID"}, Required: true},
	}
}

func prepareInputCommand(stdout io.Writer) *cli.Command {
	return &cli.Command{
		Name:  "prepare-input",
		Usage: "Validate raw epoch records and print the useful-work consensus payload",
		Flags: sourceFlags(),
		Action: func(c *cli.Context) error {
			input, err := loadInput(c)
			if err != nil {
				return err
			}
			payload, err := performance.PrepareUsefulWorkConsensus(input)
			if err != nil {
				return err
			}
			return writeJSON(stdout, payload)
		},
	}
}

func signInputCommand(stdout io.Writer, getenv func(string) string) *cli.Command {
	flags := append(sourceFlags(),
		&cli.StringFlag{Name: "private-key-env", Usage: "Environment variable containing the Ethereum signer key", Value: defaultPrivateKeyEnv},
	)
	return &cli.Command{
		Name:  "sign-input",
		Usage: "Sign the useful-work consensus payload as one frozen eligible signer",
		Flags: flags,
		Action: func(c *cli.Context) error {
			input, err := loadInput(c)
			if err != nil {
				return err
			}
			privateKey, err := privateKeyFromEnvironment(getenv, c.String("private-key-env"))
			if err != nil {
				return err
			}
			attestation, err := performance.SignUsefulWorkConsensus(input, privateKey)
			if err != nil {
				return err
			}
			return writeJSON(stdout, attestation)
		},
	}
}

func generateCommand(stdout io.Writer) *cli.Command {
	flags := append(sourceFlags(),
		&cli.StringFlag{Name: "output", Usage: "Immutable artifact output path", Required: true},
		&cli.StringFlag{Name: "program-id", Usage: "Performance Rewards Solana program ID", EnvVars: []string{"SOLANA_PERFORMANCE_REWARDS_PROGRAM_ID"}, Required: true},
		&cli.StringFlag{Name: "config-account", Usage: "Performance Rewards config account", EnvVars: []string{"SOLANA_PERFORMANCE_REWARDS_CONFIG_ACCOUNT"}, Required: true},
		&cli.BoolFlag{Name: "print", Usage: "Also publish the canonical artifact to stdout"},
	)
	return &cli.Command{
		Name:  "generate",
		Usage: "Verify quorum, generate a snapshot, and atomically persist the relayer artifact",
		Flags: flags,
		Action: func(c *cli.Context) error {
			source := performance.FileSource{Path: c.String("input"), ExpectedSourceID: c.String("source-id")}
			artifact, err := performance.GenerateArtifact(c.Context, source, c.String("program-id"), c.String("config-account"))
			if err != nil {
				return err
			}
			if err := performance.PersistArtifact(c.String("output"), artifact); err != nil {
				return err
			}
			if c.Bool("print") {
				data, err := performance.MarshalArtifact(artifact)
				if err != nil {
					return err
				}
				_, err = stdout.Write(data)
				return err
			}
			return nil
		},
	}
}

func signSnapshotCommand(stdout io.Writer, getenv func(string) string) *cli.Command {
	return &cli.Command{
		Name:  "sign-snapshot",
		Usage: "Validate an artifact and emit the exact eligible Solana attestation payload",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "artifact", Usage: "Canonical artifact JSON", Required: true},
			&cli.StringFlag{Name: "output", Usage: "Immutable attestation output path, or - for stdout", Value: "-"},
			&cli.StringFlag{Name: "private-key-env", Usage: "Environment variable containing the Ethereum signer key", Value: defaultPrivateKeyEnv},
		},
		Action: func(c *cli.Context) error {
			artifact, err := performance.LoadArtifactFile(c.String("artifact"))
			if err != nil {
				return err
			}
			privateKey, err := privateKeyFromEnvironment(getenv, c.String("private-key-env"))
			if err != nil {
				return err
			}
			attestation, err := performance.SignArtifact(artifact, privateKey)
			if err != nil {
				return err
			}
			if c.String("output") != "-" {
				return performance.PersistSnapshotAttestation(c.String("output"), artifact, attestation)
			}
			return writeJSON(stdout, attestation)
		},
	}
}

func loadInput(c *cli.Context) (*performance.FinalizedEpochInput, error) {
	source := performance.FileSource{Path: c.String("input"), ExpectedSourceID: c.String("source-id")}
	return source.LoadFinalizedEpoch(c.Context)
}

func privateKeyFromEnvironment(getenv func(string) string, name string) (*ecdsa.PrivateKey, error) {
	name = strings.TrimSpace(name)
	if getenv == nil || name == "" {
		return nil, fmt.Errorf("private key environment variable is not configured")
	}
	value := strings.TrimSpace(getenv(name))
	value = strings.TrimPrefix(strings.TrimPrefix(value, "0x"), "0X")
	if value == "" {
		return nil, fmt.Errorf("private key environment variable %s is empty", name)
	}
	privateKey, err := common.EthToEthKey(value)
	if err != nil {
		return nil, fmt.Errorf("private key environment variable %s: %w", name, err)
	}
	return privateKey, nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
