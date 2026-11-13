package rewards_test

import (
	"github.com/OpenAudio/go-openaudio/pkg/rewards"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestTrendingClaimValidation(t *testing.T) {
	attester := rewards.NewRewardAttester(nil, []rewards.Reward{{RewardId: "tt", Amount: 1000}, {RewardId: "tut", Amount: 1000}})
	base := rewards.RewardClaim{RewardID: "tt", Specifier: "2026-11-13:1", Amount: 1000, RecipientEthAddress: "0x1111111111111111111111111111111111111111", ClaimAuthority: "0x2222222222222222222222222222222222222222"}
	for _, id := range []string{"tt", "tut"} {
		for _, tc := range []struct {
			specifier string
			amount    uint64
			ok        bool
		}{
			{"2026-11-13:1", 1000, true}, {"2026-11-13:5", 1000, true}, {"2026-11-13:6", 100, true}, {"2026-11-13:10", 100, true},
			{"2026-11-13:6", 1000, false}, {"2026-11-13:1", 100, false}, {"2026-11-13:1", 23250000, false},
			{"2026-11-13:0", 1000, false}, {"2026-11-13:11", 100, false}, {"2026-11-13:01", 1000, false},
			{"2026-02-30:1", 1000, false}, {"2026-11-13:1_extra", 1000, false}, {"2026-11-13:1:2", 1000, false},
		} {
			t.Run(id+"/"+tc.specifier, func(t *testing.T) {
				claim := base
				claim.RewardID = id
				claim.Specifier = tc.specifier
				claim.Amount = tc.amount
				err := attester.Validate(claim)
				if tc.ok {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			})
		}
	}
	for _, address := range []string{"", "0x11", "0x" + string(make([]byte, 40))} {
		claim := base
		claim.ClaimAuthority = address
		require.Error(t, attester.Validate(claim))
		claim = base
		claim.RecipientEthAddress = address
		require.Error(t, attester.Validate(claim))
	}
}
