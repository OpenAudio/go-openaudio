package db

type ValidatorEventType string

const (
	ValidatorEventRegistered   ValidatorEventType = "registered"
	ValidatorEventDeregistered ValidatorEventType = "deregistered"
)
