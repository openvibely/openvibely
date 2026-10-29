package handler

import (
	"bytes"
	"context"
	"errors"
	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/web/templates/pages"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/static"
)

func (h *Handler) SetOrganizationService(s *service.OrganizationService) { h.organizationSvc = s }

func requireOrgSameOrigin(c echo.Context) error {
	raw := c.Request().Header.Get("Origin")
	if raw == "" {
		raw = c.Request().Header.Get("Referer")
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Host == "" || !strings.EqualFold(u.Host, c.Request().Host) || u.Scheme != c.Scheme() {
		return echo.NewHTTPError(http.StatusForbidden, "same-origin request required")
	}
	return nil
}
func (h *Handler) organizationAuth(c echo.Context, next echo.HandlerFunc) error {
	path := c.Request().URL.Path
	if c.Request().Method == http.MethodPost && (path == "/login" || path == "/invite" || strings.HasPrefix(path, "/organization")) {
		c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 16<<10)
	}
	var principal *models.OrgUser
	if !h.authEnabled() {
		var err error
		principal, err = h.organizationSvc.Store.UserByID(c.Request().Context(), models.BootstrapUserID)
		if err != nil {
			return err
		}
	} else {
		session := h.recognizeSession(c.Request(), time.Now())
		principal = session.orgUser
		if session.localUser != nil {
			c.Set("auth_user", session.localUser)
		} else if session.hostedClaims != nil {
			c.Set("auth_user", session.hostedClaims)
		}
	}
	if principal != nil && !principal.Disabled && principal.Role != "" {
		c.SetRequest(c.Request().WithContext(models.WithPrincipal(c.Request().Context(), principal.Principal)))
		return next(c)
	}
	if h.isAuthPublicPath(c.Request().URL.Path) {
		return next(c)
	}
	loginURL := auth.RedirectURL(authNextURL(c.Request()))
	if h.authMode == auth.AuthModeHostedSSO {
		loginURL = auth.HostedSSOStartURL(authNextURL(c.Request()))
	}
	if c.Request().Header.Get("HX-Request") == "true" {
		c.Response().Header().Set("HX-Redirect", loginURL)
		return c.NoContent(http.StatusUnauthorized)
	}
	return c.Redirect(http.StatusFound, loginURL)
}
func orgAttemptKey(c echo.Context) string {
	// Do not trust client-supplied forwarding headers for the rate-limit key.
	host, _, err := net.SplitHostPort(c.Request().RemoteAddr)
	if err != nil {
		return c.Request().RemoteAddr
	}
	return host
}
func (h *Handler) organizationLogin(c echo.Context) error {
	if err := requireOrgSameOrigin(c); err != nil {
		return err
	}
	if !h.organizationSvc.AllowAttempt(orgAttemptKey(c)) {
		return echo.NewHTTPError(http.StatusTooManyRequests, "too many attempts; try again later")
	}
	token, err := h.organizationSvc.Login(c.Request().Context(), c.FormValue("username"), c.FormValue("password"), h.authCfg.SessionTTL)
	if err != nil {
		if errors.Is(err, service.ErrOrgLogin) {
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid credentials or unavailable account")
		}
		return err
	}
	if old, err := c.Cookie(h.authCfg.CookieName); err == nil {
		if err := h.organizationSvc.Logout(c.Request().Context(), old.Value); err != nil {
			return err
		}
	}
	c.SetCookie(h.authCfg.SessionCookie(token, c.Scheme() == "https"))
	next, err := auth.DecodeNext(c.FormValue("next"))
	if err != nil {
		next = "/"
	}
	return c.Redirect(http.StatusFound, next)
}
func (h *Handler) organizationMe(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	p, ok := models.PrincipalFromContext(c.Request().Context())
	if !ok {
		if !h.authEnabled() {
			u, err := h.organizationSvc.Store.UserByID(c.Request().Context(), models.BootstrapUserID)
			if err != nil {
				return err
			}
			if u != nil {
				p = u.Principal
				ok = true
			}
		} else {
			session := h.recognizeSession(c.Request(), time.Now())
			if session.orgUser != nil {
				p = session.orgUser.Principal
				ok = true
			}
		}
	}
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]any{"authenticated": false})
	}
	return c.JSON(http.StatusOK, map[string]any{"authenticated": h.authEnabled(), "user_id": p.UserID, "org_id": p.OrgID, "role": p.Role, "username": p.Username, "display": p.DisplayName})
}
func (h *Handler) requireOrgAdmin(c echo.Context) (models.Principal, error) {
	if h.organizationSvc == nil {
		return models.Principal{}, echo.NewHTTPError(http.StatusNotFound)
	}
	p, ok := models.PrincipalFromContext(c.Request().Context())
	if !ok || !p.IsOrgAdmin() {
		return p, echo.NewHTTPError(http.StatusForbidden, "organization administrator required")
	}
	if c.Request().Method != http.MethodGet {
		if err := requireOrgSameOrigin(c); err != nil {
			return p, err
		}
	}
	return p, nil
}
func orgActionError(err error) error {
	switch {
	case errors.Is(err, persistence.ErrOrgDenied):
		return echo.NewHTTPError(http.StatusForbidden, err.Error())
	case errors.Is(err, persistence.ErrNotFound):
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	case errors.Is(err, persistence.ErrLastOwner), errors.Is(err, persistence.ErrAccountExists):
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, persistence.ErrInvitationInvalid), errors.Is(err, service.ErrOrgCredentials), errors.Is(err, service.ErrOrgLogin):
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	default:
		return err
	}
}

