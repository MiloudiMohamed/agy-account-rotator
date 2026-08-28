package vaultcrypto

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"

	"github.com/MiloudiMohamed/agy-account-rotator/internal/store"
)

// RFC 6070 PBKDF2 test vector for HMAC-SHA-256
func TestPBKDF2SHA256Vector(t *testing.T) {
	// Password = "password", Salt = "salt", c = 1, dkLen = 32
	// Expected DK: 120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b
	pwd := []byte("password")
	salt := []byte("salt")
	dk := pbkdf2SHA256(pwd, salt, 1, 32)
	wantHex := "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"
	if hex.EncodeToString(dk) != wantHex {
		t.Fatalf("PBKDF2 vector mismatch:\ngot  %x\nwant %s", dk, wantHex)
	}
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	payload := &ExportPayload{
		ExportedAt: time.Now().UTC(),
		Version:    "0.4.0",
		Config:     store.Config{Mode: "smart"},
		Accounts: []*store.Account{
			{
				Email: "test1@gmail.com",
				Label: "work",
				File: store.TokenFile{
					Token: store.Token{
						AccessToken:  "at-secret-1",
						RefreshToken: "rt-secret-1",
					},
					AuthMethod: "consumer",
				},
			},
			{
				Email: "test2@gmail.com",
				File: store.TokenFile{
					Token: store.Token{
						AccessToken:  "at-secret-2",
						RefreshToken: "rt-secret-2",
					},
					AuthMethod: "consumer",
				},
			},
		},
	}

	passphrase := "correct-horse-battery-staple"
	encrypted, err := EncryptVault(payload, passphrase)
	if err != nil {
		t.Fatal(err)
	}

	// Must not contain secret plaintext tokens
	if bytes.Contains(encrypted, []byte("rt-secret-1")) || bytes.Contains(encrypted, []byte("test1@gmail.com")) {
		t.Fatal("encrypted envelope contains raw plaintext secrets")
	}

	// Decrypt with correct passphrase
	decrypted, err := DecryptVault(encrypted, passphrase)
	if err != nil {
		t.Fatalf("decryption failed: %v", err)
	}
	if len(decrypted.Accounts) != 2 || decrypted.Accounts[0].Email != "test1@gmail.com" {
		t.Fatalf("unexpected decrypted payload: %+v", decrypted)
	}
	if decrypted.Accounts[0].File.Token.RefreshToken != "rt-secret-1" {
		t.Fatalf("token mismatch: %q", decrypted.Accounts[0].File.Token.RefreshToken)
	}

	// Decrypt with wrong passphrase must fail
	if _, err := DecryptVault(encrypted, "wrong-password"); err == nil {
		t.Fatal("expected error with wrong passphrase")
	}

	// Empty passphrase
	if _, err := EncryptVault(payload, ""); err == nil {
		t.Fatal("expected error with empty passphrase")
	}
}
