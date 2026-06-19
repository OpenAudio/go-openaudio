package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/fuzz"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("fuzzrun", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)

	mode := flags.String("mode", "model", "mode to run: model, sim, live, or chaos")
	nodes := flags.Int("nodes", fuzz.DefaultModelNodeLimit, "number of model nodes")
	steps := flags.Int("steps", 20_000, "model steps per iteration")
	iterations := flags.Int("iterations", 100, "iterations to run")
	seed := flags.Int64("seed", time.Now().UnixNano(), "base seed")
	endpoints := flags.String("endpoints", "", "comma-separated live endpoints")
	discoveryEndpoint := flags.String("discovery-endpoint", "", "live endpoint that serves /console/api/core-validators-endpoints")
	maxEndpoints := flags.Int("max-endpoints", 0, "maximum discovered live endpoints to use")
	minReachable := flags.Int("min-reachable", 0, "minimum reachable live endpoints required; defaults to two-thirds-plus-one")
	insecureTLS := flags.Bool("insecure-tls", false, "skip live endpoint TLS verification")
	scheme := flags.String("scheme", "https", "default scheme for endpoints without a scheme")
	liveTimeout := flags.Duration("live-timeout", 90*time.Second, "timeout per live iteration")
	liveWindow := flags.Duration("live-window", 60*time.Second, "height-advance window per live iteration")
	stepTimeout := flags.Duration("step-timeout", 5*time.Second, "timeout per generated chaos step")
	pollInterval := flags.Duration("poll-interval", 2*time.Second, "live poll interval")
	actionNodes := flags.String("action-node-ids", "", "comma-separated node ids eligible for mutating chaos actions")
	allowMutations := flags.Bool("allow-mutations", false, "required for chaos mode because it sends registry transactions")
	registryRPC := flags.String("registry-rpc-url", "", "eth RPC URL for chaos registry mutations")
	registryAddress := flags.String("registry-address", "", "Audius registry address for chaos registry mutations")
	registryPrivateKey := flags.String("registry-private-key", "", "private key for chaos registry mutations")
	registryStakeWei := flags.String("registry-stake-wei", "0", "stake amount for register transactions, in wei")
	registryDelegateOwner := flags.String("registry-delegate-owner", "", "delegate owner wallet for register transactions; defaults to signer")
	chaosSteps := flags.Int("chaos-steps", 50, "mutating chaos steps per iteration")
	livenessEvery := flags.Int("liveness-every", 10, "chaos liveness assertion cadence")

	if err := flags.Parse(args); err != nil {
		return err
	}
	if *iterations <= 0 {
		return fmt.Errorf("-iterations must be positive")
	}

	switch *mode {
	case "model":
		return runModelLoop(*seed, *iterations, *nodes, *steps)
	case "sim":
		return runSimulatedLoop(simulatedLoopConfig{
			seed:          *seed,
			iterations:    *iterations,
			nodes:         *nodes,
			steps:         *steps,
			timeout:       *liveTimeout,
			window:        *liveWindow,
			stepTimeout:   *stepTimeout,
			pollInterval:  *pollInterval,
			livenessEvery: *livenessEvery,
		})
	case "live":
		return runLiveLoop(ctx, liveLoopConfig{
			seed:              *seed,
			iterations:        *iterations,
			endpoints:         splitCSV(*endpoints),
			discoveryEndpoint: *discoveryEndpoint,
			maxEndpoints:      *maxEndpoints,
			minReachable:      *minReachable,
			insecureTLS:       *insecureTLS,
			scheme:            *scheme,
			timeout:           *liveTimeout,
			window:            *liveWindow,
			pollInterval:      *pollInterval,
		})
	case "chaos":
		return runChaosLoop(ctx, chaosLoopConfig{
			liveLoopConfig: liveLoopConfig{
				seed:              *seed,
				iterations:        *iterations,
				endpoints:         splitCSV(*endpoints),
				discoveryEndpoint: *discoveryEndpoint,
				maxEndpoints:      *maxEndpoints,
				minReachable:      *minReachable,
				insecureTLS:       *insecureTLS,
				scheme:            *scheme,
				timeout:           *liveTimeout,
				window:            *liveWindow,
				pollInterval:      *pollInterval,
			},
			actionNodeIDs:         nodeIDs(splitCSV(*actionNodes)),
			allowMutations:        *allowMutations,
			registryRPCURL:        *registryRPC,
			registryAddress:       *registryAddress,
			registryPrivateKey:    *registryPrivateKey,
			registryStakeWei:      *registryStakeWei,
			registryDelegateOwner: *registryDelegateOwner,
			chaosSteps:            *chaosSteps,
			livenessEvery:         *livenessEvery,
		})
	default:
		return fmt.Errorf("unsupported mode %q", *mode)
	}
}

