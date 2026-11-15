package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestLLMStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestMaskKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "****"},
		{"abc", "****"},
		{"12345678", "****"},
		{"  12345678  ", "****"},
		{"123456789", "1234...6789"},
		{"abcd123456wxyz", "abcd...wxyz"},
		{"абвгдеёжз", "абвг...еёжз"},
	}
	for _, tc := range cases {
		if got := MaskKey(tc.in); got != tc.want {
			t.Errorf("MaskKey(%q) = %q, ожидали %q", tc.in, got, tc.want)
		}
	}
}

func TestLLMKeysCRUD(t *testing.T) {
	st := openTestLLMStore(t)

	// Пока ключей нет — GetLLMKey возвращает nil без ошибки.
	got, err := st.GetLLMKey(101, "gemini")
	if err != nil || got != nil {
		t.Fatalf("пустой GetLLMKey = (%v, %v), ждали (nil, nil)", got, err)
	}

	t1 := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	if err := st.PutLLMKey(LLMKeyRecord{
		TelegramID: 101,
		Provider:   "gemini",
		SecretEnc:  "enc-g-1",
		Mask:       MaskKey("AIzaSyTest1234wxyz"),
		SharePool:  false,
		UpdatedAt:  t1,
	}); err != nil {
		t.Fatalf("PutLLMKey gemini: %v", err)
	}
	if err := st.PutLLMKey(LLMKeyRecord{
		TelegramID: 101,
		Provider:   "opencode",
		SecretEnc:  "enc-oc-1",
		Mask:       MaskKey("oc_sk_1234567890"),
		SharePool:  true,
		UpdatedAt:  t1,
	}); err != nil {
		t.Fatalf("PutLLMKey opencode: %v", err)
	}
	if err := st.PutLLMKey(LLMKeyRecord{
		TelegramID: 202,
		Provider:   "gemini",
		SecretEnc:  "enc-g-2",
		Mask:       MaskKey("AIzaSyOther9999"),
		SharePool:  true,
		UpdatedAt:  t1,
	}); err != nil {
		t.Fatalf("PutLLMKey user 202: %v", err)
	}

	// UPSERT обновляет существующую запись (share_pool и secret_enc).
	t2 := t1.Add(5 * time.Minute)
	if err := st.PutLLMKey(LLMKeyRecord{
		TelegramID: 101,
		Provider:   "gemini",
		SecretEnc:  "enc-g-1-new",
		Mask:       "AIza...new1",
		SharePool:  true,
		UpdatedAt:  t2,
	}); err != nil {
		t.Fatalf("PutLLMKey upsert: %v", err)
	}

	got, err = st.GetLLMKey(101, "gemini")
	if err != nil || got == nil {
		t.Fatalf("GetLLMKey после upsert: %v %v", got, err)
	}
	if got.SecretEnc != "enc-g-1-new" || got.Mask != "AIza...new1" || !got.SharePool || !got.UpdatedAt.Equal(t2) {
		t.Fatalf("неожиданная запись: %+v", got)
	}

	byUser, err := st.ListLLMKeysByUser(101)
	if err != nil || len(byUser) != 2 {
		t.Fatalf("ListLLMKeysByUser = %d, err=%v", len(byUser), err)
	}

	sharedAll, err := st.ListSharedLLMKeys("")
	if err != nil || len(sharedAll) != 3 {
		t.Fatalf("ListSharedLLMKeys('') = %d, err=%v", len(sharedAll), err)
	}
	sharedGem, err := st.ListSharedLLMKeys("gemini")
	if err != nil || len(sharedGem) != 2 {
		t.Fatalf("ListSharedLLMKeys('gemini') = %d, err=%v", len(sharedGem), err)
	}

	// Снимаем share_pool у 101/gemini — он исчезает из ListSharedLLMKeys.
	if err := st.PutLLMKey(LLMKeyRecord{
		TelegramID: 101,
		Provider:   "gemini",
		SecretEnc:  "enc-g-1-new",
		Mask:       "AIza...new1",
		SharePool:  false,
		UpdatedAt:  t2,
	}); err != nil {
		t.Fatalf("PutLLMKey unshare: %v", err)
	}
	sharedGem, err = st.ListSharedLLMKeys("gemini")
	if err != nil || len(sharedGem) != 1 || sharedGem[0].TelegramID != 202 {
		t.Fatalf("ListSharedLLMKeys после unshare = %+v, err=%v", sharedGem, err)
	}

	if err := st.DeleteLLMKey(101, "gemini"); err != nil {
		t.Fatalf("DeleteLLMKey: %v", err)
	}
	got, err = st.GetLLMKey(101, "gemini")
	if err != nil || got != nil {
		t.Fatalf("ключ не удалился: %+v, err=%v", got, err)
	}
}

func TestLLMUsageAtomicAndCooldown(t *testing.T) {
	st := openTestLLMStore(t)
	day := "2026-11-14"

	if err := st.AddLLMUsage(101, "gemini", day, 1, 120, 45); err != nil {
		t.Fatalf("AddLLMUsage 1: %v", err)
	}
	if err := st.AddLLMUsage(101, "gemini", day, 2, 300, 80); err != nil {
		t.Fatalf("AddLLMUsage 2: %v", err)
	}
	if err := st.AddLLMUsage(101, "opencode", day, 1, 10, 5); err != nil {
		t.Fatalf("AddLLMUsage opencode: %v", err)
	}

	until := time.Date(2026, 11, 14, 15, 30, 0, 0, time.UTC)
	if err := st.SetLLMCooldown(101, "gemini", day, until); err != nil {
		t.Fatalf("SetLLMCooldown: %v", err)
	}
	// Повторный AddLLMUsage не должен затирать cooldown_until.
	if err := st.AddLLMUsage(101, "gemini", day, 1, 50, 15); err != nil {
		t.Fatalf("AddLLMUsage 3: %v", err)
	}

	u, err := st.GetLLMUsage(101, "gemini", day)
	if err != nil || u == nil {
		t.Fatalf("GetLLMUsage = (%v, %v)", u, err)
	}
	if u.Requests != 4 || u.InputTokens != 470 || u.OutputTokens != 140 {
		t.Fatalf("счётчики = %+v, ждали 4 / 470 / 140", u)
	}
	if u.CooldownUntil == nil || !u.CooldownUntil.Equal(until) {
		t.Fatalf("CooldownUntil = %v, ждали %v", u.CooldownUntil, until)
	}

	// SetLLMCooldown работает и если до него не было запросов за день.
	if err := st.SetLLMCooldown(202, "opencode", day, until); err != nil {
		t.Fatalf("SetLLMCooldown 202: %v", err)
	}
	u2, err := st.GetLLMUsage(202, "opencode", day)
	if err != nil || u2 == nil || u2.Requests != 0 || u2.CooldownUntil == nil || !u2.CooldownUntil.Equal(until) {
		t.Fatalf("GetLLMUsage 202 = %+v, err=%v", u2, err)
	}

	list, err := st.ListLLMUsageByUser(101, day)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListLLMUsageByUser = %d, err=%v", len(list), err)
	}
}
