package nodeaccess

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"

	"atlas/pkg/api"
	"atlas/pkg/storage"
)

func testVault(t *testing.T) (*CredentialVault, *storage.DB) {
	t.Helper()
	db, err := storage.InitDB(t.TempDir() + "/atlas.db")
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	vault, err := NewCredentialVault(db, key)
	if err != nil {
		t.Fatal(err)
	}
	return vault, db
}

func TestCredentialVaultEncryptsAtRestAndDecryptsForAuthentication(t *testing.T) {
	vault, db := testVault(t)
	record, err := vault.Create(CredentialInput{
		ProfileID: "node-password-a", Priority: 10,
		Username: "atlas-readonly", Password: "correct horse battery staple", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.UsernameMasked != "••••••" || bytes.Contains(record.Ciphertext, []byte("atlas-readonly")) || bytes.Contains(record.Ciphertext, []byte("correct horse")) {
		t.Fatalf("credential material was not protected: %#v", record)
	}
	var stored api.NodeCredentialProfile
	if err := db.First(&stored, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored.Ciphertext, []byte("atlas-readonly")) || bytes.Contains(stored.Ciphertext, []byte("correct horse")) {
		t.Fatal("database ciphertext contains plaintext credential material")
	}
	profiles, err := vault.Profiles()
	if err != nil || len(profiles) != 1 || profiles[0].Username != "atlas-readonly" || profiles[0].SecretRef != "vault:node-password-a" {
		t.Fatalf("unexpected authentication profile: %#v err=%v", profiles, err)
	}
	secret, err := vault.Resolve(profiles[0].SecretRef)
	if err != nil || string(secret) != "correct horse battery staple" {
		t.Fatalf("unexpected resolved secret: %q err=%v", secret, err)
	}
	clear(secret)
}

func TestCredentialVaultRejectsWrongMasterKey(t *testing.T) {
	vault, db := testVault(t)
	if _, err := vault.Create(CredentialInput{
		ProfileID: "node-password-a", Priority: 10,
		Username: "atlas", Password: "secret", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	otherKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x17}, 32))
	otherVault, err := NewCredentialVault(db, otherKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherVault.Profiles(); err == nil {
		t.Fatal("expected authenticated decryption to reject a different master key")
	}
}

func TestCredentialProfileJSONIsRedacted(t *testing.T) {
	vault, _ := testVault(t)
	record, err := vault.Create(CredentialInput{
		ProfileID: "node-password-a", Priority: 10,
		Username: "atlas", Password: "secret", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{[]byte("atlas"), []byte("secret"), record.Ciphertext, []byte("ciphertext")} {
		if len(forbidden) > 0 && bytes.Contains(encoded, forbidden) {
			t.Fatalf("JSON exposed credential material %q: %s", forbidden, encoded)
		}
	}
}