type simulatedLoopConfig struct {
	seed          int64
	iterations    int
	nodes         int
	steps         int
	timeout       time.Duration
	window        time.Duration
	pollInterval  time.Duration
	stepTimeout   time.Duration
	livenessEvery int
}

func runSimulatedLoop(cfg simulatedLoopConfig) error {
	cfg = normalizeSimulatedLoopConfig(cfg)
	started := time.Now()
	if err := runSimulatedOutcomeEdgeCases(cfg); err != nil {
		return err
	}
	if err := runSimulatedCompoundOutcomeEdgeCases(cfg); err != nil {
		return err
	}
	if err := runSimulatedPowerSkewOutcomeEdgeCases(cfg); err != nil {
		return err
	}
	if err := runSimulatedPowerBoundaryOutcomeEdgeCases(cfg); err != nil {
		return err
	}
	if err := runSimulatedQuorumLossRecovery(cfg); err != nil {
		return err
	}
	if err := runSimulatedJailedDeregisterCompatibility(cfg); err != nil {
		return err
	}
	if err := runSimulatedDuplicateDeregisterIdempotency(cfg); err != nil {
		return err
	}
	if err := runSimulatedDuplicateJailIdempotency(cfg); err != nil {
		return err
	}
	if err := runSimulatedEndpointLieConsensusIsolation(cfg); err != nil {
		return err
	}
	if err := runSimulatedStopStartRoundTrip(cfg); err != nil {
		return err
	}
	if err := runSimulatedInactiveStartIsolation(cfg); err != nil {
		return err
	}
	if err := runSimulatedNonJailedUnjailIsolation(cfg); err != nil {
		return err
	}
	if err := runSimulatedRegisterRoundTrip(cfg); err != nil {
		return err
	}
	if err := runSimulatedRegisterIdempotency(cfg); err != nil {
		return err
	}
	if err := runSimulatedUnjailRoundTrip(cfg); err != nil {
		return err
	}
	for i := 0; i < cfg.iterations; i++ {
		network, err := fuzz.NewSimulatedNetwork(fuzz.SimulatedNetworkOptions{
			NodeCount:      cfg.nodes,
			InitialActive:  cfg.nodes,
			TickOnSnapshot: true,
		})
		if err != nil {
			return err
		}
		iterationCtx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
		result, err := fuzz.Runner{
			Network:     network,
			Seed:        cfg.seed + int64(i),
			StepTimeout: cfg.stepTimeout,
		}.Run(iterationCtx, fuzz.ValidatorChaosScenario(network.Spec(), fuzz.ValidatorChaosController{
			Registrar:       network,
			EndpointMutator: network,
			Jailer:          network,
		}, fuzz.ValidatorChaosOptions{
			Seed:                    cfg.seed + int64(i),
			Steps:                   cfg.steps,
			StepTimeout:             cfg.stepTimeout,
			LivenessEvery:           cfg.livenessEvery,
			LivenessWithin:          cfg.window,
			PollInterval:            cfg.pollInterval,
			IncludeProcessFaults:    true,
			NoProcessFaultDelay:     true,
			AssertAfterEachStep:     true,
			AssertConvergence:       true,
			IncludePersistentFaults: true,
			RecoverAtEnd:            true,
		}))
		cancel()
		if err != nil {
			return fmt.Errorf("sim iteration %d failed seed=%d events=%d: %w", i+1, result.Seed, len(result.Events), err)
		}
		if (i+1)%10 == 0 || i+1 == cfg.iterations {
			fmt.Printf("sim iteration %d/%d ok seed=%d nodes=%d steps=%d events=%d elapsed=%s\n",
				i+1,
				cfg.iterations,
				result.Seed,
				len(network.Spec().Nodes),
				cfg.steps,
				len(result.Events),
				time.Since(started).Round(time.Millisecond),
			)
		}
	}
	fmt.Printf("sim loop ok iterations=%d nodes=%d steps=%d base_seed=%d elapsed=%s\n",
		cfg.iterations,
		cfg.nodes,
		cfg.steps,
		cfg.seed,
		time.Since(started).Round(time.Millisecond),
	)
	return nil
}

