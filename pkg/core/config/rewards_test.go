package config

import (
	"github.com/OpenAudio/go-openaudio/pkg/rewards"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestStaticAudioRewardsOnlyTrending(t *testing.T) {
	for _, authorities := range [][]rewards.ClaimAuthority{DevClaimAuthorities, StageClaimAuthorities, ProdClaimAuthorities} {
		configured := MakeRewards(authorities, nil)
		require.Len(t, configured, 2)
		require.Equal(t, "tt", configured[0].RewardId)
		require.Equal(t, "tut", configured[1].RewardId)
		for _, reward := range configured {
			require.Equal(t, uint64(1000), reward.Amount)
			require.Equal(t, authorities, reward.ClaimAuthorities)
		}
	}
}
