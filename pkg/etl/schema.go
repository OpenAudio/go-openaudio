package etl

import "github.com/OpenAudio/go-openaudio/etl/processors"

var (
	TxTypePlay                        = processors.TxTypePlay
	TxTypeManageEntity                = processors.TxTypeManageEntity
	TxTypeValidatorRegistration       = processors.TxTypeValidatorRegistration
	TxTypeValidatorDeregistration     = processors.TxTypeValidatorDeregistration
	TxTypeValidatorRegistrationLegacy = processors.TxTypeValidatorRegistrationLegacy
	TxTypeSlaRollup                   = processors.TxTypeSlaRollup
	TxTypeValidatorMisbehaviorDereg   = processors.TxTypeValidatorMisbehaviorDereg
	TxTypeStorageProof                = processors.TxTypeStorageProof
	TxTypeStorageProofVerification    = processors.TxTypeStorageProofVerification
	TxTypeRelease                     = processors.TxTypeRelease
)
