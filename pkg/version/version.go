package version

import (
	_ "embed"
	"encoding/json"
	"log"
)

//go:embed .version.json
var versionJSON []byte

var Version VersionJson

func init() {
	if err := json.Unmarshal(versionJSON, &Version); err != nil {
		log.Fatalf("unable to parse .version.json file: %v", err)
	}
}

type VersionJson struct {
	Version string `json:"version"`
	Service string `json:"service"`
}
