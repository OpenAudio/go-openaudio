package console

import (
	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/storage/v1"
	"github.com/labstack/echo/v4"
)

func (cs *Console) storagePage(c echo.Context) error {
	var diag *v1.GetStorageDiagnosticsResponse
	if cs.storage != nil {
		res, err := cs.storage.GetStorageDiagnostics(c.Request().Context(), connect.NewRequest(&v1.GetStorageDiagnosticsRequest{}))
		if err != nil {
			cs.logger.Warn("storage diagnostics unavailable: " + err.Error())
		} else {
			diag = res.Msg
		}
	}
	return cs.views.RenderStorageView(c, diag)
}
