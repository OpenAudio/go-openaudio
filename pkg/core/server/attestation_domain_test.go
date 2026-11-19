package server

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/safemap"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestSlashRegistrationReplayRejectedByConsensus(t *testing.T) {
	f := newRegistrationConsensusFixture(t)
	activation := f.blockHeight + 1
	f.server.config.Upgrades.AttestationDomainHeight = activation
	reg := f.makeRegistration(t, ed25519.GenPrivKey()).GetAttestation().GetValidatorRegistration()
	reg.Endpoint = "\x08\x01"
	reg.NodeType = "\x08\x02"
	reg.SpId = ""
	reg.EthBlock = 0
	body, err := proto.Marshal(reg)
	require.NoError(t, err)
	var slash v1.SlashRecommendation
	require.NoError(t, proto.Unmarshal(body, &slash))
	slashBody, err := proto.Marshal(&slash)
	require.NoError(t, err)
	require.Equal(t, body, slashBody)
	tx := f.signRegistration(t, reg, slashBody) // Simulates harvested legacy oracle signatures.

	// Preserve historical validation, and prove the exploit rather than only
	// testing a malformed request that would have failed before the patch.
	require.NoError(t, f.server.isValidAttestation(f.ctx, tx, activation-1))
	raw, err := proto.Marshal(tx)
	require.NoError(t, err)
	valid, err := f.server.validateBlockTxs(f.ctx, time.Unix(1, 0), activation-1, nil, [][]byte{raw})
	require.NoError(t, err)
	require.True(t, valid)

	require.Error(t, f.server.isValidAttestation(f.ctx, tx, activation))
	require.Error(t, f.server.validateV1Transaction(f.ctx, activation-1, tx))
	valid, err = f.server.validateBlockTxs(f.ctx, time.Unix(1, 0), activation, nil, [][]byte{raw})
	require.NoError(t, err)
	require.False(t, valid)

	f.blockHeight = activation
	resp := f.finalizeBlock(t, tx)
	require.NotZero(t, resp.TxResults[0].Code)
	require.Empty(t, resp.ValidatorUpdates)
	_, err = f.server.getDb().GetRegisteredNodeByCometAddress(f.ctx, reg.CometAddress)
	require.ErrorIs(t, err, pgx.ErrNoRows)
}

func TestValidatorAttestationDomainsAndActivation(t *testing.T) {
	f := newRegistrationConsensusFixture(t)
	activation := f.blockHeight
	f.server.config.Upgrades.AttestationDomainHeight = activation
	tx := f.makeRegistration(t, ed25519.GenPrivKey())
	reg := tx.GetAttestation().GetValidatorRegistration()
	raw, err := proto.Marshal(reg)
	require.NoError(t, err)
	legacy := f.signRegistration(t, reg, raw)
	require.NoError(t, f.server.isValidAttestation(f.ctx, legacy, activation-1))
	require.ErrorContains(t, f.server.isValidAttestation(f.ctx, legacy, activation), "not enough attestations")
	require.Error(t, f.server.isValidAttestation(f.ctx, tx, activation-1))
	require.NoError(t, f.server.isValidAttestation(f.ctx, tx, activation))
	require.NoError(t, f.server.validateV1Transaction(f.ctx, activation-1, tx))

	wrongChain, err := domainSeparatedAttestationBytes("other-chain", reg)
	require.NoError(t, err)
	require.ErrorContains(t, f.server.isValidAttestation(f.ctx, f.signRegistration(t, reg, wrongChain), activation), "not enough attestations")
	// Force identical wire bodies with a different protobuf type, independently
	// of registration shape checks, to verify actual cryptographic separation.
	wrongType := &v1.ValidatorDeregistration{}
	wrongType.ProtoReflect().SetUnknown(raw)
	typed, err := domainSeparatedAttestationBytes("test", wrongType)
	require.NoError(t, err)
	require.ErrorContains(t, f.server.isValidAttestation(f.ctx, f.signRegistration(t, reg, typed), activation), "not enough attestations")

	// Duplicate attestations must not manufacture a quorum.
	domain, err := f.server.validatorAttestationBytes(reg, activation)
	require.NoError(t, err)
	oldSigners := f.attestorSigners
	f.attestorSigners = append(oldSigners[:0:0], oldSigners[0], oldSigners[0], oldSigners[0])
	duplicate := f.signRegistration(t, reg, domain)
	f.attestorSigners = oldSigners
	require.ErrorContains(t, f.server.isValidAttestation(f.ctx, duplicate, activation), "not enough attestations")
	duplicate.GetAttestation().Signatures = append(duplicate.GetAttestation().Signatures, "malformed")
	require.ErrorContains(t, f.server.isValidAttestation(f.ctx, duplicate, activation), "more signatures")

	validTx := f.signRegistration(t, reg, domain)
	resp := f.finalizeBlock(t, validTx)
	require.Zero(t, resp.TxResults[0].Code)
	require.Len(t, resp.ValidatorUpdates, 1)
	requireValidatorUpdatesHaveAppRows(t, f, resp.ValidatorUpdates)
}

