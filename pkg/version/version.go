package version

import (
	_ "embed"
	"encoding/json"
	"log"
	"os"
)

//go:embed .version.json
var versionJSON []byte

var Version VersionJson

func init() {
	if err := json.Unmarshal(versionJSON, &Version); err != nil {
		log.Fatalf("unable to parse .version.json file: %v", err)
	}
	// Allow override for local devnet testing (e.g. OPENAUDIO_VERSION_OVERRIDE=1.2.1)
	if override := os.Getenv("OPENAUDIO_VERSION_OVERRIDE"); override != "" {
		Version.Version = override
	}
}

type VersionJson struct {
	Version string `json:"version"`
	Service string `json:"service"`
}
