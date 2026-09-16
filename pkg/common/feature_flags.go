package common

func IsProgrammableDistributionEnabled(env string) bool {
	switch env {
	case "dev", "development", "devnet", "local", "sandbox", "stage", "staging", "testnet":
		return true
	default:
		return false
	}
}
