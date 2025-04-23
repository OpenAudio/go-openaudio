package rewards

type ClaimAuthority struct {
	Address string `json:"address"`
	Name    string `json:"name"`
}

type Reward struct {
	ClaimAuthorities []ClaimAuthority `json:"claim_authorities"`
	Amount           uint64           `json:"amount"`
	RewardId         string           `json:"reward_id"`
	Name             string           `json:"name"`
}
