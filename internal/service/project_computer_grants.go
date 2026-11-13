package service

import (
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"golang.org/x/crypto/pbkdf2"
	"os/exec"
	"sort"
	"sync"
	"time"
)

// Cookie metadata is safe for the consent UI. Values are never queried here.
func (s *ProjectComputerService) ChromeCookieChoices(ctx context.Context, profile string) ([]models.ChromeCookieSelection, error) {
	if !s.ChromeImportAvailable() {
		return nil, errors.New("Chrome import requires local macOS")
	}
	root, err := chromeRoot()
	if err != nil {
		return nil, err
	}
	db, err := chromeDB(root, profile)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return chromeCookieChoices(ctx, db, profile)
}

func chromeCookieChoices(ctx context.Context, db *sql.DB, profile string) ([]models.ChromeCookieSelection, error) {
	partition, err := chromeHasPartitionKey(ctx, db)
	if err != nil {
		return nil, err
	}
	query := `SELECT DISTINCT host_key,name,path FROM cookies WHERE (expires_utc=0 OR expires_utc>?)`
	if partition {
		query += ` AND top_frame_site_key=''`
	}
	rows, err := db.QueryContext(ctx, query+` ORDER BY host_key,name,path LIMIT 5000`, (time.Now().Unix()+11644473600)*1000000)
	if err != nil {
		return nil, errors.New("Chrome cookie metadata unavailable")
	}
	defer rows.Close()
	choices := []models.ChromeCookieSelection{}
	for rows.Next() {
		c := models.ChromeCookieSelection{Profile: profile}
		if err := rows.Scan(&c.Domain, &c.Name, &c.Path); err != nil {
			return nil, errors.New("Chrome cookie metadata unavailable")
		}
		choices = append(choices, c)
	}
	return choices, rows.Err()
}
func (s *ProjectComputerService) ChromeGrants(ctx context.Context, id string) ([]models.ComputerChromeGrant, error) {
	return s.repo.ChromeGrants(ctx, id)
}

// Only the human consent endpoint creates grants. The shared project lock prevents
// revocation racing an import in a different task desktop.
func (s *ProjectComputerService) ImportChromeSelection(ctx context.Context, id, scope string, selections []models.ChromeCookieSelection, hours int) (int, error) {
	if !s.ChromeImportAvailable() || s.taskID == "" {
		return 0, errors.New("Chrome import requires a local task desktop")
	}
	if hours != -1 && hours != 0 && hours != 1 && hours != 24 && hours != 168 && hours != 720 {
		return 0, errors.New("choose one import, 1 hour, 1 day, 7 days, 30 days or until revoked")
	}
	for _, c := range selections {
		if !chromeProfileID.MatchString(c.Profile) || c.Domain == "" || c.Name == "" || c.Path == "" {
			return 0, errors.New("select individual cookies from a Chrome profile")
		}
	}
	gate := s.lifecycle(id)
	gate.RLock()
	defer gate.RUnlock()
	lock := s.lock(id)
	lock.Lock()
	defer lock.Unlock()
	grants := s.grantLock(id)
	grants.Lock()
	defer grants.Unlock()
	expires := int64(hours)
	if hours > 0 {
		expires = time.Now().Add(time.Duration(hours) * time.Hour).Unix()
	}
	g, err := s.repo.CreateChromeGrant(ctx, id, scope, selections, expires)
	if err != nil {
		return 0, err
	}
	count, err := s.installChromeGrant(ctx, id, g)
	return count, err
}
func (s *ProjectComputerService) installChromeGrant(ctx context.Context, id string, g *models.ComputerChromeGrant, resume ...bool) (int, error) {
	if !s.ChromeImportAvailable() || g.Revoked || (g.ExpiresAt > 0 && g.ExpiresAt <= time.Now().Unix()) || (g.Scope != "project" && g.TaskID != s.taskID) {
		return 0, errors.New("Chrome grant is unavailable or expired")
	}
	progress, err := s.repo.ChromeImportProgress(ctx, id, g.ID)
	if err != nil {
		return 0, err
	}
	installed, err := s.repo.ChromeInstallations(ctx, id, g.ID)
	if err != nil {
		return 0, err
	}
	used := false
	for _, task := range installed {
		if task == s.taskID {
			used = true
		}
	}
	if g.OneTime && len(installed) > 0 && !(len(resume) > 0 && resume[0] && used) {
		return 0, errors.New("one-time Chrome import has already been used")
	}
	if len(resume) == 0 || !resume[0] {
		progress = map[string]int{}
	}
	root, err := chromeRoot()
	if err != nil {
		return 0, err
	}
	secret, err := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-w", "-s", "Chrome Safe Storage", "-a", "Chrome").Output()
	if err != nil {
		return 0, errors.New("Chrome Keychain access was not granted")
	}
	defer clear(secret)
	key := pbkdf2.Key(bytes.TrimSpace(secret), []byte("saltysalt"), 1003, 16, sha1.New)
	defer clear(key)
	byProfile := map[string][]models.ChromeCookieSelection{}
	for _, c := range g.Selections {
		byProfile[c.Profile] = append(byProfile[c.Profile], c)
	}
	return s.installChromeProfiles(ctx, id, g, progress, used, func(profile string) ([]map[string]any, error) {
		picks := byProfile[profile]
		db, err := chromeDB(root, profile)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		sites := []string{}
		for _, pick := range picks {
			sites = append(sites, pick.Domain)
		}
		return selectedChromeCookies(ctx, db, key, sites, picks...)
	})
}
func (s *ProjectComputerService) installChromeProfiles(ctx context.Context, id string, g *models.ComputerChromeGrant, progress map[string]int, used bool, load func(string) ([]map[string]any, error)) (int, error) {
	byProfile := map[string]bool{}
	for _, pick := range g.Selections {
		byProfile[pick.Profile] = true
	}
	profiles := []string{}
	for profile := range byProfile {
		profiles = append(profiles, profile)
	}
	sort.Strings(profiles)
	// Persist the approved work before touching a browser so retries and revocation
	// cover both acknowledged and ambiguous partial imports.
	for _, profile := range profiles {
		if _, ok := progress[profile]; !ok {
			if err := s.repo.RecordChromeProfile(ctx, id, g.ID, profile, 0); err != nil {
				return 0, err
			}
		}
	}
	if !used {
		if err := s.repo.RecordChromeInstall(ctx, id, g.ID); err != nil {
			return 0, err
		}
	}
	conn, err := s.Connection(ctx, id)
	if err != nil {
		return 0, &ChromeImportIncomplete{GrantID: g.ID}
	}
	total := 0
	for _, profile := range profiles {
		if count := progress[profile]; count > 0 {
			total += count
			continue
		}
		cookies, err := load(profile)
		if err != nil || len(cookies) == 0 {
			return total, &ChromeImportIncomplete{GrantID: g.ID}
		}
		for _, cookie := range cookies {
			expiry, ok := cookie["expires"].(int64)
			if g.ExpiresAt > 0 && (!ok || expiry > g.ExpiresAt) {
				cookie["expires"] = g.ExpiresAt
			}
		}
		payload, _ := json.Marshal(map[string]any{"action": "import_cookies", "cookies": cookies})
		_, err = s.request(ctx, conn, "POST", "/browser", payload)
		clear(payload)
		if err != nil {
			return total, &ChromeImportIncomplete{GrantID: g.ID}
		}
		if err := s.repo.RecordChromeProfile(ctx, id, g.ID, profile, len(cookies)); err != nil {
			return total, &ChromeImportIncomplete{GrantID: g.ID}
		}
		total += len(cookies)
	}
	return total, nil
}

