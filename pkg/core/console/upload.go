package console

import "github.com/labstack/echo/v4"

func (cs *Console) uploadPage(c echo.Context) error {
	if cs.config.Environment == "dev" {
		return cs.views.RenderUploadPageView(c)
	}
	return c.String(200, "not implemented")
}
