package server

import (
	"hash/fnv"
	"sort"
	"strings"

	v1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
)

// stringToUint32 generates a deterministic uint32 hash from a string
func stringToUint32(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

// isStringGreater compares two strings based on their deterministic integer values
func isStringGreater(a, b string) bool {
	return stringToUint32(a) > stringToUint32(b)
}

// isCreateAction checks if the manage entity action is a "Create"
func isCreateAction(action string) bool {
	return strings.EqualFold(action, "Create") // Case-insensitive exact match
}

// getTransactionType categorizes the transaction
func getTransactionType(tx *v1.SignedTransaction) string {
	switch {
	case tx.GetManageEntity() != nil:
		return "manage"
	case tx.GetPlays() != nil:
		return "play"
	default:
		return "other"
	}
}

// sortTransactionResponse sorts transactions with a defined priority
func sortTransactionResponse(txs []*v1.Transaction) []*v1.Transaction {
	sort.SliceStable(txs, func(i, j int) bool {
		one, two := txs[i].GetTransaction(), txs[j].GetTransaction()

		oneType, twoType := getTransactionType(one), getTransactionType(two)

		// Prioritize "manage" entities over "plays"
		if oneType != twoType {
			return oneType == "manage"
		}

		// If both are manage entities, prioritize "Create" actions
		if oneType == "manage" && twoType == "manage" {
			oneIsCreate := isCreateAction(one.GetManageEntity().Action)
			twoIsCreate := isCreateAction(two.GetManageEntity().Action)

			if oneIsCreate != twoIsCreate {
				return oneIsCreate
			}
		}

		// Fallback to deterministic signature comparison
		return isStringGreater(one.GetSignature(), two.GetSignature())
	})

	return txs
}