func normalizeSimulatedLoopConfig(cfg simulatedLoopConfig) simulatedLoopConfig {
	const maxSimulatedWindow = 100 * time.Millisecond
	if cfg.window <= 0 || cfg.window > maxSimulatedWindow {
		cfg.window = maxSimulatedWindow
	}
	maxPollInterval := cfg.window / 10
	if maxPollInterval < time.Millisecond {
		maxPollInterval = time.Millisecond
	}
	if cfg.pollInterval <= 0 || cfg.pollInterval > maxPollInterval {
		cfg.pollInterval = maxPollInterval
	}
	return cfg
}

func runSimulatedOutcomeEdgeCases(cfg simulatedLoopConfig) error {
	network, err := fuzz.NewSimulatedNetwork(fuzz.SimulatedNetworkOptions{
		NodeCount:      cfg.nodes,
		InitialActive:  cfg.nodes,
		TickOnSnapshot: true,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), simulatedScenarioTimeout(cfg, 1))
	defer cancel()

	result, err := fuzz.Runner{
		Network:     network,
		Seed:        cfg.seed,
		StepTimeout: cfg.stepTimeout,
	}.Run(ctx, fuzz.OutcomeEdgeCaseScenario(network.Spec(), fuzz.ValidatorChaosController{
		Registrar:       network,
		EndpointMutator: network,
		Jailer:          network,
	}, cfg.window, cfg.pollInterval))
	if err != nil {
		return fmt.Errorf("sim outcome edge cases failed seed=%d events=%d: %w", result.Seed, len(result.Events), err)
	}
	fmt.Printf("sim outcome edge cases ok seed=%d nodes=%d events=%d\n", result.Seed, len(network.Spec().Nodes), len(result.Events))
	return nil
}

func runSimulatedCompoundOutcomeEdgeCases(cfg simulatedLoopConfig) error {
	network, err := fuzz.NewSimulatedNetwork(fuzz.SimulatedNetworkOptions{
		NodeCount:      cfg.nodes,
		InitialActive:  cfg.nodes,
		TickOnSnapshot: true,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), simulatedScenarioTimeout(cfg, 2))
	defer cancel()

	result, err := fuzz.Runner{
		Network:     network,
		Seed:        cfg.seed,
		StepTimeout: cfg.stepTimeout,
	}.Run(ctx, fuzz.CompoundOutcomeEdgeCaseScenario(network.Spec(), fuzz.ValidatorChaosController{
		Registrar:       network,
		EndpointMutator: network,
		Jailer:          network,
	}, cfg.window, cfg.pollInterval))
	if err != nil {
		return fmt.Errorf("sim compound outcome edge cases failed seed=%d events=%d: %w", result.Seed, len(result.Events), err)
	}
	fmt.Printf("sim compound outcome edge cases ok seed=%d nodes=%d events=%d\n", result.Seed, len(network.Spec().Nodes), len(result.Events))
	return nil
}

func runSimulatedPowerSkewOutcomeEdgeCases(cfg simulatedLoopConfig) error {
	const nodeCount = 5
	nodePowers := map[fuzz.NodeID]int64{
		"node1": 40,
		"node2": 15,
		"node3": 15,
		"node4": 15,
		"node5": 15,
	}
	network, err := fuzz.NewSimulatedNetwork(fuzz.SimulatedNetworkOptions{
		NodeCount:      nodeCount,
		InitialActive:  nodeCount,
		NodePowers:     nodePowers,
		TickOnSnapshot: true,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), simulatedScenarioTimeout(cfg, 2))
	defer cancel()

	result, err := fuzz.Runner{
		Network:     network,
		Seed:        cfg.seed,
		StepTimeout: cfg.stepTimeout,
	}.Run(ctx, fuzz.PowerSkewOutcomeScenario(network.Spec(), fuzz.ValidatorChaosController{
		Registrar:       network,
		EndpointMutator: network,
		Jailer:          network,
	}, "node1", []fuzz.NodeID{"node4", "node5"}, cfg.window, cfg.pollInterval))
	if err != nil {
		return fmt.Errorf("sim power-skew outcome edge cases failed seed=%d events=%d: %w", result.Seed, len(result.Events), err)
	}
	fmt.Printf("sim power-skew outcome edge cases ok seed=%d nodes=%d events=%d\n", result.Seed, len(network.Spec().Nodes), len(result.Events))
	return nil
}