type slashProofPeer struct {
	v1connect.CoreServiceClient
	signature string
}

func (p slashProofPeer) GetSlashAttestation(context.Context, *connect.Request[v1.GetSlashAttestationRequest]) (*connect.Response[v1.GetSlashAttestationResponse], error) {
	return connect.NewResponse(&v1.GetSlashAttestationResponse{Signature: p.signature}), nil
}

func TestGatherSlashAttestationsDuringUpgrade(t *testing.T) {
	f := newRegistrationConsensusFixture(t)
	slash := validSlashRecommendation()
	versioned, err := SlashRecommendationSignBytes(testChainID, slash)
	require.NoError(t, err)
	raw, err := proto.Marshal(slash)
	require.NoError(t, err)
	validProof := f.attestorSigners[0](versioned)
	proofs := []string{validProof, f.attestorSigners[1](raw), validProof}
	f.server.connectRPCPeers = safemap.New[EthAddress, v1connect.CoreServiceClient]()
	for i, sign := range f.attestorSigners {
		_, address, err := common.EthRecover(sign(versioned), versioned)
		require.NoError(t, err)
		f.server.connectRPCPeers.Set(address, slashProofPeer{signature: proofs[i]})
	}
	got, err := f.server.gatherSlashAttestations(f.ctx, slash)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"https://attestor-0.example.com": validProof}, got)
}

func TestDeregistrationAttestationDomain(t *testing.T) {
	f := newRegistrationConsensusFixture(t)
	f.server.config.AttDeregistrationRSize = 3
	f.server.config.AttDeregistrationMin = 3
	cometKey := ed25519.GenPrivKey()
	node := testNode
	node.CometAddress = cometKey.PubKey().Address().String()
	require.NoError(t, f.q.InsertRegisteredNode(f.ctx, node))
	dereg := &v1.ValidatorDeregistration{
		CometAddress: node.CometAddress,
		PubKey:       cometKey.PubKey().Bytes(),
		Deadline:     f.blockHeight + 10,
	}
	att := &v1.Attestation{Body: &v1.Attestation_ValidatorDeregistration{ValidatorDeregistration: dereg}}
	tx := &v1.SignedTransaction{Transaction: &v1.SignedTransaction_Attestation{Attestation: att}}
	body, err := f.server.validatorAttestationBytes(dereg, f.blockHeight)
	require.NoError(t, err)
	for _, sign := range f.attestorSigners {
		att.Signatures = append(att.Signatures, sign(body))
	}
	require.NoError(t, f.server.isValidAttestation(f.ctx, tx, f.blockHeight))
	raw, err := proto.Marshal(dereg)
	require.NoError(t, err)
	att.Signatures = nil
	for _, sign := range f.attestorSigners {
		att.Signatures = append(att.Signatures, sign(raw))
	}
	require.ErrorContains(t, f.server.isValidAttestation(f.ctx, tx, f.blockHeight), "not enough attestations")
}

