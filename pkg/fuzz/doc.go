// Package fuzz provides a lightweight harness for exercising OpenAudio node
// networks with disruptive actions and liveness assertions.
//
// The package intentionally depends only on externally observable node
// behavior and local process management. That keeps it usable against a local
// devnet, staging-like environments, or an already-running validator set
// without importing the core runtime packages.
package fuzz
