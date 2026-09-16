package server

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
)

// CoreService is handed out before core.Run has registered the inner Server:
// the GET routes in main.go call its methods directly, and mediorum, the
// system service, and the health check hold the pointer in-process. None of
// those paths go through a transport interceptor, so readiness has to be
// enforced by the methods themselves. Before SetCore every method that
// touches the inner Server must refuse with CodeUnavailable rather than
// dereference nil in the caller's goroutine.

// unreadyCalls lists every CoreService RPC that needs the inner Server, with a
// call that would dereference it. Each must refuse on a fresh service.
var unreadyCalls = map[string]func(context.Context, *CoreService) error{
	"GetNodeInfo": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetNodeInfo(ctx, connect.NewRequest(&v1.GetNodeInfoRequest{}))
		return err
	},
	"ForwardTransaction": func(ctx context.Context, c *CoreService) error {
		_, err := c.ForwardTransaction(ctx, connect.NewRequest(&v1.ForwardTransactionRequest{}))
		return err
	},
	"GetBlock": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetBlock(ctx, connect.NewRequest(&v1.GetBlockRequest{Height: 1}))
		return err
	},
	"GetBlocks": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetBlocks(ctx, connect.NewRequest(&v1.GetBlocksRequest{}))
		return err
	},
	"GetDeregistrationAttestation": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetDeregistrationAttestation(ctx, connect.NewRequest(&v1.GetDeregistrationAttestationRequest{
			Deregistration: &v1.ValidatorDeregistration{},
		}))
		return err
	},
	"GetRegistrationAttestation": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetRegistrationAttestation(ctx, connect.NewRequest(&v1.GetRegistrationAttestationRequest{
			Registration: &v1.ValidatorRegistration{},
		}))
		return err
	},
	"GetTransaction": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetTransaction(ctx, connect.NewRequest(&v1.GetTransactionRequest{TxHash: "abc"}))
		return err
	},
	"SendTransaction": func(ctx context.Context, c *CoreService) error {
		_, err := c.SendTransaction(ctx, connect.NewRequest(&v1.SendTransactionRequest{
			Transaction: &v1.SignedTransaction{
				Transaction: &v1.SignedTransaction_ContentAttestation{ContentAttestation: &v1.ContentAttestation{}},
			},
		}))
		return err
	},
	"GetStoredSnapshots": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetStoredSnapshots(ctx, connect.NewRequest(&v1.GetStoredSnapshotsRequest{}))
		return err
	},
	"GetRewardAttestation": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetRewardAttestation(ctx, connect.NewRequest(&v1.GetRewardAttestationRequest{}))
		return err
	},
	"GetRewards": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetRewards(ctx, connect.NewRequest(&v1.GetRewardsRequest{}))
		return err
	},
	"GetReward": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetReward(ctx, connect.NewRequest(&v1.GetRewardRequest{Address: "abc"}))
		return err
	},
	"GetRewardPool": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetRewardPool(ctx, connect.NewRequest(&v1.GetRewardPoolRequest{}))
		return err
	},
	"GetERN": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetERN(ctx, connect.NewRequest(&v1.GetERNRequest{Address: "abc"}))
		return err
	},
	"GetMEAD": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetMEAD(ctx, connect.NewRequest(&v1.GetMEADRequest{Address: "abc"}))
		return err
	},
	"GetPIE": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetPIE(ctx, connect.NewRequest(&v1.GetPIERequest{Address: "abc"}))
		return err
	},
	"GetStreamURLs": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetStreamURLs(ctx, connect.NewRequest(&v1.GetStreamURLsRequest{}))
		return err
	},
	"GetUploadByCID": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetUploadByCID(ctx, connect.NewRequest(&v1.GetUploadByCIDRequest{Cid: "abc"}))
		return err
	},
	"GetSlashAttestation": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetSlashAttestation(ctx, connect.NewRequest(&v1.GetSlashAttestationRequest{}))
		return err
	},
	"GetSlashAttestations": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetSlashAttestations(ctx, connect.NewRequest(&v1.GetSlashAttestationsRequest{}))
		return err
	},
	"GetRewardSenderAttestation": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetRewardSenderAttestation(ctx, connect.NewRequest(&v1.GetRewardSenderAttestationRequest{}))
		return err
	},
	"GetDeleteRewardSenderAttestation": func(ctx context.Context, c *CoreService) error {
		_, err := c.GetDeleteRewardSenderAttestation(ctx, connect.NewRequest(&v1.GetDeleteRewardSenderAttestationRequest{}))
		return err
	},
	// StreamBlocks must refuse before it touches the stream, so a nil stream
	// is safe here and proves the guard runs first.
	"StreamBlocks": func(ctx context.Context, c *CoreService) error {
		return c.StreamBlocks(ctx, connect.NewRequest(&v1.StreamBlocksRequest{}), nil)
	},
}