type organizationPageData struct {
	CSS         string
	Utilities   string
	Org         *models.Organization
	Principal   models.Principal
	Members     []models.OrgUser
	Invitations []models.OrgInvitation
	Admin       bool
	Local       bool
	AuthOff     bool
	InviteURL   string
	Token       string
	Joining     bool
	SignedIn    bool
}

var organizationTemplate = template.Must(template.New("organization").Parse(`{{if .Joining}}<!doctype html>
<html lang="en" data-theme="dark"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Organization - OpenVibely</title><link href="{{.CSS}}" rel="stylesheet"><link href="{{.Utilities}}" rel="stylesheet"></head>
<body class="min-h-screen bg-base-200 p-6"><main class="max-w-3xl mx-auto space-y-6"><a href="/" class="link">Back to OpenVibely</a>{{else}}<div id="organization-container" class="max-w-3xl mx-auto space-y-6"><h1 class="text-2xl font-bold mb-6">Organization</h1>{{end}}
<section class="card bg-base-100 border border-base-300"><div class="card-body"><h1 class="card-title text-2xl">{{if .Joining}}Join {{end}}{{.Org.Name}}</h1>
{{if .Joining}}
<p>All organization members currently have access to every project. Join only an organization you trust.</p>
<form method="post" action="/invite" class="space-y-4"><input type="hidden" name="token" value="{{.Token}}">
{{if .SignedIn}}<p>Join as <strong>{{.Principal.Username}}</strong>.</p>{{else}}
<p>Create an account, or enter your existing account credentials. New passwords must be at least 12 characters.</p>
<label class="form-control">Username<input class="input input-bordered" name="username" autocomplete="username" minlength="3" maxlength="64" required></label>
<label class="form-control">Password<input class="input input-bordered" name="password" type="password" autocomplete="new-password" maxlength="72" required></label>
{{end}}<button class="btn btn-primary" type="submit">Join organization</button></form>
{{else}}
<p>Signed in as {{.Principal.Username}} · {{.Principal.Role}}</p>
<p>All members currently share access to every project. Private projects are not available yet.</p>
{{if .AuthOff}}<p>Authentication is off. Anyone who can reach this app acts as the owner. Enable authentication before inviting people.</p>{{end}}
{{if .Admin}}<form method="post" action="/organization" hx-post="/organization" hx-target="#main-content" class="flex gap-2"><input aria-label="Organization name" class="input input-bordered flex-1" name="name" value="{{.Org.Name}}" maxlength="100" required><button class="btn" type="submit">Rename</button></form>{{end}}
</div></section>
<section class="card bg-base-100 border border-base-300"><div class="card-body"><h2 class="card-title">Members</h2>
{{range .Members}}<div class="flex flex-wrap items-center gap-2 py-2 border-b border-base-300"><span class="flex-1">{{.Username}} · {{.Role}}</span>
{{if and $.Admin $.Local (or (eq $.Principal.Role "owner") (eq .Role "member"))}}<form method="post" action="/organization/members/{{.UserID}}" hx-post="/organization/members/{{.UserID}}" hx-target="#main-content" class="flex gap-2"><select aria-label="New role for {{.Username}}" name="role" class="select select-bordered"><option value="member" {{if eq .Role "member"}}selected{{end}}>Member</option>{{if eq $.Principal.Role "owner"}}<option value="admin" {{if eq .Role "admin"}}selected{{end}}>Admin</option><option value="owner" {{if eq .Role "owner"}}selected{{end}}>Owner</option>{{end}}</select><button class="btn" name="action" value="role">Save role</button><button class="btn btn-outline" name="action" value="remove">Remove</button></form>{{end}}</div>{{end}}
</div></section>
{{if .Admin}}<section class="card bg-base-100 border border-base-300"><div class="card-body"><h2 class="card-title">Invitations</h2>
{{if .Local}}<p>Links expire in seven days and work once. Anyone with the link can join.</p><form method="post" action="/organization/invitations" hx-post="/organization/invitations" hx-target="#main-content" class="flex gap-2"><select name="role" aria-label="Invitation role" class="select select-bordered"><option value="member">Member</option>{{if eq .Principal.Role "owner"}}<option value="admin">Admin</option>{{end}}</select><button class="btn btn-primary" type="submit">Create invite link</button></form>
{{if .InviteURL}}<label class="form-control">Share this link (shown only once)<input class="input input-bordered" readonly value="{{.InviteURL}}"></label>{{end}}
{{range .Invitations}}<div class="flex flex-wrap items-center gap-2 py-2"><span class="flex-1">{{.Role}} · {{if .Used}}Accepted{{else if .Revoked}}Revoked{{else}}Pending{{end}}</span>{{if and (not .Used) (not .Revoked)}}<form method="post" action="/organization/invitations/{{.ID}}/revoke" hx-post="/organization/invitations/{{.ID}}/revoke" hx-target="#main-content"><button class="btn btn-sm" type="submit">Revoke</button></form>{{end}}</div>{{end}}
{{else}}<p>{{if .AuthOff}}Enable local authentication to create invitations.{{else}}Hosted invitations are managed through workspace settings when hosted teams support is available.{{end}}</p>{{end}}
</div></section>{{end}}{{end}}
{{if .Joining}}</main></body></html>{{else}}</div>{{end}}`))

