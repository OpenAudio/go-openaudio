package config

import "github.com/OpenAudio/go-openaudio/pkg/rewards"

// Solana reward manager pubkeys for the AUDIO mint, per environment.
// Used as a denylist by validateCreateRewardPool: pools cannot be
// created for the AUDIO RM, so AUDIO sender attestations stay on the
// network-wide validator/AAO trust set rather than shifting to pool-
// controlled authorities. Empty values disable the denylist for that
// environment.
var (
	DevAudioRewardsManagerPubkey  = "DJPzVothq58SmkpRb1ATn5ddN2Rpv1j2TcGvM3XsHf1c"
	ProdAudioRewardsManagerPubkey = "71hWFVYokLaN1PNYzTAWi13EfJ7Xt9VbSWUKsXUT8mxE"
)

// AudioRewardsManagerPubkey returns the configured AUDIO RM pubkey for
// the current runtime environment, or "" if none is configured. Callers
// MUST treat "" as "no denylist enforcement."
func AudioRewardsManagerPubkey() string {
	switch GetRuntimeEnvironment() {
	case "prod", "production", "mainnet":
		return ProdAudioRewardsManagerPubkey
	case "dev", "development", "devnet", "local", "sandbox":
		return DevAudioRewardsManagerPubkey
	default:
		return ""
	}
}

var (
	DevClaimAuthorities = []rewards.ClaimAuthority{
		{
			Address: "0xfc3916B97489d2eFD81DDFDf11bad8E33ad5b87a",
			Name:    "Audius",
		},
	}
	StageClaimAuthorities = []rewards.ClaimAuthority{
		{
			Address: "0xDC2BDF1F23381CA2eC9e9c70D4FD96CD8645D090",
			Name:    "Audius",
		},
	}
	ProdClaimAuthorities = []rewards.ClaimAuthority{
		{
			Address: "0xc8d0C29B6d540295e8fc8ac72456F2f4D41088c8",
			Name:    "Audius",
		},
	}
)

var (
	// BaseRewards is the static AUDIO payout allowlist. Trending payouts use
	// the existing AAO and validator attestation path; user challenges are retired.
	BaseRewards = []rewards.Reward{
		{Amount: 1000, RewardId: "tt", Name: "trending tracks"},
		{Amount: 1000, RewardId: "tut", Name: "trending underground"},
	}

	// Environment-specific reward extensions
	DevRewardExtensions = []rewards.Reward{
		// Add dev-specific rewards here
		// Example:
		// {
		//     Amount:   10,
		//     RewardId: "test",
		//     Name:     "test reward",
		// },
	}

	StageRewardExtensions = []rewards.Reward{
		// Add stage-specific rewards here
	}

	ProdRewardExtensions = []rewards.Reward{
		// Add prod-specific rewards here
	}
)

func MakeRewards(claimAuthorities []rewards.ClaimAuthority, rewardExtensions []rewards.Reward) []rewards.Reward {
	// Create a deep 	 of BaseRewards
	rewardsList := make([]rewards.Reward, len(BaseRewards))
	copy(rewardsList, BaseRewards)

	// Assign pubkeys to all base rewards
	for i := range rewardsList {
		rewardsList[i].ClaimAuthorities = claimAuthorities
	}

	// Add environment-specific rewards
	if len(rewardExtensions) > 0 {
		// Create a copy of extensions to avoid modifying the original
		extendedRewards := make([]rewards.Reward, len(rewardExtensions))
		copy(extendedRewards, rewardExtensions)

		// Assign pubkeys to extended rewards
		for i := range extendedRewards {
			extendedRewards[i].ClaimAuthorities = claimAuthorities
		}

		// Append extended rewards to base rewards
		rewardsList = append(rewardsList, extendedRewards...)
	}
	return rewardsList
}
