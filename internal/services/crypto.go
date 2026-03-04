package services

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
)

// Encrypt encrypts plaintext using AES-256-GCM.
// key must be the ENCRYPTION_KEY env var (any length — hashed to 32 bytes).
func Encrypt(plaintext, key string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	k := deriveKey(key)
	block, err := aes.NewCipher(k)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts a value produced by Encrypt.
func Decrypt(ciphertext64, key string) (string, error) {
	if ciphertext64 == "" {
		return "", nil
	}
	k := deriveKey(key)
	data, err := base64.StdEncoding.DecodeString(ciphertext64)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ciphertext := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// deriveKey hashes any-length key to 32 bytes for AES-256.
func deriveKey(key string) []byte {
	h := sha256.Sum256([]byte(key))
	return h[:]
}
