package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

var chromeProfileID = regexp.MustCompile(`^(Default|Profile [0-9]+)$`)

func (s *ProjectComputerService) ChromeImportAvailable() bool {
	return runtime.GOOS == "darwin" && s.provisioner.Provider() == "local"
}
func chromeRoot() (string, error) {
	home, err := os.UserHomeDir()
	return filepath.Join(home, "Library/Application Support/Google/Chrome"), err
}
func chromeDB(root, profile string) (*sql.DB, error) {
	if !chromeProfileID.MatchString(profile) {
		return nil, errors.New("choose a Chrome profile")
	}
	path := filepath.Join(root, profile, "Network", "Cookies")
	if _, err := os.Stat(path); err != nil {
		path = filepath.Join(root, profile, "Cookies")
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	return sql.Open("sqlite", u.String())
}

// Only names and domains leave this metadata API. No cookie values are selected.
func (s *ProjectComputerService) ChromeProfiles() ([]string, error) {
	if !s.ChromeImportAvailable() {
		return nil, errors.New("Chrome import requires local macOS")
	}
	root, err := chromeRoot()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, errors.New("Chrome profiles unavailable")
	}
	profiles := []string{}
	for _, e := range entries {
		if e.IsDir() && chromeProfileID.MatchString(e.Name()) {
			profiles = append(profiles, e.Name())
		}
	}
	return profiles, nil
}
func decryptChromeCookie(encrypted, key []byte, host string, version int) (string, error) {
	if len(encrypted) < 19 || string(encrypted[:3]) != "v10" || (len(encrypted)-3)%aes.BlockSize != 0 {
		return "", fmt.Errorf("unsupported cookie encryption")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	plain := append([]byte(nil), encrypted[3:]...)
	defer clear(plain)
	cipher.NewCBCDecrypter(block, bytes.Repeat([]byte{32}, 16)).CryptBlocks(plain, plain)
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > 16 || pad > len(plain) {
		return "", fmt.Errorf("invalid cookie padding")
	}
	for _, v := range plain[len(plain)-pad:] {
		if int(v) != pad {
			return "", fmt.Errorf("invalid cookie padding")
		}
	}
	plain = plain[:len(plain)-pad]
	if version >= 24 {
		hash := sha256.Sum256([]byte(host))
		if len(plain) < 32 || !bytes.Equal(plain[:32], hash[:]) {
			return "", fmt.Errorf("cookie domain mismatch")
		}
		plain = plain[32:]
	}
	return string(plain), nil
}

func selectedChromeCookies(ctx context.Context, db *sql.DB, key []byte, sites []string, selections ...models.ChromeCookieSelection) ([]map[string]any, error) {
	if len(sites) == 0 || len(sites) > 100 {
		return nil, errors.New("select sites to import")
	}
	selected := map[string]bool{}
	for _, site := range sites {
		if site == "" || strings.ContainsAny(site, "/: ") {
			return nil, errors.New("invalid site selection")
		}
		selected[site] = true
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='version'`).Scan(&version); err != nil {
		return nil, errors.New("unsupported Chrome cookie database")
	}
	// Partitioned cookies require a partition key; do not flatten them into ordinary cookies.
	partition, err := chromeHasPartitionKey(ctx, db)
	if err != nil {
		return nil, err
	}
	args := make([]any, 0, len(selected))
	marks := make([]string, 0, len(selected))
	for site := range selected {
		args = append(args, site)
		marks = append(marks, "?")
	}
	query := `SELECT host_key,name,value,encrypted_value,path,expires_utc,is_secure,is_httponly,samesite FROM cookies WHERE host_key IN (` + strings.Join(marks, ",") + `)`
	if len(selections) > 0 {
		clauses := []string{}
		for _, selection := range selections {
			clauses = append(clauses, "(host_key=? AND name=? AND path=?)")
			args = append(args, selection.Domain, selection.Name, selection.Path)
		}
		query += " AND (" + strings.Join(clauses, " OR ") + ")"
	}
	if partition {
		query += ` AND top_frame_site_key=''`
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errors.New("could not read Chrome cookies")
	}
	defer rows.Close()
	cookies := []map[string]any{}
	for rows.Next() {
		var host, name, value, path string
		var encrypted []byte
		var expiry int64
		var secure, httpOnly bool
		var sameSite int
		if err := rows.Scan(&host, &name, &value, &encrypted, &path, &expiry, &secure, &httpOnly, &sameSite); err != nil {
			return nil, errors.New("could not read Chrome cookies")
		}
		if !selected[host] {
			continue
		}
		seconds := expiry/1000000 - 11644473600
		if expiry != 0 && seconds <= time.Now().Unix() {
			continue
		}
		if len(encrypted) > 0 {
			value, err = decryptChromeCookie(encrypted, key, host, version)
			if err != nil {
				continue
			}
		}
		c := map[string]any{"name": name, "value": value, "domain": host, "path": path, "secure": secure, "httpOnly": httpOnly}
		if expiry != 0 {
			c["expires"] = seconds
		}
		switch sameSite {
		case 0:
			if secure {
				c["sameSite"] = "None"
			}
		case 1:
			c["sameSite"] = "Lax"
		case 2:
			c["sameSite"] = "Strict"
		}
		cookies = append(cookies, c)
	}
	return cookies, rows.Err()
}

func chromeHasPartitionKey(ctx context.Context, db *sql.DB) (bool, error) {
	columns, err := db.QueryContext(ctx, `PRAGMA table_info(cookies)`)
	if err != nil {
		return false, err
	}
	partition := false
	for columns.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err = columns.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			columns.Close()
			return false, err
		}
		if name == "top_frame_site_key" {
			partition = true
		}
	}
	columns.Close()
	return partition, columns.Err()
}
