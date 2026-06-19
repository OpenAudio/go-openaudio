package fuzz

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type ValidatorPowerBaseline struct {
	mu         sync.Mutex
	captured   bool
	totalPower int64
	livePower  int64
	summary    string
}

func CaptureValidatorPowerBaseline(baseline *ValidatorPowerBaseline) Action {
	return ActionFunc{
		Label: "capture validator power baseline",
		Fn: func(ctx context.Context, run *RunContext) error {
			if baseline == nil {
				return fmt.Errorf("%w: validator power baseline is nil", ErrInvalidScenario)
			}
			snapshot, err := run.Network.Snapshot(ctx)
			if err != nil {
				return err
			}
			totalPower, livePower := snapshot.ValidatorPower()
			if totalPower <= 0 {
				return fmt.Errorf("%w: snapshot has no validator power: %s", ErrInvalidScenario, snapshot.Summary())
			}
			baseline.capture(totalPower, livePower, snapshot.Summary())
			run.record("validator_power_baseline", fmt.Sprintf("live=%d total=%d", livePower, totalPower), snapshot.Summary())
			return nil
		},
	}
}

func ValidatorPowerRestored(baseline *ValidatorPowerBaseline, within, pollInterval time.Duration) Assertion {
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	if within <= 0 {
		within = 2 * pollInterval
	}
	return AssertionFunc{
		Label: "validator power restored to baseline",
		Fn: func(ctx context.Context, run *RunContext) error {
			wantTotal, wantLive, baselineSummary, ok := baseline.values()
			if !ok {
				return fmt.Errorf("%w: validator power baseline was not captured", ErrInvalidScenario)
			}

			deadline := time.NewTimer(within)
			defer deadline.Stop()
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()

			var last Snapshot
			var gotTotal, gotLive int64
			for {
				snapshot, err := run.Network.Snapshot(ctx)
				if err != nil {
					return err
				}
				last = snapshot
				gotTotal, gotLive = snapshot.ValidatorPower()
				if gotTotal == wantTotal && gotLive == wantLive {
					return nil
				}

				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-deadline.C:
					return fmt.Errorf(
						"validator power did not return to baseline within %s: got live=%d total=%d want live=%d total=%d baseline=%s last=%s",
						within,
						gotLive,
						gotTotal,
						wantLive,
						wantTotal,
						baselineSummary,
						last.Summary(),
					)
				case <-ticker.C:
				}
			}
		},
	}
}

func (b *ValidatorPowerBaseline) capture(totalPower, livePower int64, summary string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.captured = true
	b.totalPower = totalPower
	b.livePower = livePower
	b.summary = summary
}

func (b *ValidatorPowerBaseline) values() (totalPower, livePower int64, summary string, ok bool) {
	if b == nil {
		return 0, 0, "", false
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	return b.totalPower, b.livePower, b.summary, b.captured
}
