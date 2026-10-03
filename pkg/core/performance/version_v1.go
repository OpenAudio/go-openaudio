package performance

import "fmt"

// ScoreV1 gives equal weight to storage, useful-work, and block-production
// success ratios. Each component is floored to basis points and the final
// mean is floored, yielding a stable score in [0, 10,000]. A component with
// no assigned opportunities scores zero rather than being silently perfect.
func ScoreV1(input OperatorInput) (uint64, error) {
	storage, err := metricBasisPoints("storage", input.Storage)
	if err != nil {
		return 0, err
	}
	usefulWork, err := metricBasisPoints("useful work", input.UsefulWork)
	if err != nil {
		return 0, err
	}
	blocks, err := metricBasisPoints("block production", input.BlockProduction)
	if err != nil {
		return 0, err
	}
	return (storage + usefulWork + blocks) / 3, nil
}

func metricBasisPoints(name string, metric Metric) (uint64, error) {
	if metric.Completed > metric.Total {
		return 0, fmt.Errorf("%w: %s completed %d exceeds total %d", ErrInvalidMetric, name, metric.Completed, metric.Total)
	}
	if metric.Total == 0 {
		return 0, nil
	}
	value, err := FloorMulDiv(metric.Completed, MaxScore, metric.Total)
	if err != nil {
		return 0, fmt.Errorf("%w: %s ratio: %v", ErrInvalidMetric, name, err)
	}
	return value, nil
}

func score(version Hash, input OperatorInput) (uint64, error) {
	switch version {
	case scoringVersionV1:
		return ScoreV1(input)
	default:
		return 0, fmt.Errorf("%w: %x", ErrUnsupportedVersion, version)
	}
}
