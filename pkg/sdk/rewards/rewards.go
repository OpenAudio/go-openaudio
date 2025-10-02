package rewards

import (
	"context"
	"crypto/ecdsa"
	"errors"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	corev1connect "github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	"github.com/OpenAudio/go-openaudio/pkg/common"
)

type Rewards struct {
	privKey *ecdsa.PrivateKey
	core    corev1connect.CoreServiceClient
}

func NewRewards(core corev1connect.CoreServiceClient) *Rewards {
	return &Rewards{
		core: core,
	}
}

func (r *Rewards) SetPrivKey(privKey *ecdsa.PrivateKey) {
	r.privKey = privKey
}

func (r *Rewards) CreateReward(ctx context.Context, cr *v1.CreateReward) (*v1.GetRewardResponse, error) {
	sig, err := common.SignCreateReward(r.privKey, cr)
	if err != nil {
		return nil, err
	}
	cr.Signature = sig

	tx := &v1.SendTransactionRequest{
		Transaction: &v1.SignedTransaction{
			Transaction: &v1.SignedTransaction_Reward{
				Reward: &v1.RewardMessage{
					Action: &v1.RewardMessage_Create{Create: cr},
				},
			},
		},
	}

	req := connect.NewRequest(tx)
	resp, err := r.core.SendTransaction(ctx, req)
	if err != nil {
		return nil, err
	}

	txhash := resp.Msg.Transaction.Hash
	reward, err := r.core.GetReward(ctx, connect.NewRequest(&v1.GetRewardRequest{
		Txhash: txhash,
	}))
	if err != nil {
		return nil, err
	}

	return reward.Msg, nil
}

func (r *Rewards) DeleteReward(ctx context.Context, dr *v1.DeleteReward) (string, error) {
	sig, err := common.SignDeleteReward(r.privKey, dr)
	if err != nil {
		return "", err
	}
	dr.Signature = sig

	tx := &v1.SendTransactionRequest{
		Transaction: &v1.SignedTransaction{
			Transaction: &v1.SignedTransaction_Reward{
				Reward: &v1.RewardMessage{
					Action: &v1.RewardMessage_Delete{Delete: dr},
				},
			},
		},
	}

	req := connect.NewRequest(tx)
	deleteRes, err := r.core.SendTransaction(ctx, req)
	if err != nil {
		return "", err
	}

	txhash := deleteRes.Msg.GetTransaction().GetHash()
	return txhash, nil
}

func (r *Rewards) GetReward(ctx context.Context, address string) (*v1.GetRewardResponse, error) {
	req := connect.NewRequest(&v1.GetRewardRequest{
		Address: address,
	})
	resp, err := r.core.GetReward(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (r *Rewards) GetRewards(ctx context.Context, claim_authority string) (*v1.GetRewardsResponse, error) {
	if claim_authority == "" {
		return nil, errors.New("claim_authority required")
	}
	req := connect.NewRequest(&v1.GetRewardsRequest{
		ClaimAuthority: claim_authority,
	})
	resp, err := r.core.GetRewards(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (r *Rewards) GetRewardAttestation(ctx context.Context, req *v1.GetRewardAttestationRequest) (*v1.GetRewardAttestationResponse, error) {
	sigData := &v1.RewardAttestationSignature{
		EthRecipientAddress: req.EthRecipientAddress,
		Amount:              req.Amount,
		RewardAddress:       req.RewardAddress,
		RewardId:            req.RewardId,
		Specifier:           req.Specifier,
		ClaimAuthority:      req.ClaimAuthority,
	}

	sig, err := common.ProtoSign(r.privKey, sigData)
	if err != nil {
		return nil, err
	}

	req.Signature = sig

	connectReq := connect.NewRequest(req)
	resp, err := r.core.GetRewardAttestation(ctx, connectReq)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}
