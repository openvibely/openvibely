package service

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/chromefixture"
	"path/filepath"
	"strings"
	"testing"
)

func TestChromeCookieSelectionAndEncryption(t *testing.T) {
	db, err := chromefixture.Open(filepath.Join(t.TempDir(), "cookies.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE meta(key TEXT,value TEXT);INSERT INTO meta VALUES('version','24');CREATE TABLE cookies(host_key TEXT,name TEXT,value TEXT,encrypted_value BLOB,path TEXT,expires_utc INTEGER,is_secure INTEGER,is_httponly INTEGER,samesite INTEGER,top_frame_site_key TEXT);`)
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{7}, 16)
	host := ".selected.test"
	hash := sha256.Sum256([]byte(host))
	plain := append(hash[:], []byte("fixture-secret")...)
	pad := 16 - len(plain)%16
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, _ := aes.NewCipher(key)
	cipher.NewCBCEncrypter(block, bytes.Repeat([]byte{32}, 16)).CryptBlocks(plain, plain)
	encrypted := append([]byte("v10"), plain...)
	for _, row := range []struct {
		host, name, partition string
		expiry                int64
	}{{host, "selected", "", 0}, {"other.test", "other", "", 0}, {host, "partitioned", "https://partition.test", 0}, {host, "expired", "", 1}} {
		if _, err := db.Exec(`INSERT INTO cookies VALUES(?,?,?,?,'/',?,1,1,1,?)`, row.host, row.name, "", encrypted, row.expiry, row.partition); err != nil {
			t.Fatal(err)
		}
	}
	choices, err := chromeCookieChoices(t.Context(), db, "Default")
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 2 {
		t.Fatalf("choices: %+v", choices)
	}
	for _, c := range choices {
		if c.Name == "partitioned" || c.Name == "expired" {
			t.Fatalf("unimportable choice: %+v", c)
		}
	}
	cookies, err := selectedChromeCookies(t.Context(), db, key, []string{host})
	if err != nil {
		t.Fatal(err)
	}
	if len(cookies) != 1 || cookies[0]["name"] != "selected" || cookies[0]["value"] != "fixture-secret" {
		t.Fatal("wrong selected cookies")
	}
	if _, err := db.Exec(`INSERT INTO cookies VALUES(?,?,?,?,'/',0,1,1,1,'')`, host, "not-selected", "", encrypted); err != nil {
		t.Fatal(err)
	}
	exact, err := selectedChromeCookies(t.Context(), db, key, []string{host}, models.ChromeCookieSelection{Profile: "Default", Domain: host, Name: "selected", Path: "/"})
	if err != nil || len(exact) != 1 || exact[0]["name"] != "selected" {
		t.Fatal("individual cookie consent was ignored", err)
	}
	if _, err := decryptChromeCookie(encrypted, key, "other.test", 24); err == nil {
		t.Fatal("host integrity not checked")
	}
	if _, err := selectedChromeCookies(t.Context(), db, key, nil); err == nil {
		t.Fatal("empty consent accepted")
	}
	if _, err := chromeDB(t.TempDir(), "../Default"); err == nil {
		t.Fatal("profile traversal allowed")
	}
	if _, err := decryptChromeCookie([]byte("v20"+strings.Repeat("x", 32)), key, host, 24); err == nil {
		t.Fatal("unsupported encryption accepted")
	}
}

func TestChromeChoicesLegacySchema(t *testing.T) {
	db, err := chromefixture.Open(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE cookies(host_key TEXT,name TEXT,path TEXT,expires_utc INTEGER);INSERT INTO cookies VALUES('example.test','session','/',0)`)
	if err != nil {
		t.Fatal(err)
	}
	choices, err := chromeCookieChoices(t.Context(), db, "Default")
	if err != nil || len(choices) != 1 {
		t.Fatalf("%+v %v", choices, err)
	}
}
