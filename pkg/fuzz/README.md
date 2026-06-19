# OpenAudio Fuzz Harness

`pkg/fuzz` is an opt-in harness for running many OpenAudio nodes through odd validator lifecycle states and asserting that the network keeps making progress.

This package is intentionally separate from the core runtime. It observes nodes through public HTTP surfaces and can manage local processes when a scenario provides commands. That keeps the first version usable from Go on macOS without requiring Docker, while leaving room to move the package into its own repository later.

## What exists now

- A scenario runner with deterministic seeds and event capture.
- Static network support for asserting against already-running endpoints.
- Local process support for nodes whose `NodeSpec` includes a command.
- HTTP observation through `/health-check`, `/core.v1.CoreService/GetStatus`, and `/core/crpc/status`.
- Reusable actions for wait/start/stop/restart/hook/parallel/sequence.
- Interface-backed register, deregister, and advertised-endpoint mutation actions.
- Reusable assertions for reachability, quorum readiness, height advancement, height regression, and validator voting power.
- A pure validator-lifecycle model that can stress up to 300 nodes and catches invalid Comet validator-update emissions before running real processes.
- A seedable validator-chaos scenario generator that composes stop/restart, register/deregister, jail/unjail, endpoint mutation, and periodic liveness assertions.
- An in-memory `SimulatedNetwork` that runs the same chaos scenarios through the real runner/controller interfaces at up to 300 nodes without Docker.
- Outcome edge-case scenarios that leave faults in place and assert the net result: the chain advances when quorum should survive, stalls when quorum is intentionally lost, and recovers after repair.
- Compound outcome scenarios that mix endpoint lies, stopped validators, deregistered cohorts, jailed cohorts, duplicate removals, and quorum-boundary transitions while asserting only the expected chain outcome.
- Power-skew outcome scenarios where node-count quorum and voting-power quorum disagree, such as one high-power validator being enough to halt progress even when most nodes are still live.
- Power-boundary outcome scenarios that inspect observed validator power at runtime, stop the largest partition that should still allow progress, then stop one more validator and assert the chain stalls.
- A validator-quorum outcome oracle that inspects the resulting validator power after a chaos action and asserts height advances or stalls accordingly, with fork and height-regression checks bundled into generated chaos outcomes.
- Live-validator height convergence assertions, so one progressing validator cannot mask other reachable live validators being stuck behind.
- Live-validator block-hash agreement checks, so same-height forks cannot hide behind successful height progression.
- Persistent-fault simulated chaos that can accumulate stops, deregistrations, jails, and endpoint lies across many generated steps instead of always repairing inside the same action.
- Opt-in recovery sweeps for generated simulated chaos that repair all controllable faults, then assert the chain advances again and validator power returns to its pre-chaos baseline.
- A jailed-then-deregistered compatibility scenario that captures post-jail validator power, then asserts the formal deregistration does not create a new halt, validator-set change, or live-validator fork.
- A duplicate-deregister idempotency scenario that captures post-deregister validator power, then asserts repeating the deregistration does not create a new halt, validator-set change, or live-validator fork.
- A duplicate-jail idempotency scenario that captures post-jail validator power, then asserts repeating the jail action does not create a new halt, validator-set change, or live-validator fork.
- An endpoint-lie consensus-isolation scenario that captures validator power, then asserts a bad advertised endpoint does not change consensus power, halt progress, or fork live validators.
- An endpoint-repair idempotency scenario that asserts repairing an already-honest endpoint, repairing a bad endpoint, and duplicate repair do not alter validator power, halt progress, or fork live validators.
- An endpoint-register round-trip scenario that advertises a bad endpoint, deregisters the validator, then asserts registration restores both validator power and externally observable endpoint health.
- A cohort-endpoint consensus-isolation scenario that advertises and repairs bad endpoints for a quorum-impacting validator cohort, then asserts validator power and consensus outcome remain unchanged.
- An inactive-endpoint isolation scenario that asserts endpoint updates and repairs for jailed or absent validators do not resurrect validator power, halt progress, or fork live validators.
- A stop/start round-trip scenario that captures initial live validator power, stops a validator, then asserts starting it restores the original live validator outcome.
- An inactive-start isolation scenario that jails and deregisters a validator, then asserts starting that inactive process does not resurrect validator power, halt progress, or fork live validators.
- A non-jailed unjail isolation scenario that asserts unjailing an already-active or absent validator does not alter validator power, halt progress, or fork live validators.
- A register round-trip scenario that captures initial validator power, deregisters a validator, then asserts registering it restores the original validator set and consensus outcome.
- A jailed-register round-trip scenario that captures initial validator power, jails a validator, then asserts registration restores the original validator set and consensus outcome.
- A register idempotency scenario that asserts registering an already-active validator, and registering again after restore, does not alter validator power, halt progress, or fork live validators.
- An unjail round-trip scenario that captures initial validator power, jails a validator, then asserts unjailing it restores the original validator set and consensus outcome.
- A cohort lifecycle round-trip scenario that deregisters/registers and jails/unjails a quorum-impacting validator cohort, then asserts the original validator set and consensus outcome are restored each time.
- A mixed lifecycle quorum-recovery scenario that combines validator-set removal with process outages, asserts the chain stalls below quorum, then asserts register/unjail restores progress before the stopped processes return.
- Seeded skewed validator-power profiles for simulated fuzzing so generated programs exercise power quorum, not only node-count quorum.
- A quorum-loss recovery scenario that intentionally drops equal-power validator sets below quorum, asserts height stalls, restarts the cohort, and asserts height resumes.
- An opt-in live liveness test guarded by `OPENAUDIO_FUZZ_RUN=1`.

