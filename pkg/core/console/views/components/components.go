package components

import "github.com/OpenAudio/go-openaudio/pkg/core/config"

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
