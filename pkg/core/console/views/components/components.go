package components

import "github.com/AudiusProject/audiusd/pkg/core/config"

type Components struct {
	config  *config.Config
	baseUrl string
}

func NewComponents(config *config.Config, baseUrl string) *Components {
	return &Components{
		config:  config,
		baseUrl: baseUrl,
	}
}
