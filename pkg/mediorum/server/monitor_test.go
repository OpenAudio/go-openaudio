package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestRunBucketWriteCanary(t *testing.T) {
	ss := &MediorumServer{bucket: openMemBucket(t), logger: zap.NewNop()}

	ss.bucketWriteErr = "stale"
	ss.runBucketWriteCanary(context.Background())
	assert.Empty(t, ss.bucketWriteErr, "healthy bucket should clear the error")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ss.runBucketWriteCanary(ctx)
	assert.NotEmpty(t, ss.bucketWriteErr, "failed write should surface as error")

	ss.runBucketWriteCanary(context.Background())
	assert.Empty(t, ss.bucketWriteErr, "next success should clear the error")
}
