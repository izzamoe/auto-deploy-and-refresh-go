package admin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"log/slog"
	"strings"
	"time"

	"github.com/izzamoe/auto-deploy/internal/config"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const (
	jwtCookieName = "admin_token"
	jwtTTL        = 24 * time.Hour
)

var jwtSecret []byte

func InitJWTSecret() error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	jwtSecret = b
	return nil
}

type jwtClaims struct {
	Sub string `json:"sub"`
	Exp int64  `json:"exp"`
}

func issueJWT(username string) (string, error) {
	claims := jwtClaims{
		Sub: username,
		Exp: time.Now().Add(jwtTTL).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString(payload)
	unsigned := header + "." + body

	mac := hmac.New(sha256.New, jwtSecret)
	mac.Write([]byte(unsigned))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return unsigned + "." + sig, nil
}

func validateJWT(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}

	unsigned := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, jwtSecret)
	mac.Write([]byte(unsigned))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	if subtle.ConstantTimeCompare([]byte(parts[2]), []byte(expectedSig)) != 1 {
		return "", false
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}

	var claims jwtClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return "", false
	}

	if time.Now().Unix() > claims.Exp {
		return "", false
	}

	return claims.Sub, true
}

type LoginHandler struct {
	cfg  *config.ServiceConfig
	tmpl *template.Template
}

func NewLoginHandler(cfg *config.ServiceConfig) (*LoginHandler, error) {
	tmpl, err := template.ParseFS(templateFS, "templates/login.html")
	if err != nil {
		return nil, err
	}
	return &LoginHandler{cfg: cfg, tmpl: tmpl}, nil
}

type loginPageData struct {
	ErrorMessage string
	Username     string
}

func (h *LoginHandler) ShowLoginHertz(ctx context.Context, c *app.RequestContext) {
	token := string(c.Cookie(jwtCookieName))
	if _, ok := validateJWT(token); ok {
		c.Redirect(consts.StatusFound, []byte("/admin/"))
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.SetStatusCode(consts.StatusOK)
	if err := h.tmpl.Execute(c.Response.BodyWriter(), loginPageData{}); err != nil {
		slog.Error("login template render failed", "err", err)
	}
}

func (h *LoginHandler) HandleLoginHertz(ctx context.Context, c *app.RequestContext) {
	username := strings.TrimSpace(string(c.FormValue("username")))
	password := string(c.FormValue("password"))

	uOK := subtle.ConstantTimeCompare([]byte(username), []byte(h.cfg.AdminUsername)) == 1
	pOK := subtle.ConstantTimeCompare([]byte(password), []byte(h.cfg.AdminPassword)) == 1

	if !uOK || !pOK {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.SetStatusCode(consts.StatusUnauthorized)
		if err := h.tmpl.Execute(c.Response.BodyWriter(), loginPageData{
			ErrorMessage: "Invalid username or password.",
			Username:     username,
		}); err != nil {
			slog.Error("login template render failed", "err", err)
		}
		return
	}

	token, err := issueJWT(username)
	if err != nil {
		slog.Error("jwt issue failed", "err", err)
		c.String(consts.StatusInternalServerError, "internal error")
		return
	}

	c.SetCookie(jwtCookieName, token, int(jwtTTL.Seconds()), "/", "", protocol.CookieSameSiteLaxMode, false, true)
	c.Redirect(consts.StatusFound, []byte("/admin/"))
}

func (h *LoginHandler) HandleLogoutHertz(ctx context.Context, c *app.RequestContext) {
	c.SetCookie(jwtCookieName, "", -1, "/", "", protocol.CookieSameSiteLaxMode, false, true)
	c.Redirect(consts.StatusFound, []byte("/admin/login"))
}

func HertzSessionAuthMiddleware(cfg *config.ServiceConfig) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		path := string(c.Request.URI().Path())
		isAPIOrEvents := strings.HasPrefix(path, "/admin/api") || strings.HasPrefix(path, "/admin/events")

		if isAPIOrEvents {
			authHeader := string(c.GetHeader("Authorization"))
			if strings.HasPrefix(authHeader, "Basic ") {
				username, password, ok := c.Request.BasicAuth()
				if ok {
					uOK := subtle.ConstantTimeCompare([]byte(username), []byte(cfg.AdminUsername)) == 1
					pOK := subtle.ConstantTimeCompare([]byte(password), []byte(cfg.AdminPassword)) == 1
					if uOK && pOK {
						c.Next(ctx)
						return
					}
				}
				hertzRequireAuth(c)
				c.Abort()
				return
			}
		}

		token := string(c.Cookie(jwtCookieName))
		if _, ok := validateJWT(token); ok {
			c.Next(ctx)
			return
		}

		if isAPIOrEvents {
			hertzRequireAuth(c)
			c.Abort()
			return
		}

		c.Redirect(consts.StatusFound, []byte("/admin/login"))
		c.Abort()
	}
}

func RegisterLoginRoutesHertz(h *server.Hertz, handler *LoginHandler) {
	h.GET("/admin/login", handler.ShowLoginHertz)
	h.POST("/admin/login", handler.HandleLoginHertz)
	h.GET("/admin/logout", handler.HandleLogoutHertz)
}
