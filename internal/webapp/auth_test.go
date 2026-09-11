package webapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	testBotToken = "123456:TEST_TOKEN_FOR_HMAC"
	testAdminID  = int64(1074442235)
)

func signInitData(botToken string, fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		if k == "hash" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+fields[k])
	}
	dataCheck := strings.Join(parts, "\n")

	mac1 := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = mac1.Write([]byte(botToken))
	secret := mac1.Sum(nil)

	mac2 := hmac.New(sha256.New, secret)
	_, _ = mac2.Write([]byte(dataCheck))
	fields["hash"] = hex.EncodeToString(mac2.Sum(nil))

	q := url.Values{}
	for k, v := range fields {
		q.Set(k, v)
	}
	return q.Encode()
}

func TestValidateInitData_OK(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	raw := signInitData(testBotToken, map[string]string{
		"auth_date": strconv.FormatInt(now.Unix(), 10),
		"query_id":  "AAEtest",
		"user":      fmt.Sprintf(`{"id":%d,"first_name":"Admin","username":"boss"}`, testAdminID),
	})
	uid, err := ValidateInitData(raw, testBotToken, now, testAdminID)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if uid != testAdminID {
		t.Fatalf("user id = %d, want %d", uid, testAdminID)
	}
}

func TestValidateInitData_WrongHash(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	raw := signInitData(testBotToken, map[string]string{
		"auth_date": strconv.FormatInt(now.Unix(), 10),
		"user":      fmt.Sprintf(`{"id":%d,"first_name":"Admin"}`, testAdminID),
	})
	vals, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	vals.Set("hash", strings.Repeat("ab", 32))
	_, err = ValidateInitData(vals.Encode(), testBotToken, now, testAdminID)
	if err == nil {
		t.Fatal("expected error for wrong hash")
	}
	if !errors.Is(err, ErrInvalidInitData) {
		t.Fatalf("err = %v, want ErrInvalidInitData", err)
	}
}

func TestValidateInitData_WrongUser(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	other := testAdminID + 1
	raw := signInitData(testBotToken, map[string]string{
		"auth_date": strconv.FormatInt(now.Unix(), 10),
		"user":      fmt.Sprintf(`{"id":%d,"first_name":"Kent"}`, other),
	})
	uid, err := ValidateInitData(raw, testBotToken, now, testAdminID)
	if err == nil {
		t.Fatal("expected error for non-admin user")
	}
	if !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("err = %v, want ErrNotAdmin", err)
	}
	if uid != other {
		t.Fatalf("user id = %d, want parsed %d", uid, other)
	}
}