// readyReporters are the RPCs that must keep answering before core registers,
// because they are how callers find out that it has not.
var readyReporters = map[string]bool{
	"GetStatus": true,
	"GetHealth": true,
	"Ping":      true,
}

func assertUnavailable(t *testing.T, err error) {
	t.Helper()
	var cerr *connect.Error
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeUnavailable {
		t.Fatalf("want CodeUnavailable, got %v", err)
	}
}

func TestEveryRPCRefusesBeforeCoreRegisters(t *testing.T) {
	for name, call := range unreadyCalls {
		t.Run(name, func(t *testing.T) {
			assertUnavailable(t, call(context.Background(), NewCoreService()))
		})
	}
}

// Every method on the generated handler interface is either a readiness
// reporter or covered above, so adding an RPC without a guard fails here.
func TestEveryHandlerMethodIsCovered(t *testing.T) {
	iface := reflect.TypeOf((*v1connect.CoreServiceHandler)(nil)).Elem()
	for i := 0; i < iface.NumMethod(); i++ {
		name := iface.Method(i).Name
		if readyReporters[name] {
			continue
		}
		if _, ok := unreadyCalls[name]; !ok {
			t.Errorf("%s is not covered by unreadyCalls; add a readiness guard and a case for it", name)
		}
	}
}

func TestInProcessHelpersRefuseBeforeCoreRegisters(t *testing.T) {
	c := NewCoreService()
	ctx := context.Background()

	if c.IsReady() {
		t.Fatal("fresh service reports ready")
	}
	_, err := c.GetConsensusNodeEndpoints(ctx)
	assertUnavailable(t, err)
	_, err = c.IsCidClaimedByUser(ctx, "abc", 1)
	assertUnavailable(t, err)
	if c.GetConfig() != nil {
		t.Fatal("GetConfig should be nil before core registers")
	}
	if c.GetEthService() != nil {
		t.Fatal("GetEthService should be nil before core registers")
	}
}

// GetStatus is the readiness probe, so before registration it must answer
// "live, not ready" instead of refusing.
func TestGetStatusReportsNotReadyBeforeCoreRegisters(t *testing.T) {
	c := NewCoreService()
	res, err := c.GetStatus(context.Background(), connect.NewRequest(&v1.GetStatusRequest{}))
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if !res.Msg.Live {
		t.Error("want Live=true")
	}
	if res.Msg.Ready {
		t.Error("want Ready=false")
	}
	if res.Msg.ChainInfo != nil || res.Msg.NodeInfo != nil {
		t.Error("want no chain or node info before core registers")
	}
}

func TestHealthAndPingWorkBeforeCoreRegisters(t *testing.T) {
	c := NewCoreService()
	ctx := context.Background()
	if _, err := c.GetHealth(ctx, connect.NewRequest(&v1.GetHealthRequest{})); err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	res, err := c.Ping(ctx, connect.NewRequest(&v1.PingRequest{}))
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if res.Msg.Message != "pong" {
		t.Fatalf("Ping: got %q", res.Msg.Message)
	}
}

// Mediorum asks core for the rules before every attestation decision. Before
// core registers there is no chain to ask, and the answer must be an error,
// never a default that a caller could mistake for "no rule here".
func TestNextBlockRulesBeforeCoreRegistersErrors(t *testing.T) {
	if _, err := NewCoreService().NextBlockRules(); err == nil {
		t.Fatal("rules must be unavailable before core registers")
	}
}
