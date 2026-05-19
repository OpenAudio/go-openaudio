// Package env provides environment variable helpers that support
// OPENAUDIO_-prefixed canonical names with fallback to legacy names.
//
// Convention: always pass the OPENAUDIO_ key first, then any legacy key(s).
// The first key that is set wins; if none are set, the default is returned.
package env

import (
	"os"
	"strconv"
	"time"
)

// Get returns the value of the first set environment variable from keys,
// or defaultValue if none are set.
func Get(defaultValue string, keys ...string) string {
	for _, key := range keys {
		if val, ok := os.LookupEnv(key); ok {
			return val
		}
	}
	return defaultValue
}

// String returns the value of the first set environment variable from keys,
// or "" if none are set.
func String(keys ...string) string {
	return Get("", keys...)
}

// Bool returns true if the first set environment variable equals "true".
func Bool(keys ...string) bool {
	return String(keys...) == "true"
}

// GetInt returns the integer value of the first set environment variable.
// If the value cannot be parsed, defaultValue is returned.
func GetInt(defaultValue int, keys ...string) int {
	for _, key := range keys {
		if val, ok := os.LookupEnv(key); ok {
			if i, err := strconv.Atoi(val); err == nil {
				return i
			}
			return defaultValue
		}
	}
	return defaultValue
}

// GetDuration returns the duration value of the first set environment variable.
// If the value cannot be parsed, defaultValue is returned.
func GetDuration(defaultValue time.Duration, keys ...string) time.Duration {
	for _, key := range keys {
		if val, ok := os.LookupEnv(key); ok {
			if d, err := time.ParseDuration(val); err == nil {
				return d
			}
			return defaultValue
		}
	}
	return defaultValue
}

// Lookup returns the value of the first set environment variable.
// Returns ("", false) if none of the keys are set.
func Lookup(keys ...string) (string, bool) {
	for _, key := range keys {
		if val, ok := os.LookupEnv(key); ok {
			return val, true
		}
	}
	return "", false
}

// IsSet returns true if any of the given keys is set.
func IsSet(keys ...string) bool {
	_, ok := Lookup(keys...)
	return ok
}
