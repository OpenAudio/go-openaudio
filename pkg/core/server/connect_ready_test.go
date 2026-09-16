package server

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
)

// Mediorum calls SendTransaction in-process, bypassing ReadyCheckInterceptor,
// and can do so while core is still compacting its databases. Before core
// registers itself that must be a refusal, not a nil dereference in the
// caller's goroutine.
func TestSendTransactionBeforeCoreRegistersIsUnavailable(t *testing.T) {
	c := NewCoreService()
	_, err := c.SendTransaction(context.Background(), connect.NewRequest(&v1.SendTransactionRequest{
		Transaction: &v1.SignedTransaction{
			Transaction: &v1.SignedTransaction_ContentAttestation{ContentAttestation: &v1.ContentAttestation{}},
		},
	}))
	var cerr *connect.Error
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeUnavailable {
		t.Fatalf("want CodeUnavailable, got %v", err)
	}
}