func runSimulatedPowerBoundaryOutcomeEdgeCases(cfg simulatedLoopConfig) error {
	nodeCount := clampSimNodeCount(cfg.nodes, 5)
	network, err := fuzz.NewSimulatedNetwork(fuzz.SimulatedNetworkOptions{
		NodeCount:      nodeCount,
		InitialActive:  nodeCount,
		NodePowers:     fuzz.SeededValidatorPowers(nodeCount, cfg.seed+0x5eed),
		TickOnSnapshot: true,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), simulatedScenarioTimeout(cfg, 2))
	defer cancel()

	result, err := fuzz.Runner{
		Network:     network,
		Seed:        cfg.seed,
		StepTimeout: cfg.stepTimeout,
	}.Run(ctx, fuzz.PowerBoundaryOutcomeScenario(cfg.window, cfg.pollInterval))
	if err != nil {
		return fmt.Errorf("sim power-boundary outcome edge cases failed seed=%d events=%d: %w", result.Seed, len(result.Events), err)
	}
	fmt.Printf("sim power-boundary outcome edge cases ok seed=%d nodes=%d events=%d\n", result.Seed, len(network.Spec().Nodes), len(result.Events))
	return nil
}

func runSimulatedQuorumLossRecovery(cfg simulatedLoopConfig) error {
	network, err := fuzz.NewSimulatedNetwork(fuzz.SimulatedNetworkOptions{
		NodeCount:      cfg.nodes,
		InitialActive:  cfg.nodes,
		TickOnSnapshot: true,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), simulatedScenarioTimeout(cfg, 1))
	defer cancel()

	result, err := fuzz.Runner{
		Network:     network,
		Seed:        cfg.seed,
		StepTimeout: cfg.stepTimeout,
	}.Run(ctx, fuzz.QuorumLossRecoveryScenario(network.Spec(), cfg.window, cfg.pollInterval))
	if err != nil {
		return fmt.Errorf("sim quorum-loss recovery failed seed=%d events=%d: %w", result.Seed, len(result.Events), err)
	}
	fmt.Printf("sim quorum-loss recovery ok seed=%d nodes=%d events=%d\n", result.Seed, len(network.Spec().Nodes), len(result.Events))
	return nil
}

func runSimulatedJailedDeregisterCompatibility(cfg simulatedLoopConfig) error {
	nodeCount := clampSimNodeCount(cfg.nodes, 4)
	network, err := fuzz.NewSimulatedNetwork(fuzz.SimulatedNetworkOptions{
		NodeCount:      nodeCount,
		InitialActive:  nodeCount,
		TickOnSnapshot: true,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), simulatedScenarioTimeout(cfg, 1))
	defer cancel()

	result, err := fuzz.Runner{
		Network:     network,
		Seed:        cfg.seed,
		StepTimeout: cfg.stepTimeout,
	}.Run(ctx, fuzz.JailedDeregisterCompatibilityScenario(network.Spec(), fuzz.ValidatorChaosController{
		Registrar: network,
		Jailer:    network,
	}, "", cfg.window, cfg.pollInterval))
	if err != nil {
		return fmt.Errorf("sim jailed-deregister compatibility failed seed=%d events=%d: %w", result.Seed, len(result.Events), err)
	}
	fmt.Printf("sim jailed-deregister compatibility ok seed=%d nodes=%d events=%d\n", result.Seed, len(network.Spec().Nodes), len(result.Events))
	return nil
}

func runSimulatedDuplicateDeregisterIdempotency(cfg simulatedLoopConfig) error {
	nodeCount := clampSimNodeCount(cfg.nodes, 4)
	network, err := fuzz.NewSimulatedNetwork(fuzz.SimulatedNetworkOptions{
		NodeCount:      nodeCount,
		InitialActive:  nodeCount,
		TickOnSnapshot: true,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), simulatedScenarioTimeout(cfg, 1))
	defer cancel()

	result, err := fuzz.Runner{
		Network:     network,
		Seed:        cfg.seed,
		StepTimeout: cfg.stepTimeout,
	}.Run(ctx, fuzz.DuplicateDeregisterIdempotencyScenario(network.Spec(), fuzz.ValidatorChaosController{
		Registrar: network,
	}, "", cfg.window, cfg.pollInterval))
	if err != nil {
		return fmt.Errorf("sim duplicate-deregister idempotency failed seed=%d events=%d: %w", result.Seed, len(result.Events), err)
	}
	fmt.Printf("sim duplicate-deregister idempotency ok seed=%d nodes=%d events=%d\n", result.Seed, len(network.Spec().Nodes), len(result.Events))
	return nil
}

