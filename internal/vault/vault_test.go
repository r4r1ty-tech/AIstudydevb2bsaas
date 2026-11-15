package vault

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestConcurrentLoadSingleKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")

	const workers = 32
	vaults := make([]*Vault, workers)
	errs := make([]error, workers)

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			vaults[i], errs[i] = Load("", path)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d Load failed: %v", i, err)
		}
	}

	sealed, err := vaults[0].Seal("конкурентный-секрет")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	for i, v := range vaults {
		got, err := v.Open(sealed)
		if err != nil || got != "конкурентный-секрет" {
			t.Fatalf("worker %d получил другой ключ: got=%q err=%v", i, got, err)
		}
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("key perm = %v, want 0600", st.Mode().Perm())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, ent := range entries {
		if strings.HasPrefix(ent.Name(), ".vault-key-") {
			t.Fatalf("остался временный файл: %s", ent.Name())
		}
	}
}