## Running against an existing devnet

```sh
OPENAUDIO_FUZZ_RUN=1 \
OPENAUDIO_FUZZ_ENDPOINTS=https://node1.oap.devnet,https://node2.oap.devnet,https://node3.oap.devnet,https://node4.oap.devnet \
OPENAUDIO_FUZZ_INSECURE_TLS=1 \
go test ./pkg/fuzz -run TestLiveLivenessScenario -count=1 -v
```

`OPENAUDIO_FUZZ_ENDPOINTS` may point at any reachable validator-like nodes. Use `OPENAUDIO_FUZZ_SCHEME=http` when endpoints are provided without a scheme and should default to HTTP.

You can also discover the current validator endpoints from a console node:

```sh
OPENAUDIO_FUZZ_RUN=1 \
OPENAUDIO_FUZZ_DISCOVERY_ENDPOINT=https://creatornode.audius.co \
go test ./pkg/fuzz -run TestLiveLivenessScenario -count=1 -v
```

Use `OPENAUDIO_FUZZ_MAX_ENDPOINTS=300` to cap very large discovered sets.
Set `OPENAUDIO_FUZZ_MIN_REACHABLE` when a live run should allow some public endpoints to be slow or unreachable.

## Local process shape

Build a native binary:

```sh
make bin/openaudio-native
```

Then construct a `ProcessNetwork` with one `NodeSpec` per node:

```go
spec := fuzz.NetworkSpec{
    Name: "local-process-devnet",
    Nodes: []fuzz.NodeSpec{
        {
            ID:       "node1",
            Endpoint: "https://node1.oap.devnet",
            Command:  []string{"./bin/openaudio-native"},
            Env: map[string]string{
                "NETWORK":                   "dev",
                "OPENAUDIO_NODE_ENDPOINT":   "https://node1.oap.devnet",
                "OPENAUDIO_CORE_ROOT_DIR":   "/tmp/openaudio-fuzz/node1/core",
                "OPENAUDIO_TLS_SELF_SIGNED": "true",
            },
            LogPath: "/tmp/openaudio-fuzz/node1.log",
        },
    },
}
```

