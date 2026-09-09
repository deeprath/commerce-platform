package api

import (
	"github.com/labstack/echo/v4"

	"github.com/deeprath/commerce-platform/pkg/errs"
)

type loginBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) login(c echo.Context) error {
	var in loginBody
	if err := c.Bind(&in); err != nil || in.Username == "" || in.Password == "" {
		return c.JSON(400, errs.HTTPError{Status: 400, Code: "INVALID_ARGUMENT", Reason: "USERNAME_PASSWORD_REQUIRED"})
	}
	tok, err := s.broker.Login(c.Request().Context(), in.Username, in.Password)
	if err != nil {
		return fail(c, errs.ToStatus(err).Err())
	}
	s.broker.SetCookies(c.Response().Writer, tok)
	return c.JSON(200, map[string]any{"authenticated": true, "expires_in": tok.ExpiresIn})
}

func (s *Server) logout(c echo.Context) error {
	s.broker.ClearCookies(c.Response().Writer)
	return c.NoContent(204)
}