func runSimulatedDuplicateJailIdempotency(cfg simulatedLoopConfig) error {
	nodeCount := clampSimNodeCount(cfg.nodes, 4)
	network, err := fuzz.NewSimulatedNetwork(fuzz.SimulatedNetworkOptions{
		NodeCount:      nodeCount,
		InitialActive:  nodeCount,
		TickOnSnapshot: true,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), simulatedScenarioTimeout(cfg, 1))
	defer cancel()

	result, err := fuzz.Runner{
		Network:     network,
		Seed:        cfg.seed,
		StepTimeout: cfg.stepTimeout,
	}.Run(ctx, fuzz.DuplicateJailIdempotencyScenario(network.Spec(), fuzz.ValidatorChaosController{
		Jailer: network,
	}, "", cfg.window, cfg.pollInterval))
	if err != nil {
		return fmt.Errorf("sim duplicate-jail idempotency failed seed=%d events=%d: %w", result.Seed, len(result.Events), err)
	}
	fmt.Printf("sim duplicate-jail idempotency ok seed=%d nodes=%d events=%d\n", result.Seed, len(network.Spec().Nodes), len(result.Events))
	return nil
}

func runSimulatedEndpointLieConsensusIsolation(cfg simulatedLoopConfig) error {
	nodeCount := clampSimNodeCount(cfg.nodes, 4)
	network, err := fuzz.NewSimulatedNetwork(fuzz.SimulatedNetworkOptions{
		NodeCount:      nodeCount,
		InitialActive:  nodeCount,
		TickOnSnapshot: true,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), simulatedScenarioTimeout(cfg, 1))
	defer cancel()

	result, err := fuzz.Runner{
		Network:     network,
		Seed:        cfg.seed,
		StepTimeout: cfg.stepTimeout,
	}.Run(ctx, fuzz.EndpointLieConsensusIsolationScenario(network.Spec(), fuzz.ValidatorChaosController{
		EndpointMutator: network,
	}, "", cfg.window, cfg.pollInterval))
	if err != nil {
		return fmt.Errorf("sim endpoint-lie consensus isolation failed seed=%d events=%d: %w", result.Seed, len(result.Events), err)
	}
	fmt.Printf("sim endpoint-lie consensus isolation ok seed=%d nodes=%d events=%d\n", result.Seed, len(network.Spec().Nodes), len(result.Events))
	return nil
}

func runSimulatedStopStartRoundTrip(cfg simulatedLoopConfig) error {
	nodeCount := clampSimNodeCount(cfg.nodes, 4)
	network, err := fuzz.NewSimulatedNetwork(fuzz.SimulatedNetworkOptions{
		NodeCount:      nodeCount,
		InitialActive:  nodeCount,
		TickOnSnapshot: true,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), simulatedScenarioTimeout(cfg, 1))
	defer cancel()

	result, err := fuzz.Runner{
		Network:     network,
		Seed:        cfg.seed,
		StepTimeout: cfg.stepTimeout,
	}.Run(ctx, fuzz.StopStartRoundTripScenario(network.Spec(), "", cfg.window, cfg.pollInterval))
	if err != nil {
		return fmt.Errorf("sim stop-start round-trip failed seed=%d events=%d: %w", result.Seed, len(result.Events), err)
	}
	fmt.Printf("sim stop-start round-trip ok seed=%d nodes=%d events=%d\n", result.Seed, len(network.Spec().Nodes), len(result.Events))
	return nil
}

func runSimulatedInactiveStartIsolation(cfg simulatedLoopConfig) error {
	nodeCount := clampSimNodeCount(cfg.nodes, 4)
	network, err := fuzz.NewSimulatedNetwork(fuzz.SimulatedNetworkOptions{
		NodeCount:      nodeCount,
		InitialActive:  nodeCount,
		TickOnSnapshot: true,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), simulatedScenarioTimeout(cfg, 1))
	defer cancel()

	result, err := fuzz.Runner{
		Network:     network,
		Seed:        cfg.seed,
		StepTimeout: cfg.stepTimeout,
	}.Run(ctx, fuzz.InactiveStartIsolationScenario(network.Spec(), fuzz.ValidatorChaosController{
		Registrar: network,
		Jailer:    network,
	}, "", cfg.window, cfg.pollInterval))
	if err != nil {
		return fmt.Errorf("sim inactive-start isolation failed seed=%d events=%d: %w", result.Seed, len(result.Events), err)
	}
	fmt.Printf("sim inactive-start isolation ok seed=%d nodes=%d events=%d\n", result.Seed, len(network.Spec().Nodes), len(result.Events))
	return nil
}

