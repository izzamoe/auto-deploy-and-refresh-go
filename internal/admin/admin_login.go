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
	"github.com/izzamoe/auto-deploy/internal/store"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const (
	jwtCookieName = "admin_token"
	jwtTTL        = 24 * time.Hour
)

// JWTHandler holds the signing secret and provides JWT issue/validate methods.
type JWTHandler struct {
	secret []byte
}

// NewJWTHandler creates a JWTHandler with 32 cryptographically random bytes.
func NewJWTHandler() (*JWTHandler, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return &JWTHandler{secret: b}, nil
}

type jwtClaims struct {
	Sub string `json:"sub"`
	Exp int64  `json:"exp"`
}

func (h *JWTHandler) issueJWT(username string) (string, error) {
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

	mac := hmac.New(sha256.New, h.secret)
	mac.Write([]byte(unsigned))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return unsigned + "." + sig, nil
}

func (h *JWTHandler) validateJWT(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}

	unsigned := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, h.secret)
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

// adminUserAuthenticator verifies admin credentials against the datastore.
// Satisfied by *store.AdminUserStore.
type adminUserAuthenticator interface {
	VerifyPassword(username, password string) (*store.AdminUser, bool, error)
	GetByUsername(username string) (*store.AdminUser, error)
}

// ctxKeyAdminUser holds the authenticated admin username in the request context.
const ctxKeyAdminUser = "adminUsername"

// currentAdminUsername returns the username set by HertzSessionAuthMiddleware.
func currentAdminUsername(c *app.RequestContext) string {
	if v, ok := c.Get(ctxKeyAdminUser); ok {
		if username, ok := v.(string); ok {
			return username
		}
	}
	return ""
}

type LoginHandler struct {
	cfg   *config.ServiceConfig
	jwt   *JWTHandler
	users adminUserAuthenticator
	tmpl  *template.Template
}

func NewLoginHandler(cfg *config.ServiceConfig, jwt *JWTHandler, users adminUserAuthenticator) (*LoginHandler, error) {
	tmpl, err := template.ParseFS(templateFS, "templates/login.html")
	if err != nil {
		return nil, err
	}
	return &LoginHandler{cfg: cfg, jwt: jwt, users: users, tmpl: tmpl}, nil
}

type loginPageData struct {
	ErrorMessage string
	Username     string
}

func (h *LoginHandler) ShowLoginHertz(ctx context.Context, c *app.RequestContext) {
	token := string(c.Cookie(jwtCookieName))
	if _, ok := h.jwt.validateJWT(token); ok {
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

	_, ok, err := h.users.VerifyPassword(username, password)
	if err != nil {
		slog.Error("admin login verify failed", "err", err)
		c.String(consts.StatusInternalServerError, "internal error")
		return
	}
	if !ok {
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

	token, err := h.jwt.issueJWT(username)
	if err != nil {
		slog.Error("jwt issue failed", "err", err)
		c.String(consts.StatusInternalServerError, "internal error")
		return
	}

	c.SetCookie(jwtCookieName, token, int(jwtTTL.Seconds()), "/", "", protocol.CookieSameSiteLaxMode, h.cfg.CookieSecure, true)
	c.Redirect(consts.StatusFound, []byte("/admin/"))
}

func (h *LoginHandler) HandleLogoutHertz(ctx context.Context, c *app.RequestContext) {
	c.SetCookie(jwtCookieName, "", -1, "/", "", protocol.CookieSameSiteLaxMode, h.cfg.CookieSecure, true)
	c.Redirect(consts.StatusFound, []byte("/admin/login"))
}

func HertzSessionAuthMiddleware(jwt *JWTHandler, users adminUserAuthenticator) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		path := string(c.Request.URI().Path())
		isAPIOrEvents := strings.HasPrefix(path, "/admin/api") || strings.HasPrefix(path, "/admin/events")

		username, ok := authenticateAdminRequest(c, jwt, users, isAPIOrEvents)
		if !ok {
			if isAPIOrEvents {
				hertzRequireAuth(c)
			} else {
				c.Redirect(consts.StatusFound, []byte("/admin/login"))
			}
			c.Abort()
			return
		}

		// Force-password-change gate: until the seeded default is replaced, the
		// data/action API is blocked except the account endpoints. The SPA HTML
		// shell is never gated (it decides to show the change-password screen).
		if isAPIOrEvents && !strings.HasPrefix(path, "/admin/api/account") {
			if user, err := users.GetByUsername(username); err == nil && user.MustChangePassword {
				writeAdminAPIErrorHertz(c, consts.StatusForbidden, "Password change required")
				c.Abort()
				return
			}
		}

		c.Set(ctxKeyAdminUser, username)
		c.Next(ctx)
	}
}

// authenticateAdminRequest resolves the caller's admin username from Basic auth
// (API paths) or the session cookie. A Basic header that is present but invalid
// fails without falling back to the cookie.
func authenticateAdminRequest(c *app.RequestContext, jwt *JWTHandler, users adminUserAuthenticator, isAPIOrEvents bool) (string, bool) {
	if isAPIOrEvents {
		authHeader := string(c.GetHeader("Authorization"))
		if strings.HasPrefix(authHeader, "Basic ") {
			username, password, hasAuth := c.Request.BasicAuth()
			if hasAuth {
				if user, ok, err := users.VerifyPassword(username, password); err == nil && ok {
					return user.Username, true
				}
			}
			return "", false
		}
	}

	token := string(c.Cookie(jwtCookieName))
	if sub, ok := jwt.validateJWT(token); ok {
		return sub, true
	}
	return "", false
}

func RegisterLoginRoutesHertz(h *server.Hertz, handler *LoginHandler) {
	h.GET("/admin/login", handler.ShowLoginHertz)
	h.POST("/admin/login", handler.HandleLoginHertz)
	h.GET("/admin/logout", handler.HandleLogoutHertz)
}
