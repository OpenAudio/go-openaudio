package sandbox

import (
	"embed"
	"html/template"
	"net/http"

	"github.com/AudiusProject/audiusd/pkg/core/config"
)

//go:embed editor.html
var EditorAssets embed.FS

type PageData struct {
	Environment string
	RPCUrl      string
	ChainID     uint
}

func ServeSandbox(cfg *config.Config, w http.ResponseWriter, r *http.Request) {
	sandboxVars := cfg.NewSandboxVars()

	tmpl := template.Must(template.ParseFS(EditorAssets, "editor.html"))

	data := PageData{
		Environment: sandboxVars.SdkEnvironment,
		RPCUrl:      sandboxVars.EthRpcURL,
		ChainID:     uint(sandboxVars.EthChainID),
	}

	// Execute directly (no named template)
	err := tmpl.Execute(w, data)
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}
