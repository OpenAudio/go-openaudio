package config

import "github.com/AudiusProject/audiusd/pkg/rewards"

var (
	DevClaimAuthorities   = []rewards.ClaimAuthority{{Address: "0x73EB6d82CFB20bA669e9c178b718d770C49BB52f", Name: "TikiLabsDiscovery"}, {Address: "0xfc3916B97489d2eFD81DDFDf11bad8E33ad5b87a", Name: "TikiLabsBridge"}}
	StageClaimAuthorities = []rewards.ClaimAuthority{{Address: "0x8fcFA10Bd3808570987dbb5B1EF4AB74400FbfDA", Name: "TikiLabsDiscovery"}, {Address: "0x788aab45F3D4b7e44dBE71c688589942a9261651", Name: "TikiLabsBridge"}}
	ProdClaimAuthorities  = []rewards.ClaimAuthority{{Address: "0xf1a1Bd34b2Bc73629aa69E50E3249E89A3c16786", Name: "TikiLabsDiscovery"}, {Address: "0x66C72FC7D7b36c7691ed72CA243dd427880C8ec8", Name: "TikiLabsBridge"}}
)

var (
	// BaseRewards contains all rewards that are common across all environments
	BaseRewards = []rewards.Reward{
		{
			Amount:   1,
			RewardId: "p",
			Name:     "profile completion",
		},
		{
			Amount:   1,
			RewardId: "e",
			Name:     "endless listen streak",
		},
		{
			Amount:   1,
			RewardId: "u",
			Name:     "upload tracks",
		},
		{
			Amount:   1,
			RewardId: "r",
			Name:     "referrals",
		},
		{
			Amount:   1,
			RewardId: "rv",
			Name:     "referrals verified",
		},
		{
			Amount:   1,
			RewardId: "rd",
			Name:     "referred",
		},
		{
			Amount:   5,
			RewardId: "v",
			Name:     "verified",
		},
		{
			Amount:   1,
			RewardId: "m",
			Name:     "mobile install",
		},
		{
			Amount:   1000,
			RewardId: "tt",
			Name:     "trending tracks",
		},
		{
			Amount:   1000,
			RewardId: "tut",
			Name:     "trending underground",
		},
		{
			Amount:   100,
			RewardId: "tp",
			Name:     "trending playlist",
		},
		{
			Amount:   2,
			RewardId: "ft",
			Name:     "first tip",
		},
		{
			Amount:   2,
			RewardId: "fp",
			Name:     "first playlist",
		},
		{
			Amount:   5,
			RewardId: "b",
			Name:     "audio match buyer",
		},
		{
			Amount:   5,
			RewardId: "s",
			Name:     "audio match seller",
		},
		{
			Amount:   1,
			RewardId: "o",
			Name:     "airdrop 2",
		},
		{
			Amount:   1,
			RewardId: "c",
			Name:     "first weekly comment",
		},
		{
			Amount:   25,
			RewardId: "p1",
			Name:     "play count milestone",
		},
		{
			Amount:   100,
			RewardId: "p2",
			Name:     "play count milestone",
		},
		{
			Amount:   1000,
			RewardId: "p3",
			Name:     "play count milestone",
		},
		{
			Amount:   100,
			RewardId: "t",
			Name:     "tastemaker",
		},
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