func runSimulatedNonJailedUnjailIsolation(cfg simulatedLoopConfig) error {
	nodeCount := clampSimNodeCount(cfg.nodes, 4)
	network, err := fuzz.NewSimulatedNetwork(fuzz.SimulatedNetworkOptions{
		NodeCount:      nodeCount,
		InitialActive:  nodeCount,
		TickOnSnapshot: true,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), simulatedScenarioTimeout(cfg, 1))
	defer cancel()

	result, err := fuzz.Runner{
		Network:     network,
		Seed:        cfg.seed,
		StepTimeout: cfg.stepTimeout,
	}.Run(ctx, fuzz.NonJailedUnjailIsolationScenario(network.Spec(), fuzz.ValidatorChaosController{
		Registrar: network,
		Jailer:    network,
	}, "", cfg.window, cfg.pollInterval))
	if err != nil {
		return fmt.Errorf("sim non-jailed unjail isolation failed seed=%d events=%d: %w", result.Seed, len(result.Events), err)
	}
	fmt.Printf("sim non-jailed unjail isolation ok seed=%d nodes=%d events=%d\n", result.Seed, len(network.Spec().Nodes), len(result.Events))
	return nil
}

func runSimulatedRegisterRoundTrip(cfg simulatedLoopConfig) error {
	nodeCount := clampSimNodeCount(cfg.nodes, 4)
	network, err := fuzz.NewSimulatedNetwork(fuzz.SimulatedNetworkOptions{
		NodeCount:      nodeCount,
		InitialActive:  nodeCount,
		TickOnSnapshot: true,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), simulatedScenarioTimeout(cfg, 1))
	defer cancel()

	result, err := fuzz.Runner{
		Network:     network,
		Seed:        cfg.seed,
		StepTimeout: cfg.stepTimeout,
	}.Run(ctx, fuzz.RegisterRoundTripScenario(network.Spec(), fuzz.ValidatorChaosController{
		Registrar: network,
	}, "", cfg.window, cfg.pollInterval))
	if err != nil {
		return fmt.Errorf("sim register round-trip failed seed=%d events=%d: %w", result.Seed, len(result.Events), err)
	}
	fmt.Printf("sim register round-trip ok seed=%d nodes=%d events=%d\n", result.Seed, len(network.Spec().Nodes), len(result.Events))
	return nil
}

func runSimulatedRegisterIdempotency(cfg simulatedLoopConfig) error {
	nodeCount := clampSimNodeCount(cfg.nodes, 4)
	network, err := fuzz.NewSimulatedNetwork(fuzz.SimulatedNetworkOptions{
		NodeCount:      nodeCount,
		InitialActive:  nodeCount,
		TickOnSnapshot: true,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), simulatedScenarioTimeout(cfg, 1))
	defer cancel()

	result, err := fuzz.Runner{
		Network:     network,
		Seed:        cfg.seed,
		StepTimeout: cfg.stepTimeout,
	}.Run(ctx, fuzz.RegisterIdempotencyScenario(network.Spec(), fuzz.ValidatorChaosController{
		Registrar: network,
	}, "", cfg.window, cfg.pollInterval))
	if err != nil {
		return fmt.Errorf("sim register idempotency failed seed=%d events=%d: %w", result.Seed, len(result.Events), err)
	}
	fmt.Printf("sim register idempotency ok seed=%d nodes=%d events=%d\n", result.Seed, len(network.Spec().Nodes), len(result.Events))
	return nil
}

func runSimulatedUnjailRoundTrip(cfg simulatedLoopConfig) error {
	nodeCount := clampSimNodeCount(cfg.nodes, 4)
	network, err := fuzz.NewSimulatedNetwork(fuzz.SimulatedNetworkOptions{
		NodeCount:      nodeCount,
		InitialActive:  nodeCount,
		TickOnSnapshot: true,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), simulatedScenarioTimeout(cfg, 1))
	defer cancel()

	result, err := fuzz.Runner{
		Network:     network,
		Seed:        cfg.seed,
		StepTimeout: cfg.stepTimeout,
	}.Run(ctx, fuzz.UnjailRoundTripScenario(network.Spec(), fuzz.ValidatorChaosController{
		Jailer: network,
	}, "", cfg.window, cfg.pollInterval))
	if err != nil {
		return fmt.Errorf("sim unjail round-trip failed seed=%d events=%d: %w", result.Seed, len(result.Events), err)
	}
	fmt.Printf("sim unjail round-trip ok seed=%d nodes=%d events=%d\n", result.Seed, len(network.Spec().Nodes), len(result.Events))
	return nil
}

