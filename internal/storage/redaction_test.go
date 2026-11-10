package storage

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAccountRedactionAndMaskedProxyRoundTrip(t *testing.T) {
	acc := Account{ID: "acc1", APIKey: "AIza-secret", ProxyURL: "socks5://alice:p%40ss@proxy.example:1080"}
	red := acc.Redacted()
	if red.APIKey != "" || !red.HasAPIKey {
		t.Fatalf("api_key must be dropped and has_api_key set, got %+v", red)
	}
	if strings.Contains(red.ProxyURL, "p%40ss") || !strings.Contains(red.ProxyURL, "alice:"+MaskedSecret+"@proxy.example:1080") {
		t.Fatalf("proxy password must be masked, got %q", red.ProxyURL)
	}

	if got := RestoreMaskedProxyURL(red.ProxyURL, acc.ProxyURL); got != acc.ProxyURL {
		t.Fatalf("masked proxy URL must restore the stored password, got %q", got)
	}
	if got := RestoreMaskedProxyURL("socks5://alice:"+MaskedSecret+"@other.example:1080", acc.ProxyURL); strings.Contains(got, "p%40ss") {
		t.Fatalf("password must not be restored for a different host, got %q", got)
	}

	store, err := NewStore(filepath.Join(t.TempDir(), "gw.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	acc.NetworkMode = "proxy"
	if _, err := store.UpsertAccount(acc); err != nil {
		t.Fatal(err)
	}
	edit := red
	edit.Name = "renamed"
	saved, err := store.UpsertAccount(edit)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ProxyURL != acc.ProxyURL || saved.APIKey != acc.APIKey {
		t.Fatalf("saving a redacted record must keep stored secrets, got proxy=%q key=%q", saved.ProxyURL, saved.APIKey)
	}
}

func TestValidateAccountRejectsUnsafeIDs(t *testing.T) {
	for _, id := range []string{"../etc", "a/b", ".", "..", "has space", strings.Repeat("x", 65)} {
		acc := Account{ID: id}
		if err := ValidateAccount(&acc); err == nil {
			t.Fatalf("expected account id %q to be rejected", id)
		}
	}
	acc := Account{ID: "acc_iso-2.prod"}
	if err := ValidateAccount(&acc); err != nil {
		t.Fatalf("expected valid id, got %v", err)
	}
}
