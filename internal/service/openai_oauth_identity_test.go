package service

import (
	"encoding/base64"
	"strings"
	"testing"
)

func openAITestJWT(payload string) string {
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature"
}

func TestResolveOpenAIOAuthIdentityRequiresUserAndAccountForPrincipal(t *testing.T) {
	identity := ResolveOpenAIOAuthIdentity(openAITestJWT(`{
		"sub":"user-1",
		"https://api.openai.com/auth":{"chatgpt_account_id":"account-1"},
		"https://api.openai.com/profile":{"email":"owner@example.com"}
	}`))
	if identity.AccountID != "account-1" || identity.DisplayName != "owner@example.com" || identity.PrincipalHash == "" {
		t.Fatalf("identity = %+v", identity)
	}
	if strings.Contains(identity.PrincipalHash, "user-1") || strings.Contains(identity.PrincipalHash, "account-1") {
		t.Fatalf("principal hash exposes provider identity: %q", identity.PrincipalHash)
	}

	withoutUser := ResolveOpenAIOAuthIdentity(openAITestJWT(`{"chatgpt_account_id":"account-1"}`))
	if withoutUser.AccountID != "account-1" || withoutUser.PrincipalHash != "" {
		t.Fatalf("account-only identity = %+v, want label but no adoption evidence", withoutUser)
	}
	withoutAccount := ResolveOpenAIOAuthIdentity(openAITestJWT(`{"sub":"user-1","email":"owner@example.com"}`))
	if withoutAccount.AccountID != "" || withoutAccount.PrincipalHash != "" {
		t.Fatalf("user-only identity = %+v, want no adoption evidence", withoutAccount)
	}
}

func TestResolveOpenAIOAuthIdentitySeparatesUsersAndAccounts(t *testing.T) {
	principal := func(user, account string) string {
		return ResolveOpenAIOAuthIdentity(openAITestJWT(`{"sub":"` + user + `","chatgpt_account_id":"` + account + `"}`)).PrincipalHash
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
