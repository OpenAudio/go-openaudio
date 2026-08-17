package performance

import (
	"context"
	"fmt"
)

// Source supplies consensus-derived, epoch-frozen inputs. Production Core can
// implement this interface once useful work has an authoritative per-operator
// metric; tests and offline tooling can provide immutable input fixtures now.
type Source interface {
	PerformanceInputs(context.Context, Epoch) ([]OperatorInput, error)
}

// Generate loads inputs through Core's source seam and creates a snapshot.
func Generate(ctx context.Context, source Source, epoch Epoch, version Hash) (*Snapshot, error) {
	if source == nil {
		return nil, fmt.Errorf("performance source is nil")
	}
	inputs, err := source.PerformanceInputs(ctx, epoch)
	if err != nil {
		return nil, fmt.Errorf("load performance inputs: %w", err)
	}
	return BuildSnapshotForVersion(epoch, version, inputs)
}