func simulatedScenarioTimeout(cfg simulatedLoopConfig, stallWindows int) time.Duration {
	timeout := cfg.timeout
	minimumTimeout := time.Duration(stallWindows+2)*cfg.window + 10*cfg.pollInterval + 2*time.Second
	if timeout < minimumTimeout {
		timeout = minimumTimeout
	}
	return timeout
}

func clampSimNodeCount(nodes, minimum int) int {
	if nodes < minimum {
		return minimum
	}
	if nodes > fuzz.DefaultModelNodeLimit {
		return fuzz.DefaultModelNodeLimit
	}
	return nodes
}

func runModelLoop(baseSeed int64, iterations, nodes, steps int) error {
	started := time.Now()
	for i := 0; i < iterations; i++ {
		seed := baseSeed + int64(i)
		result, err := fuzz.RunValidatorLifecycleModel(seed, nodes, steps, fuzz.ValidatorSetBehaviorCurrent)
		if err != nil {
			return fmt.Errorf("model iteration %d failed seed=%d node_count=%d steps=%d height=%d: %w",
				i+1,
				result.Seed,
				result.NodeCount,
				result.Steps,
				result.Height,
				err,
			)
		}
		if (i+1)%10 == 0 || i+1 == iterations {
			fmt.Printf("model iteration %d/%d ok seed=%d nodes=%d steps=%d height=%d elapsed=%s\n",
				i+1,
				iterations,
				seed,
				result.NodeCount,
				result.Steps,
				result.Height,
				time.Since(started).Round(time.Millisecond),
			)
		}
	}
	fmt.Printf("model loop ok iterations=%d nodes=%d steps=%d base_seed=%d elapsed=%s\n",
		iterations,
		nodes,
		steps,
		baseSeed,
		time.Since(started).Round(time.Millisecond),
	)
	return nil
}

type chaosLoopConfig struct {
	liveLoopConfig
	actionNodeIDs         []fuzz.NodeID
	allowMutations        bool
	registryRPCURL        string
	registryAddress       string
	registryPrivateKey    string
	registryStakeWei      string
	registryDelegateOwner string
	chaosSteps            int
	livenessEvery         int
}

func runChaosLoop(ctx context.Context, cfg chaosLoopConfig) error {
	if !cfg.allowMutations {
		return fmt.Errorf("chaos mode sends registry transactions; pass -allow-mutations to confirm this is a disposable devnet")
	}
	stakeAmount, ok := new(big.Int).SetString(cfg.registryStakeWei, 10)
	if !ok {
		return fmt.Errorf("invalid -registry-stake-wei %q", cfg.registryStakeWei)
	}
	controller, err := fuzz.NewEthRegistryController(ctx, fuzz.EthRegistryControllerOptions{
		RPCURL:              cfg.registryRPCURL,
		RegistryAddress:     cfg.registryAddress,
		PrivateKey:          cfg.registryPrivateKey,
		StakeAmount:         stakeAmount,
		DelegateOwnerWallet: cfg.registryDelegateOwner,
	})
	if err != nil {
		return err
	}
	defer controller.Close()

	client, endpoints, err := liveClientAndEndpoints(ctx, cfg.liveLoopConfig)
	if err != nil {
		return err
	}
	spec := networkSpecFromEndpoints(endpoints)
	network, err := fuzz.NewStaticNetwork(spec, client)
	if err != nil {
		return err
	}

	minReachable := cfg.minReachable
	if minReachable <= 0 {
		minReachable = quorumCount(len(endpoints))
	}
	started := time.Now()
	for i := 0; i < cfg.iterations; i++ {
		iterationCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
		result, err := fuzz.Runner{
			Network:     network,
			Seed:        cfg.seed + int64(i),
			StepTimeout: cfg.timeout,
		}.Run(iterationCtx, fuzz.ValidatorChaosScenario(spec, fuzz.ValidatorChaosController{
			Registrar:       controller,
			EndpointMutator: controller,
		}, fuzz.ValidatorChaosOptions{
			Seed:           cfg.seed + int64(i),
			Steps:          cfg.chaosSteps,
			StepTimeout:    cfg.timeout,
			LivenessEvery:  cfg.livenessEvery,
			LivenessWithin: cfg.window,
			PollInterval:   cfg.pollInterval,
			ActionNodeIDs:  cfg.actionNodeIDs,
		}))
		cancel()
		if err != nil {
			return fmt.Errorf("chaos iteration %d failed seed=%d events=%d min_reachable=%d: %w", i+1, result.Seed, len(result.Events), minReachable, err)
		}
		fmt.Printf("chaos iteration %d/%d ok seed=%d endpoints=%d elapsed=%s\n",
			i+1,
			cfg.iterations,
			result.Seed,
			len(spec.Nodes),
			time.Since(started).Round(time.Millisecond),
		)
	}
	return nil
}

