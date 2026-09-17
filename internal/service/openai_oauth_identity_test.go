package service

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func openAITestJWT(payload string) string {
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature"
}

func newOpenAITestVerifier(t *testing.T) (*OpenAIOAuthIdentityVerifier, *rsa.PrivateKey) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	exponent := big.NewInt(int64(privateKey.PublicKey.E)).Bytes()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "test-key", "kty": "RSA", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(exponent),
		}}})
	}))
	t.Cleanup(server.Close)
	return &OpenAIOAuthIdentityVerifier{
		issuer: openAIOAuthIssuer, audience: openAIOAuthAudience, jwksURL: server.URL, client: server.Client(),
	}, privateKey
}

func signOpenAITestJWT(t *testing.T, privateKey *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test-key", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	encodedHeader := base64.RawURLEncoding.EncodeToString(header)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(encodedHeader + "." + encodedPayload))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return encodedHeader + "." + encodedPayload + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func signedOpenAITestIdentity(t *testing.T, verifier *OpenAIOAuthIdentityVerifier, key *rsa.PrivateKey, user, account string) OpenAIOAuthIdentity {
	t.Helper()
	identity, err := verifier.Resolve(context.Background(), signOpenAITestJWT(t, key, map[string]any{
		"iss": openAIOAuthIssuer, "aud": []string{openAIOAuthAudience}, "sub": user,
		"chatgpt_account_id": account, "email": "owner@example.com", "exp": 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestOpenAIOAuthIdentityVerifierRequiresValidSignatureIssuerAndAudience(t *testing.T) {
	verifier, key := newOpenAITestVerifier(t)
	identity := signedOpenAITestIdentity(t, verifier, key, "user-1", "account-1")
	if identity.AccountID != "account-1" || identity.DisplayName != "owner@example.com" || identity.PrincipalHash == "" {
		t.Fatalf("identity = %+v", identity)
	}
	if strings.Contains(identity.PrincipalHash, "user-1") || strings.Contains(identity.PrincipalHash, "account-1") {
		t.Fatalf("principal hash exposes provider identity: %q", identity.PrincipalHash)
	}

	wrongIssuer := signOpenAITestJWT(t, key, map[string]any{"iss": "https://attacker.invalid", "aud": openAIOAuthAudience, "sub": "user-1", "chatgpt_account_id": "account-1"})
	if _, err := verifier.Resolve(context.Background(), wrongIssuer); err == nil {
		t.Fatal("accepted wrong issuer")
	}
	wrongAudience := signOpenAITestJWT(t, key, map[string]any{"iss": openAIOAuthIssuer, "aud": "other", "sub": "user-1", "chatgpt_account_id": "account-1"})
	if _, err := verifier.Resolve(context.Background(), wrongAudience); err == nil {
		t.Fatal("accepted wrong audience")
	}
	withoutSubject, err := verifier.Resolve(context.Background(), signOpenAITestJWT(t, key, map[string]any{"iss": openAIOAuthIssuer, "aud": openAIOAuthAudience, "chatgpt_account_id": "account-1"}))
	if err != nil || withoutSubject.PrincipalHash != "" {
		t.Fatalf("account-only identity = %+v, %v; want no adoption evidence", withoutSubject, err)
	}
	withoutAccount, err := verifier.Resolve(context.Background(), signOpenAITestJWT(t, key, map[string]any{"iss": openAIOAuthIssuer, "aud": openAIOAuthAudience, "sub": "user-1"}))
	if err != nil || withoutAccount.PrincipalHash != "" {
		t.Fatalf("user-only identity = %+v, %v; want no adoption evidence", withoutAccount, err)
	}
	tampered := signOpenAITestJWT(t, key, map[string]any{"iss": openAIOAuthIssuer, "aud": openAIOAuthAudience, "sub": "user-1", "chatgpt_account_id": "account-1"})
	tamperedParts := strings.Split(tampered, ".")
	replacement := byte('A')
	if tamperedParts[1][0] == replacement {
		replacement = 'B'
	}
	tamperedParts[1] = string(replacement) + tamperedParts[1][1:]
	tampered = strings.Join(tamperedParts, ".")
	if _, err := verifier.Resolve(context.Background(), tampered); err == nil {
		t.Fatal("accepted invalid signature")
	}
}

func TestOpenAIOAuthIdentityVerifierSeparatesUsersAndAccounts(t *testing.T) {
	verifier, key := newOpenAITestVerifier(t)
	principal := func(user, account string) string {
		return signedOpenAITestIdentity(t, verifier, key, user, account).PrincipalHash
	}
	base := principal("user-1", "account-1")
	if base == "" || base != principal("user-1", "account-1") {
		t.Fatal("same OpenAI user/account did not produce a stable principal")
	}
	if base == principal("user-2", "account-1") {
		t.Fatal("different OpenAI users in one account shared a principal")
	}
	if base == principal("user-1", "account-2") {
		t.Fatal("one OpenAI user in different accounts shared a principal")
	}
}
