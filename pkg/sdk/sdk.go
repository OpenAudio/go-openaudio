package sdk

import "github.com/AudiusProject/audiusd/pkg/core/sdk"

type AudiusdSDK struct {
	core    *sdk.Sdk
	storage *StorageSDK
}

func NewAudiusdSDK(core *sdk.Sdk, storage *StorageSDK) *AudiusdSDK {
	return &AudiusdSDK{
		core:    core,
		storage: storage,
	}
}

func (s *AudiusdSDK) Core() *sdk.Sdk {
	return s.core
}

func (s *AudiusdSDK) Storage() *StorageSDK {
	return s.storage
}
