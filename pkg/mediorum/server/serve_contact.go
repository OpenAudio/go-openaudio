package server

import (
	"github.com/OpenAudio/go-openaudio/pkg/env"
	"github.com/labstack/echo/v4"
)

type ContactResponse struct {
	Email string `json:"email"`
}

func (ss *MediorumServer) serveContact(c echo.Context) error {
	if ss.trustedNotifier != nil {
		return c.JSON(200, ContactResponse{Email: ss.trustedNotifier.Email})
	}

	email := env.String("OPENAUDIO_NODE_OPERATOR_EMAIL", "nodeOperatorEmailAddress")
	if email == "" {
		return c.String(200, "Email address unavailable at the moment")
	}
	return c.JSON(200, ContactResponse{Email: email})
}