type liveLoopConfig struct {
	seed              int64
	iterations        int
	endpoints         []string
	discoveryEndpoint string
	maxEndpoints      int
	minReachable      int
	insecureTLS       bool
	scheme            string
	timeout           time.Duration
	window            time.Duration
	pollInterval      time.Duration
}

func runLiveLoop(ctx context.Context, cfg liveLoopConfig) error {
	client, endpoints, err := liveClientAndEndpoints(ctx, cfg)
	if err != nil {
		return err
	}
	minReachable := cfg.minReachable
	if minReachable <= 0 {
		minReachable = quorumCount(len(endpoints))
	}
	if minReachable > len(endpoints) {
		return fmt.Errorf("-min-reachable cannot exceed endpoint count %d", len(endpoints))
	}

	spec := networkSpecFromEndpoints(endpoints)
	network, err := fuzz.NewStaticNetwork(spec, client)
	if err != nil {
		return err
	}

	started := time.Now()
	for i := 0; i < cfg.iterations; i++ {
		iterationCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
		result, err := fuzz.Runner{
			Network:     network,
			Seed:        cfg.seed + int64(i),
			StepTimeout: cfg.timeout,
		}.Run(iterationCtx, fuzz.LiveLivenessScenario(minReachable, 1, cfg.window, cfg.pollInterval))
		cancel()
		if err != nil {
			return fmt.Errorf("live iteration %d failed seed=%d events=%d: %w", i+1, result.Seed, len(result.Events), err)
		}
		fmt.Printf("live iteration %d/%d ok seed=%d endpoints=%d elapsed=%s\n",
			i+1,
			cfg.iterations,
			result.Seed,
			len(spec.Nodes),
			time.Since(started).Round(time.Millisecond),
		)
	}
	return nil
}

func liveClientAndEndpoints(ctx context.Context, cfg liveLoopConfig) (*fuzz.Client, []string, error) {
	client := fuzz.NewClient(
		fuzz.WithDefaultScheme(cfg.scheme),
		fuzz.WithRequestTimeout(5*time.Second),
	)
	if cfg.insecureTLS {
		client = fuzz.NewClient(
			fuzz.WithDefaultScheme(cfg.scheme),
			fuzz.WithRequestTimeout(5*time.Second),
			fuzz.WithHTTPClient(&http.Client{
				Timeout: 5 * time.Second,
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // operator opt-in for local self-signed nodes
				},
			}),
		)
	}

	endpoints := cfg.endpoints
	if len(endpoints) == 0 {
		if cfg.discoveryEndpoint == "" {
			return nil, nil, fmt.Errorf("live mode requires -endpoints or -discovery-endpoint")
		}
		discovered, err := client.DiscoverValidatorEndpoints(ctx, cfg.discoveryEndpoint)
		if err != nil {
			return nil, nil, err
		}
		endpoints = discovered
	}
	if cfg.maxEndpoints > 0 && len(endpoints) > cfg.maxEndpoints {
		endpoints = endpoints[:cfg.maxEndpoints]
	}
	return client, endpoints, nil
}

func networkSpecFromEndpoints(endpoints []string) fuzz.NetworkSpec {
	nodes := make([]fuzz.NodeSpec, 0, len(endpoints))
	for i, endpoint := range endpoints {
		nodes = append(nodes, fuzz.NodeSpec{
			ID:       fuzz.NodeID(fmt.Sprintf("node%d", i+1)),
			Endpoint: endpoint,
		})
	}
	return fuzz.NetworkSpec{Name: "live", Nodes: nodes}
}

func quorumCount(nodes int) int {
	if nodes <= 0 {
		return 0
	}
	return (nodes*2)/3 + 1
}

func splitCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func nodeIDs(raw []string) []fuzz.NodeID {
	out := make([]fuzz.NodeID, 0, len(raw))
	for _, item := range raw {
		if item != "" {
			out = append(out, fuzz.NodeID(item))
		}
	}
	return out
}
