package performance

import (
	"context"
	"fmt"
)

// Source supplies already validated, epoch-frozen inputs to the scoring engine.
// Production batch generation uses FinalizedSource and GenerateArtifact so the
// source registration, evidence, useful-work quorum, and relayer projection are
// all enforced; this smaller seam remains useful for deterministic engines.
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
