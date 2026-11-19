package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	"github.com/stretchr/testify/require"
)

func TestProgrammableRewardAttestationDisabled(t *testing.T) {
	// A registered server must reject before consulting its database or key.
	service := &CoreService{core: &Server{}}
	request := connect.NewRequest(&v1.GetRewardAttestationRequest{
		EthRecipientAddress: "0x1111111111111111111111111111111111111111",
		RewardAddress:       "0x2222222222222222222222222222222222222222",
		RewardId:            "fixture-reward", Specifier: "d3", Amount: 1,
		ClaimAuthority: "0x3333333333333333333333333333333333333333",
		Signature:      "rejected before authentication",
	})
	response, err := service.GetRewardAttestation(context.Background(), request)
	require.Nil(t, response)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	require.ErrorContains(t, err, "programmable reward attestations are temporarily disabled")

	// Verify the public Connect route uses the same containment gate.
	path, handler := v1connect.NewCoreServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	httpRequest := httptest.NewRequest(http.MethodPost, v1connect.CoreServiceGetRewardAttestationProcedure, strings.NewReader(`{"rewardId":"fixture-reward","amount":"1"}`))
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Connect-Protocol-Version", "1")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httpRequest)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Contains(t, recorder.Body.String(), "programmable reward attestations are temporarily disabled")
}
