package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"golang.org/x/crypto/bcrypt"
)

var ErrOrgLogin = errors.New("invalid credentials or unavailable account")
var ErrOrgRateLimited = errors.New("too many attempts; try again later")
var ErrOrgCredentials = errors.New("check your account details")
var orgUsername = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,64}$`)

type orgLoginWindow struct {
	Start time.Time
	Count int
}
type OrganizationService struct {
	Store          persistence.OrganizationStore
	secret         string
	dummyHash      []byte
	mu             sync.Mutex
	changes        chan struct{}
	attempts       map[string]orgLoginWindow
	trustedProxies []netip.Prefix
}

func NewOrganizationService(store persistence.OrganizationStore, secret string) *OrganizationService {
	dummy, _ := bcrypt.GenerateFromPassword([]byte("invalid-account-comparison"), bcrypt.DefaultCost)
	return &OrganizationService{Store: store, secret: secret, dummyHash: dummy, changes: make(chan struct{}), attempts: map[string]orgLoginWindow{}}
}
func (s *OrganizationService) Bootstrap(ctx context.Context, username, password string, authEnabled bool) error {
	if authEnabled && username != "" {
		user, err := s.Store.UserByID(ctx, models.BootstrapUserID)
		if err != nil {
			return err
		}
		if user == nil {
			return persistence.ErrNotFound
		}
		if user.PasswordHash == "" {
			hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			if err := s.Store.Bootstrap(ctx, username, string(hash)); err != nil {
				return err
			}
		}
	}
	if !authEnabled {
		members, err := s.Store.Members(ctx)
		if err != nil {
			return err
		}
		if len(members) != 1 || members[0].UserID != models.BootstrapUserID || members[0].Role != "owner" || members[0].Disabled {
			return errors.New("authentication cannot be disabled while the organization has multiple members or a different owner")
		}
	}
	return nil
}
func orgToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (s *OrganizationService) tokenHash(token string) string {
	sum := sha256.Sum256([]byte(s.secret + "\x00" + token))
	return hex.EncodeToString(sum[:])
}

// ConfigureTrustedProxies is called once at startup. Only these peers may
// supply X-Forwarded-For; walk the chain from the server toward the client.
func (s *OrganizationService) ConfigureTrustedProxies(raw string) error {
	var prefixes []netip.Prefix
	if strings.TrimSpace(raw) != "" {
		for _, entry := range strings.Split(raw, ",") {
			entry = strings.TrimSpace(entry)
			prefix, err := netip.ParsePrefix(entry)
			if err != nil {
				addr, addrErr := netip.ParseAddr(entry)
				if addrErr != nil {
					return fmt.Errorf("invalid AUTH_TRUSTED_PROXIES entry %q", entry)
				}
				prefix = netip.PrefixFrom(addr, addr.BitLen())
			}
			prefixes = append(prefixes, prefix)
		}
	}
	s.trustedProxies = prefixes
	return nil
}
func (s *OrganizationService) trustedProxy(addr netip.Addr) bool {
	for _, prefix := range s.trustedProxies {
		if prefix.Contains(addr.Unmap()) {
			return true
		}
	}
	return false
}
func (s *OrganizationService) ClientIP(remote, forwarded string) string {
	peer, err := netip.ParseAddrPort(remote)
	var addr netip.Addr
	if err == nil {
		addr = peer.Addr().Unmap()
	} else {
		addr, err = netip.ParseAddr(remote)
		if err != nil {
			return remote
		}
		addr = addr.Unmap()
	}
	fallback := addr.String()
	if !s.trustedProxy(addr) || forwarded == "" {
		return fallback
	}
	chain := strings.Split(forwarded, ",")
	for i := len(chain) - 1; i >= 0 && s.trustedProxy(addr); i-- {
		next, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
		if err != nil {
			return fallback
		}
		addr = next.Unmap()
	}
	return addr.String()
}

// Reserve before password hashing so concurrent attempts cannot bypass limits.
// Successful authentication releases its reservation; failures last 15 minutes.
func (s *OrganizationService) BeginLogin(ip, username string) (func(bool), bool) {
	username = strings.ToLower(strings.TrimSpace(username))
	sum := sha256.Sum256([]byte(username))
	account := hex.EncodeToString(sum[:])
	keys := []string{"login-pair:" + ip + ":" + account, "login-account:" + account, "login-ip:" + ip}
	starts, ok := s.reserveAttempts(keys, []int{10, 100, 100})
	if !ok {
		return nil, false
	}
	var once sync.Once
	return func(failed bool) {
		once.Do(func() {
			if failed {
				return
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			for i, key := range keys {
				v, exists := s.attempts[key]
				if exists && v.Start.Equal(starts[i]) {
					v.Count--
					if v.Count == 0 {
						delete(s.attempts, key)
					} else {
						s.attempts[key] = v
					}
				}
			}
		})
	}, true
}
func (s *OrganizationService) AllowSignup(ip string) bool {
	_, ok := s.reserveAttempts([]string{"signup:" + ip}, []int{30})
	return ok
}
func (s *OrganizationService) reserveAttempts(keys []string, limits []int) ([]time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.attempts {
		if now.Sub(v.Start) >= 15*time.Minute {
			delete(s.attempts, k)
		}
	}
	missing := 0
	for i, key := range keys {
		v, ok := s.attempts[key]
		if !ok {
			missing++
		}
		if v.Count >= limits[i] {
			return nil, false
		}
	}
	if len(s.attempts)+missing > 16384 {
		return nil, false
	}
	starts := make([]time.Time, len(keys))
	for i, key := range keys {
		v, ok := s.attempts[key]
		if !ok {
			v.Start = now
		}
		v.Count++
		s.attempts[key] = v
		starts[i] = v.Start
	}
	return starts, true
}
func (s *OrganizationService) VerifyCredentials(ctx context.Context, username, password string) (*models.OrgUser, error) {
	user, err := s.Store.UserByUsername(ctx, strings.TrimSpace(username))
	if err != nil {
		return nil, err
	}
	hash := s.dummyHash
	if user != nil && user.PasswordHash != "" {
		hash = []byte(user.PasswordHash)
	}
	match := bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
	if !match || user == nil || user.Disabled {
		return nil, ErrOrgLogin
	}
	return user, nil
}
func (s *OrganizationService) Login(ctx context.Context, username, password string, ttl time.Duration) (string, error) {
	user, err := s.VerifyCredentials(ctx, username, password)
	if err != nil {
		return "", err
	}
	if user.Role == "" {
		return "", ErrOrgLogin
	}
	return s.NewSession(ctx, user.UserID, ttl)
}
func (s *OrganizationService) NewSession(ctx context.Context, id string, ttl time.Duration) (string, error) {
	token, err := orgToken()
	if err != nil {
		return "", err
	}
	err = s.Store.CreateSession(ctx, s.tokenHash(token), id, time.Now().Add(ttl).Unix())
	return token, err
}
func (s *OrganizationService) Session(ctx context.Context, token string) (*models.OrgUser, error) {
	if len(token) != 43 {
		return nil, nil
	}
	return s.Store.SessionUser(ctx, s.tokenHash(token), time.Now().Unix())
}
func (s *OrganizationService) Logout(ctx context.Context, token string) error {
	err := s.Store.RevokeSession(ctx, s.tokenHash(token))
	if err == nil {
		s.Notify()
	}
	return err
}
func (s *OrganizationService) Invite(ctx context.Context, actor, role string) (string, error) {
	token, err := orgToken()
	if err != nil {
		return "", err
	}
	id, err := orgToken()
	if err != nil {
		return "", err
	}
	err = s.Store.CreateInvitation(ctx, actor, id, s.tokenHash(token), role, time.Now().Add(7*24*time.Hour).Unix())
	return token, err
}

// Accept joins an existing password-verified account or creates one. The store
// atomically consumes the invitation with membership creation, preventing reuse.
func (s *OrganizationService) Accept(ctx context.Context, token, username, password, existingID string) (*models.OrgUser, error) {
	if len(token) != 43 {
		return nil, persistence.ErrInvitationInvalid
	}
	user := models.OrgUser{}
	if existingID == "" {
		username = strings.TrimSpace(username)
		if username == "" {
			return nil, fmt.Errorf("%w: enter a username", ErrOrgCredentials)
		}
		if len(password) > 72 {
			return nil, fmt.Errorf("%w: this password is too long; try a shorter password", ErrOrgCredentials)
		}
		existing, err := s.Store.UserByUsername(ctx, username)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			verified, err := s.VerifyCredentials(ctx, username, password)
			if err != nil {
				return nil, ErrOrgLogin
			}
			existingID = verified.UserID
		} else {
			if !orgUsername.MatchString(username) {
				return nil, fmt.Errorf("%w: use a username of 3–64 letters, numbers, dots, dashes, or underscores", ErrOrgCredentials)
			}
			if utf8.RuneCountInString(password) < 12 {
				return nil, fmt.Errorf("%w: use a password with at least 12 characters", ErrOrgCredentials)
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if err != nil {
				return nil, err
			}
			id, err := orgToken()
			if err != nil {
				return nil, err
			}
			user = models.OrgUser{Principal: models.Principal{UserID: id, Username: username, DisplayName: username}, PasswordHash: string(hash)}
		}
	}
	return s.Store.AcceptInvitation(ctx, s.tokenHash(token), time.Now().Unix(), user, existingID)
}
func (s *OrganizationService) Changes() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changes
}
func (s *OrganizationService) Notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.changes)
	s.changes = make(chan struct{})
}

// RegisterHostedSession makes a verified SSO login revocable locally. Signed
// cookies from before the migration must complete SSO again.
func (s *OrganizationService) RegisterHostedSession(ctx context.Context, token, userID string, expiresAt int64) error {
	return s.Store.CreateSession(ctx, s.tokenHash(token), userID, expiresAt)
}

// HostedSession is called only after signature, audience and expiry validation.
func (s *OrganizationService) HostedSession(ctx context.Context, token string) (*models.OrgUser, error) {
	return s.Store.SessionUser(ctx, s.tokenHash(token), time.Now().Unix())
}
