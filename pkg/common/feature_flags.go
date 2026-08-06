package common

func IsProgrammableDistributionEnabled(env string) bool {
	switch env {
	case "dev", "development", "devnet", "local", "sandbox", "stage", "staging", "testnet":
		return true
	default:
		return false
	}
}

// IsContentAuthEnabled reports whether a network verifies upload signatures and
// attests content cids on chain.
//
// Separate from IsProgrammableDistributionEnabled even though the lists
// coincide today: that governs DDEX, this governs the ordinary track-upload
// path. Sharing one flag would mean the cid-claim bypass could only be closed
// on networks that also run DDEX.
func IsContentAuthEnabled(env string) bool {
	switch env {
	case "dev", "development", "devnet", "local", "sandbox", "stage", "staging", "testnet":
		return true
	default:
		return false
	}
}
