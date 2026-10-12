// Package credentials protects UI-supplied provider credentials with operator
// keys. Neither ciphertext nor plaintext belongs in a settings projection.
package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"strings"
)

var ErrKey = errors.New("provider credential key unavailable; configure FREEFSM_CREDENTIAL_KEYS with the original operator key")
var ErrReview = errors.New("legacy provider credential requires explicit review and replacement in Mail settings")

const prefix = "aesgcm:v1:"

// ValidateConfiguration permits an unconfigured optional provider, but rejects
// a malformed operator key ring before traffic is served.
func ValidateConfiguration() error {
	if os.Getenv("FREEFSM_CREDENTIAL_KEYS") == "" {
		return nil
	}
	_, err := keys()
	return err
}

func keys() ([]cipher.AEAD, error) {
	raw := os.Getenv("FREEFSM_CREDENTIAL_KEYS")
	if raw == "" {
		return nil, ErrKey
	}
	var result []cipher.AEAD
	for _, part := range strings.Split(raw, ",") {
		b, e := base64.StdEncoding.DecodeString(strings.TrimSpace(part))
		if e != nil || len(b) != 32 {
			return nil, ErrKey
		}
		block, e := aes.NewCipher(b)
		if e != nil {
			return nil, ErrKey
		}
		a, e := cipher.NewGCM(block)
		if e != nil {
			return nil, ErrKey
		}
		result = append(result, a)
	}
	return result, nil
}

func Seal(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	ring, err := keys()
	if err != nil {
		return "", err
	}
	a := ring[0]
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", ErrKey
	}
	data := a.Seal(nonce, nonce, []byte(value), []byte("freefsm:smtp-password:v1"))
	return prefix + base64.RawStdEncoding.EncodeToString(data), nil
}

func Open(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, prefix) {
		return "", ErrReview
	}
	ring, err := keys()
	if err != nil {
		return "", err
	}
	data, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil {
		return "", ErrReview
	}
	for _, a := range ring {
		if len(data) < a.NonceSize()+a.Overhead() {
			return "", ErrReview
		}
		plain, e := a.Open(nil, data[:a.NonceSize()], data[a.NonceSize():], []byte("freefsm:smtp-password:v1"))
		if e == nil {
			return string(plain), nil
		}
	}
	return "", ErrKey
}
