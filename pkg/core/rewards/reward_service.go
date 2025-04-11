package rewards

import (
	"github.com/AudiusProject/audiusd/pkg/core/config"
)

type RewardService struct {
	Config  *config.Config
	Rewards []Reward
}

func NewRewardService(config *config.Config) *RewardService {
	// Create a deep copy of BaseRewards
	rewards := make([]Reward, len(BaseRewards))
	copy(rewards, BaseRewards)

	// Get the appropriate pubkeys and reward extensions based on environment
	var pubkeys []ClaimAuthority
	var extensions []Reward
	switch config.Environment {
	case "dev":
		pubkeys = DevClaimAuthorities
		extensions = DevRewardExtensions
	case "stage":
		pubkeys = StageClaimAuthorities
		extensions = StageRewardExtensions
	case "prod":
		pubkeys = ProdClaimAuthorities
		extensions = ProdRewardExtensions
	}

	// Assign pubkeys to all base rewards
	for i := range rewards {
		rewards[i].ClaimAuthorities = pubkeys
	}

	// Add environment-specific rewards
	if len(extensions) > 0 {
		// Create a copy of extensions to avoid modifying the original
		extendedRewards := make([]Reward, len(extensions))
		copy(extendedRewards, extensions)

		// Assign pubkeys to extended rewards
		for i := range extendedRewards {
			extendedRewards[i].ClaimAuthorities = pubkeys
		}

		// Append extended rewards to base rewards
		rewards = append(rewards, extendedRewards...)
	}

	return &RewardService{
		Config:  config,
		Rewards: rewards,
	}
}