Set `ValidatorChaosOptions.StartNodes=true` when the scenario should start those commands itself.

There is one important topology constraint: current node endpoint validation expects FQDN-style URLs without explicit ports. A fully local multi-process validator set still needs stable hostnames and port routing, or a future core change outside this package. This package does not change that behavior.

## Next tactical additions

- Process profiles that generate node env, data dirs, P2P peers, and HTTP routing consistently.
- Fault actions for endpoint lies, partitions, delayed restarts, stale state, and signer/key mismatches.
- Internal/protocol-level hooks once they can be introduced without coupling the harness to production runtime code.

## Devnet Registry Controller

`EthRegistryController` implements register, deregister, and endpoint-update actions against the ServiceProviderFactory contract. It is opt-in and requires explicit RPC, registry, and private-key configuration:

```go
controller, err := fuzz.NewEthRegistryController(ctx, fuzz.EthRegistryControllerOptions{
    RPCURL:          "ws://localhost:8545",
    RegistryAddress: "0x...",
    PrivateKey:      "...",
})
```

Pass it into `ValidatorChaosController` as the `Registrar` and `EndpointMutator` to exercise real L1-backed lifecycle changes on a disposable devnet.

The `fuzzrun` command can wire this controller for a disposable devnet. It refuses to send transactions unless `-allow-mutations` is present:

```sh
go run ./pkg/fuzz/cmd/fuzzrun \
  -mode chaos \
  -allow-mutations \
  -endpoints https://node4.oap.devnet \
  -action-node-ids node1 \
  -registry-rpc-url ws://localhost:8545 \
  -registry-address 0x... \
  -registry-private-key ... \
  -chaos-steps 20 \
  -iterations 10
```

Use `-action-node-ids` to restrict registry mutations to nodes controlled by the supplied private key.

## Fast model fuzzing

The model fuzz target exercises validator lifecycle ordering without starting real nodes:

```sh
go test ./pkg/fuzz -run TestValidatorLifecycleModelStress300Nodes -count=1 -v
go test ./pkg/fuzz -run '^$' -fuzz FuzzValidatorLifecycleModel -fuzztime=30s
```

The seed corpus includes the incident class where a validator is first jailed, then formally deregistered. The current behavior must delete app state without emitting a second zero-power Comet update.

The simulated chaos fuzz target drives the real runner/controller path against the in-memory network:

```sh
go test ./pkg/fuzz -run '^$' -fuzz FuzzSimulatedChaosProgram -fuzztime=30s
```

For a replayable long-running loop:

```sh
go run ./pkg/fuzz/cmd/fuzzrun -mode model -nodes 300 -steps 20000 -iterations 1000 -seed 1
```

To exercise the full runner/controller chaos path without Docker or contract credentials:

```sh
go run ./pkg/fuzz/cmd/fuzzrun -mode sim -nodes 300 -steps 1000 -iterations 100 -seed 1
```

`sim` mode first runs outcome edge-case scenarios, compound outcome edge cases, power-skew outcome edge cases, dynamic power-boundary edge cases, quorum-loss recovery, and targeted lifecycle compatibility/round-trip scenarios, including endpoint-register recovery, jailed-register recovery, cohort endpoint-isolation, lifecycle round trips, and mixed lifecycle quorum recovery, once. It then runs the seeded chaos loop with persistent faults plus validator-quorum, live-validator convergence, and same-height block-hash agreement assertions after every generated step. The generated simulated chaos loop also repairs all controllable faults at the end and asserts the chain advances again. Because this mode is fully in-memory, long live-style assertion windows are capped to short simulated windows.

For repeated read-only live liveness checks:

```sh
go run ./pkg/fuzz/cmd/fuzzrun \
  -mode live \
  -discovery-endpoint https://creatornode.audius.co \
  -max-endpoints 50 \
  -min-reachable 34 \
  -iterations 5
```