type ChromeImportIncomplete struct{ GrantID string }

func (e *ChromeImportIncomplete) Error() string {
	return "Chrome import is incomplete. Retry to resume the remaining profiles."
}
func (s *ProjectComputerService) ResumeChromeImport(ctx context.Context, id, grantID string) (int, error) {
	gate := s.lifecycle(id)
	gate.RLock()
	defer gate.RUnlock()
	lock := s.lock(id)
	lock.Lock()
	defer lock.Unlock()
	grants := s.grantLock(id)
	grants.Lock()
	defer grants.Unlock()
	state, err := s.repo.Get(ctx, id)
	if err != nil {
		return 0, err
	}
	if !state.Held || state.HandoffKind != "request_chrome_login" {
		return 0, errors.New("no Chrome import awaiting consent")
	}
	g, err := s.repo.ChromeGrant(ctx, id, grantID)
	if err != nil {
		return 0, err
	}
	if g.TaskID != s.taskID {
		return 0, errors.New("import belongs to another task")
	}
	return s.installChromeGrant(ctx, id, g, true)
}
func (s *ProjectComputerService) PendingChromeImport(ctx context.Context, id string) (*models.ComputerChromeGrant, error) {
	return s.repo.PendingChromeImport(ctx, id)
}

func (s *ProjectComputerService) UseChromeGrant(ctx context.Context, id, grantID string) (int, error) {
	lock := s.grantLock(id)
	lock.Lock()
	defer lock.Unlock()
	grant, err := s.repo.ChromeGrant(ctx, id, grantID)
	if err != nil {
		return 0, err
	}
	return s.installChromeGrant(ctx, id, grant)
}
func (s *ProjectComputerService) RevokeChromeGrant(ctx context.Context, id, grantID string) error {
	gate := s.lifecycle(id)
	gate.RLock()
	defer gate.RUnlock()
	lock := s.grantLock(id)
	lock.Lock()
	defer lock.Unlock()
	grant, err := s.repo.ChromeGrant(ctx, id, grantID)
	if err != nil {
		return err
	}
	if err := s.repo.RevokeChromeGrant(ctx, id, grantID); err != nil {
		return err
	}
	tasks, err := s.repo.ChromeInstallations(ctx, id, grantID)
	if err != nil {
		return err
	}
	for _, taskID := range tasks {
		scoped, err := s.ForTask(ctx, id, taskID)
		if err != nil {
			return err
		}
		conn, err := scoped.Connection(ctx, id)
		if err != nil {
			return errors.New("grant revoked; start the computer and retry to clear installed cookies")
		}
		payload, _ := json.Marshal(map[string]any{"action": "clear_cookies", "cookies": grant.Selections})
		if _, err = scoped.request(ctx, conn, "POST", "/browser", payload); err != nil {
			return errors.New("grant revoked; retry to finish clearing installed cookies")
		}
	}
	return nil
}

func (s *ProjectComputerService) grantLock(id string) *sync.Mutex {
	lock, _ := s.locks.LoadOrStore("chrome-grants:"+id, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func (s *ProjectComputerService) ChromeGrantsPage(ctx context.Context, id string, offset int) ([]models.ComputerChromeGrant, bool, error) {
	grants, err := s.repo.ChromeGrantsPage(ctx, id, offset)
	more := len(grants) > 200
	if more {
		grants = grants[:200]
	}
	return grants, more, err
}
