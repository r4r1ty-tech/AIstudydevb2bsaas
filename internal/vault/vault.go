// Package vault шифрует пароли и сессии аккаунтов универа перед записью в sqlite.
// Ключ лежит отдельно от базы (файл 0600 или SECRET_KEY), так что утёкшая база
// без ключа паролей не раскрывает.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const keyLen = 32

var ErrBadCiphertext = errors.New("vault: bad ciphertext")

type Vault struct {
	aead cipher.AEAD
	key  []byte
}

// New собирает хранилище из 32-байтового ключа.
func New(key []byte) (*Vault, error) {
	if len(key) != keyLen {
		return nil, fmt.Errorf("vault: key must be %d bytes, got %d", keyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("vault: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: gcm: %w", err)
	}
	return &Vault{aead: aead, key: append([]byte(nil), key...)}, nil
}

// Load берёт ключ из env (base64), иначе из файла; файла нет — создаёт его.
// Все процессы читают один и тот же файл, поэтому создание идёт через O_EXCL.
func Load(envKey, path string) (*Vault, error) {
	if s := strings.TrimSpace(envKey); s != "" {
		key, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("vault: SECRET_KEY is not base64: %w", err)
		}
		return New(key)
	}
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("vault: no key path")
	}
	if key, err := readKey(path); err == nil {
		return New(key)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("vault: mkdir: %w", err)
	}
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("vault: rand: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) { // соседний процесс успел первым
			key, rerr := readKey(path)
			if rerr != nil {
				return nil, rerr
			}
			return New(key)
		}
		return nil, fmt.Errorf("vault: create key: %w", err)
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		f.Close()
		return nil, fmt.Errorf("vault: write key: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("vault: close key: %w", err)
	}
	return New(key)
}

func readKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("vault: key file %s is not base64: %w", path, err)
	}
	return key, nil
}

// Seal шифрует строку; пустая остаётся пустой.
func (v *Vault) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("vault: nonce: %w", err)
	}
	out := v.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Open расшифровывает результат Seal.
func (v *Vault) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", ErrBadCiphertext
	}
	n := v.aead.NonceSize()
	if len(raw) < n {
		return "", ErrBadCiphertext
	}
	plain, err := v.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", ErrBadCiphertext
	}
	return string(plain), nil
}

// Derive выводит из ключа хранилища отдельный ключ под назначение label
// (HMAC-SHA256), чтобы один секрет не использовался для разных целей напрямую.
func (v *Vault) Derive(label string) []byte {
	m := hmac.New(sha256.New, v.key)
	m.Write([]byte(label))
	return m.Sum(nil)
}
