package vault

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSealOpenAndKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "secret.key")
	v1, err := Load("", path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("key perm %v", st.Mode().Perm())
	}
	sealed, err := v1.Seal("пароль")
	if err != nil || sealed == "" || sealed == "пароль" {
		t.Fatalf("seal %q %v", sealed, err)
	}
	v2, err := Load("", path) // тот же файл — тот же ключ
	if err != nil {
		t.Fatal(err)
	}
	got, err := v2.Open(sealed)
	if err != nil || got != "пароль" {
		t.Fatalf("open %q %v", got, err)
	}
	if _, err := v2.Open(sealed[:len(sealed)-4] + "AAAA"); err == nil {
		t.Fatal("tampered ciphertext opened")
	}
	if s, _ := v1.Seal(""); s != "" {
		t.Fatal("empty must stay empty")
	}
}

func TestEnvKey(t *testing.T) {
	if _, err := Load("not base64!!", ""); err == nil {
		t.Fatal("bad env key accepted")
	}
	v, err := Load("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", "")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := v.Seal("x")
	if got, _ := v.Open(s); got != "x" {
		t.Fatal("roundtrip")
	}
}