func (h *Handler) renderOrganization(c echo.Context, data organizationPageData) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	c.Response().Header().Set("Referrer-Policy", "no-referrer")
	data.CSS = static.URL("app.css")
	data.Utilities = static.URL("app-utilities.css")
	var b bytes.Buffer
	if err := organizationTemplate.Execute(&b, data); err != nil {
		return err
	}
	if data.Joining {
		return c.HTML(http.StatusOK, b.String())
	}
	content := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		_, err := io.WriteString(w, b.String())
		return err
	})
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.OrganizationContent(content))
	}
	currentProjectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	projects, err := h.projectSvc.List(c.Request().Context())
	if err != nil {
		return err
	}
	return render(c, http.StatusOK, pages.Organization(projects, currentProjectID, content))
}
func (h *Handler) organizationData(c echo.Context) (organizationPageData, error) {
	if h.organizationSvc == nil {
		return organizationPageData{}, echo.NewHTTPError(http.StatusNotFound)
	}
	p, ok := models.PrincipalFromContext(c.Request().Context())
	if !ok {
		return organizationPageData{}, echo.NewHTTPError(http.StatusUnauthorized)
	}
	org, err := h.organizationSvc.Store.Organization(c.Request().Context())
	if err != nil {
		return organizationPageData{}, err
	}
	members, err := h.organizationSvc.Store.Members(c.Request().Context())
	if err != nil {
		return organizationPageData{}, err
	}
	data := organizationPageData{Org: org, Principal: p, Members: members, Admin: p.IsOrgAdmin(), Local: h.authMode == auth.AuthModeLocal, AuthOff: !h.authEnabled()}
	if data.Admin {
		data.Invitations, err = h.organizationSvc.Store.Invitations(c.Request().Context())
	}
	return data, err
}
func (h *Handler) OrganizationPage(c echo.Context) error {
	data, err := h.organizationData(c)
	if err != nil {
		return err
	}
	return h.renderOrganization(c, data)
}
func (h *Handler) RenameOrganization(c echo.Context) error {
	p, err := h.requireOrgAdmin(c)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" || len(name) > 100 {
		return echo.NewHTTPError(http.StatusBadRequest, "organization name must be 1–100 bytes")
	}
	if err := h.organizationSvc.Store.RenameOrganization(c.Request().Context(), p.UserID, name); err != nil {
		return orgActionError(err)
	}
	return c.Redirect(http.StatusSeeOther, "/organization")
}
func (h *Handler) CreateOrgInvitation(c echo.Context) error {
	p, err := h.requireOrgAdmin(c)
	if err != nil {
		return err
	}
	if h.authMode != auth.AuthModeLocal {
		return echo.NewHTTPError(http.StatusForbidden, "local authentication is required for invitations")
	}
	token, err := h.organizationSvc.Invite(c.Request().Context(), p.UserID, c.FormValue("role"))
	if err != nil {
		return orgActionError(err)
	}
	data, err := h.organizationData(c)
	if err != nil {
		return err
	}
	base := strings.TrimRight(h.appBaseURL, "/")
	if base == "" {
		base = c.Scheme() + "://" + c.Request().Host
	}
	data.InviteURL = base + "/invite?token=" + url.QueryEscape(token)
	return h.renderOrganization(c, data)
}
func (h *Handler) RevokeOrgInvitation(c echo.Context) error {
	p, err := h.requireOrgAdmin(c)
	if err != nil {
		return err
	}
	if err := h.organizationSvc.Store.RevokeInvitation(c.Request().Context(), p.UserID, c.Param("id")); err != nil {
		return orgActionError(err)
	}
	return c.Redirect(http.StatusSeeOther, "/organization")
}
func (h *Handler) ChangeOrgMember(c echo.Context) error {
	p, err := h.requireOrgAdmin(c)
	if err != nil {
		return err
	}
	if h.authMode != auth.AuthModeLocal {
		return echo.NewHTTPError(http.StatusForbidden, "local authentication is required for member changes")
	}
	remove := c.FormValue("action") == "remove"
	if err := h.organizationSvc.Store.ChangeMember(c.Request().Context(), p.UserID, c.Param("id"), c.FormValue("role"), remove); err != nil {
		return orgActionError(err)
	}
	h.organizationSvc.Notify()
	return c.Redirect(http.StatusSeeOther, "/organization")
}
func (h *Handler) OrgInvitePage(c echo.Context) error {
	if h.organizationSvc == nil || h.authMode != auth.AuthModeLocal {
		return echo.NewHTTPError(http.StatusForbidden, "local authentication is required to join")
	}
	org, err := h.organizationSvc.Store.Organization(c.Request().Context())
	if err != nil {
		return err
	}
	p, ok := models.PrincipalFromContext(c.Request().Context())
	return h.renderOrganization(c, organizationPageData{Org: org, Principal: p, SignedIn: ok, Joining: true, Token: c.QueryParam("token")})
}
func (h *Handler) AcceptOrgInvitation(c echo.Context) error {
	if h.organizationSvc == nil || h.authMode != auth.AuthModeLocal {
		return echo.NewHTTPError(http.StatusForbidden, "local authentication is required to join")
	}
	if err := requireOrgSameOrigin(c); err != nil {
		return err
	}
	if !h.organizationSvc.AllowAttempt(orgAttemptKey(c)) {
		return echo.NewHTTPError(http.StatusTooManyRequests, "too many attempts; try again later")
	}
	existing := ""
	if p, ok := models.PrincipalFromContext(c.Request().Context()); ok {
		existing = p.UserID
	}
	user, err := h.organizationSvc.Accept(c.Request().Context(), c.FormValue("token"), c.FormValue("username"), c.FormValue("password"), existing)
	if err != nil {
		return orgActionError(err)
	}
	token, err := h.organizationSvc.NewSession(c.Request().Context(), user.UserID, h.authCfg.SessionTTL)
	if err != nil {
		return err
	}
	if old, err := c.Cookie(h.authCfg.CookieName); err == nil {
		if err := h.organizationSvc.Logout(c.Request().Context(), old.Value); err != nil {
			return err
		}
	}
	c.SetCookie(h.authCfg.SessionCookie(token, c.Scheme() == "https"))
	return c.Redirect(http.StatusSeeOther, "/organization")
}
