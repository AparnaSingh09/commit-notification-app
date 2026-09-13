// Package crypto encrypts OAuth tokens at rest with AES-256-GCM.
//
// The key is derived by SHA-256-hashing JWT_SECRET rather than requiring a
// second secret in .env, which is a reasonable simplification for local dev.
// For a real deployment, use a separate TOKEN_ENC_KEY (or a KMS-managed key)
// instead of deriving it from the session-signing secret.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
)

// DeriveKey turns an arbitrary-length secret into a 32-byte AES-256 key.
func DeriveKey(secret string) [32]byte {
	return sha256.Sum256([]byte(secret))
}

// Encrypt returns nonce||ciphertext, sealed with AES-GCM under key.
func Encrypt(key [32]byte, plaintext string) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// Decrypt reverses Encrypt.
func Decrypt(key [32]byte, data []byte) (string, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