func TestMalformedRegistrationRejected(t *testing.T) {
	f := newRegistrationConsensusFixture(t)
	base := f.makeRegistration(t, ed25519.GenPrivKey()).GetAttestation().GetValidatorRegistration()
	tests := map[string]func(*v1.ValidatorRegistration){
		"control endpoint":   func(r *v1.ValidatorRegistration) { r.Endpoint = "\x08\x01" },
		"no host":            func(r *v1.ValidatorRegistration) { r.Endpoint = "https:///" },
		"unsupported scheme": func(r *v1.ValidatorRegistration) { r.Endpoint = "ftp://example.com" },
		"credentials":        func(r *v1.ValidatorRegistration) { r.Endpoint = "https://user@example.com" },
		"bad node type":      func(r *v1.ValidatorRegistration) { r.NodeType = "\x08\x02" },
		"missing SP ID":      func(r *v1.ValidatorRegistration) { r.SpId = "" },
		"negative SP ID":     func(r *v1.ValidatorRegistration) { r.SpId = "-1" },
		"zero block":         func(r *v1.ValidatorRegistration) { r.EthBlock = 0 },
		"negative block":     func(r *v1.ValidatorRegistration) { r.EthBlock = -1 },
		"short pubkey":       func(r *v1.ValidatorRegistration) { r.PubKey = []byte{1} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			reg := proto.Clone(base).(*v1.ValidatorRegistration)
			mutate(reg)
			body, err := domainSeparatedAttestationBytes("test", reg)
			require.NoError(t, err)
			tx := f.signRegistration(t, reg, body)
			require.Error(t, f.server.isValidAttestation(f.ctx, tx, f.blockHeight))
		})
	}
}

func TestMempoolBoundsAttestationSignaturesBeforeActivation(t *testing.T) {
	f := newRegistrationConsensusFixture(t)
	f.server.config.Upgrades.AttestationDomainHeight = 0
	tx := f.makeRegistration(t, ed25519.GenPrivKey())
	require.NoError(t, f.server.validateV1Transaction(f.ctx, f.blockHeight, tx))

	att := tx.GetAttestation()
	for len(att.Signatures) <= f.server.config.AttRegistrationRSize {
		att.Signatures = append(att.Signatures, "malformed")
	}
	// recoverSigners starts a goroutine per signature, so admission bounds the
	// count on every chain, including those with no activation height yet.
	require.ErrorContains(t, f.server.validateV1Transaction(f.ctx, f.blockHeight, tx), "more signatures")

	empty := &v1.SignedTransaction{Transaction: &v1.SignedTransaction_Attestation{}}
	require.Error(t, f.server.validateV1Transaction(f.ctx, f.blockHeight, empty))
}

func TestRefreshPeersSkipsMalformedRegisteredEndpoint(t *testing.T) {
	f := newRegistrationConsensusFixture(t)
	bad := testNode
	bad.Endpoint = "\x08\x01"
	require.NoError(t, f.q.InsertRegisteredNode(f.ctx, bad))
	f.server.peerStatus = safemap.New[EthAddress, *v1.GetStatusResponse_PeerInfo_Peer]()
	f.server.connectRPCPeers = safemap.New[EthAddress, v1connect.CoreServiceClient]()
	require.NoError(t, f.server.refreshConnectRPCPeers(f.ctx, f.server.logger))
	require.False(t, f.server.connectRPCPeers.Has(bad.EthAddress))
	nodes, err := f.q.GetAllRegisteredNodes(f.ctx)
	require.NoError(t, err)
	for _, node := range nodes {
		if node.EthAddress != bad.EthAddress {
			require.True(t, f.server.connectRPCPeers.Has(node.EthAddress))
		}
	}
	// Assert the guard is local peer policy, not an unrequested state deletion.
	_, err = f.q.GetRegisteredNodeByCometAddress(f.ctx, bad.CometAddress)
	require.NoError(t, err)
}
