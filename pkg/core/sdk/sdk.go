package sdk

import (
	"context"
	"errors"
	"fmt"
	"time"

	"crypto/ecdsa"

	"github.com/AudiusProject/audiusd/pkg/common"
	ccommon "github.com/AudiusProject/audiusd/pkg/core/common"
	"github.com/AudiusProject/audiusd/pkg/core/gen/core_openapi"
	"github.com/AudiusProject/audiusd/pkg/core/gen/core_openapi/protocol"
	"github.com/AudiusProject/audiusd/pkg/core/gen/core_proto"
	adx "github.com/AudiusProject/audiusd/pkg/core/gen/core_proto/audiusddex/v1beta1"
	"github.com/cometbft/cometbft/rpc/client/http"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

type Sdk struct {
	logger      Logger
	useHttps    bool
	privKeyPath string
	privKey     *ecdsa.PrivateKey

	retries int
	delay   time.Duration

	OAPIEndpoint string
	GRPCEndpoint string
	JRPCEndpoint string
	protocol.ClientService
	core_proto.ProtocolClient
	http.HTTP
}

func defaultSdk() *Sdk {
	return &Sdk{
		logger:  NewNoOpLogger(),
		retries: 10,
		delay:   3 * time.Second,
	}
}

func initSdk(sdk *Sdk) error {
	ctx := context.Background()
	// TODO: add default environment here if not set

	// TODO: add node selection logic here, based on environment, if endpoint not configured

	g, ctx := errgroup.WithContext(ctx)

	if sdk.OAPIEndpoint != "" {
		g.Go(func() error {
			transport := core_openapi.DefaultTransportConfig().WithHost(sdk.OAPIEndpoint)
			if sdk.useHttps {
				transport.WithSchemes([]string{"https"})
			}

			retries := sdk.retries

			client := core_openapi.NewHTTPClientWithConfig(nil, transport)
			for tries := retries; tries >= 0; tries-- {
				_, err := client.Protocol.ProtocolPing(protocol.NewProtocolPingParams())
				if err == nil {
					break
				}

				if tries == 0 {
					sdk.logger.Error("exhausted openapi retries", "error", err, "endpoint", sdk.OAPIEndpoint)
					return err
				}

				time.Sleep(sdk.delay)
			}

			sdk.ClientService = client.Protocol
			return nil
		})
	}

	// initialize grpc client
	if sdk.GRPCEndpoint != "" {
		g.Go(func() error {
			grpcConn, err := grpc.NewClient(sdk.GRPCEndpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				return err
			}

			grpcClient := core_proto.NewProtocolClient(grpcConn)

			for tries := sdk.retries; tries >= 0; tries-- {
				_, err := grpcClient.Ping(ctx, &core_proto.PingRequest{})
				if err == nil {
					break
				}

				if tries == 0 {
					sdk.logger.Error("exhausted grpc retries", "error", err, "endpoint", sdk.GRPCEndpoint)
					return err
				}

				time.Sleep(sdk.delay)
			}

			sdk.ProtocolClient = grpcClient
			return nil
		})
	}

	// initialize jsonrpc client
	if sdk.JRPCEndpoint != "" {
		g.Go(func() error {
			jrpcConn, err := http.New(sdk.JRPCEndpoint)
			if err != nil {
				return err
			}

			for tries := sdk.retries; tries >= 0; tries-- {
				_, err := jrpcConn.Health(ctx)
				if err == nil {
					break
				}

				if tries == 0 {
					sdk.logger.Error("exhausted jrpc retries", "error", err, "endpoint", sdk.GRPCEndpoint)
					return err
				}

				time.Sleep(sdk.delay)
			}

			sdk.HTTP = *jrpcConn
			return nil
		})
	}

	if sdk.privKeyPath != "" {
		g.Go(func() error {
			key, err := common.LoadPrivateKey(sdk.privKeyPath)
			if err != nil {
				return err
			}
			sdk.privKey = key
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		sdk.logger.Error("init sdk error", "error", err)
		return err
	}

	return nil
}

type ReleaseResult struct {
	TxHash  string
	TrackID string
}

func (sdk *Sdk) ReleaseTrack(cid, title, genre string) (*ReleaseResult, error) {
	if sdk.privKey == nil {
		return nil, errors.New("No private key set, cannot release track")
	}
	trackId := uuid.NewString()
	ern := &adx.NewReleaseMessage{
		ReleaseHeader: &adx.ReleaseHeader{
			Sender: &adx.Party{
				PartyId: "audius_sdk",
				PubKey:  crypto.CompressPubkey(&sdk.privKey.PublicKey),
			},
		},
		ResourceList: []*adx.Resource{
			&adx.Resource{
				ResourceReference: "AT1",
				Resource: &adx.Resource_SoundRecording{
					SoundRecording: &adx.SoundRecording{
						Cid: cid,
						Id: &adx.SoundRecordingId{
							Isrc: uuid.NewString(),
						},
					},
				},
			},
		},
		ReleaseList: []*adx.Release{
			&adx.Release{
				Release: &adx.Release_TrackRelease{
					TrackRelease: &adx.TrackRelease{
						ReleaseId: &adx.ReleaseId{
							Isrc: trackId,
						},
						ReleaseResourceReference: "AT1",
						Title:                    title,
						Genre:                    genre,
					},
				},
			},
		},
	}

	ernBytes, err := proto.Marshal(ern)
	if err != nil {
		return nil, fmt.Errorf("failure to marshal ern: %v", err)
	}

	sig, err := common.EthSign(sdk.privKey, ernBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to sign message: %w", err)
	}

	tx := &core_proto.SignedTransaction{
		Signature: sig,
		RequestId: uuid.NewString(),
		Transaction: &core_proto.SignedTransaction_Release{
			Release: ern,
		},
	}
	sendParams := protocol.NewProtocolSendTransactionParams()
	sendParams.SetTransaction(ccommon.SignedTxProtoIntoSignedTxOapi(tx))
	res, err := sdk.ProtocolSendTransaction(sendParams)
	if err != nil {
		return nil, fmt.Errorf("ern failed: %w", err)
	}

	return &ReleaseResult{TrackID: trackId, TxHash: res.Payload.Txhash}, nil
}
