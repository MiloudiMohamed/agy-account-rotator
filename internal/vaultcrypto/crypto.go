// Package vaultcrypto provides passphrase-based AES-256-GCM encryption and
// decryption for securely exporting and importing account vaults across machines.
package vaultcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/MiloudiMohamed/agy-account-rotator/internal/store"
)

const (
	CurrentVersion = 1
	KDFType        = "pbkdf2_sha256"
	Iterations     = 100000
	KeyLen         = 32
	SaltLen        = 16
	NonceLen       = 12
)

type VaultEnvelope struct {
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

type ExportPayload struct {
	ExportedAt time.Time        `json:"exported_at"`
	Version    string           `json:"version"`
	Accounts   []*store.Account `json:"accounts"`
	Config     store.Config     `json:"config"`
}

// pbkdf2SHA256 implements RFC 8018 PBKDF2 using HMAC-SHA-256 (stdlib only).
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen
	var out []byte

	var buf [4]byte
	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(buf[:], uint32(block))
		prf.Write(buf[:])
		u := prf.Sum(nil)

		t := make([]byte, hashLen)
		copy(t, u)

		for i := 1; i < iter; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for k := 0; k < hashLen; k++ {
				t[k] ^= u[k]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

// EncryptVault packs accounts and config into an encrypted JSON envelope.
func EncryptVault(payload *ExportPayload, passphrase string) ([]byte, error) {
	if passphrase == "" {
		return nil, errors.New("passphrase cannot be empty")
	}

	plainJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	salt := make([]byte, SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}

	key := pbkdf2SHA256([]byte(passphrase), salt, Iterations, KeyLen)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, NonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}

	sealed := gcm.Seal(nil, nonce, plainJSON, nil)

	env := VaultEnvelope{
		Version:    CurrentVersion,
		KDF:        KDFType,
		Iterations: Iterations,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(sealed),
	}

	return json.MarshalIndent(env, "", "  ")
}

// DecryptVault unlocks and deserializes an encrypted JSON envelope.
func DecryptVault(envelopeData []byte, passphrase string) (*ExportPayload, error) {
	if passphrase == "" {
		return nil, errors.New("passphrase cannot be empty")
	}

	var env VaultEnvelope
	if err := json.Unmarshal(envelopeData, &env); err != nil {
		return nil, fmt.Errorf("invalid envelope JSON: %w", err)
	}

	if env.Version != CurrentVersion {
		return nil, fmt.Errorf("unsupported vault version %d (expected %d)", env.Version, CurrentVersion)
	}
	if env.KDF != KDFType {
		return nil, fmt.Errorf("unsupported KDF %q (expected %s)", env.KDF, KDFType)
	}

	salt, err := base64.StdEncoding.DecodeString(env.Salt)
	if err != nil || len(salt) < SaltLen {
		return nil, errors.New("invalid salt in envelope")
	}

	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil || len(nonce) != NonceLen {
		return nil, errors.New("invalid nonce in envelope")
	}

	ciphertext, err := base64.StdEncoding.DecodeString(env.Ciphertext)
	if err != nil {
		return nil, errors.New("invalid ciphertext in envelope")
	}

	iter := env.Iterations
	if iter <= 0 {
		iter = Iterations
	}

	key := pbkdf2SHA256([]byte(passphrase), salt, iter, KeyLen)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.New("decryption failed: incorrect passphrase or corrupted data")
	}

	var payload ExportPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return nil, fmt.Errorf("unmarshaling decrypted vault: %w", err)
	}

	return &payload, nil
}
