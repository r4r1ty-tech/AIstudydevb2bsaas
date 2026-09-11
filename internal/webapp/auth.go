package webapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	ErrMissingInitData = errors.New("missing init data")
	ErrInvalidInitData = errors.New("invalid init data")
	ErrExpiredInitData = errors.New("init data expired")
	ErrNotAdmin        = errors.New("not admin")
)

const initDataMaxAge = 24 * time.Hour

// ValidateInitData checks Telegram Mini App initData HMAC and that the user is admin.
func ValidateInitData(initData, botToken string, now time.Time, adminID int64) (userID int64, err error) {
	initData = strings.TrimSpace(initData)
	if initData == "" {
		return 0, ErrMissingInitData
	}
	if botToken == "" {
		return 0, ErrInvalidInitData
	}

	vals, err := url.ParseQuery(initData)
	if err != nil {
		return 0, ErrInvalidInitData
	}

	hash := vals.Get("hash")
	if hash == "" {
		return 0, ErrInvalidInitData
	}
	wantMAC, err := hex.DecodeString(hash)
	if err != nil || len(wantMAC) != sha256.Size {
		return 0, ErrInvalidInitData
	}

	keys := make([]string, 0, len(vals))
	for k := range vals {
		if k == "hash" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+vals.Get(k))
	}
	dataCheckString := strings.Join(parts, "\n")

	// Telegram: secret_key = HMAC_SHA256(key="WebAppData", msg=bot_token)
	secret := hmacSHA256([]byte("WebAppData"), []byte(botToken))
	gotMAC := hmacSHA256(secret, []byte(dataCheckString))
	if !hmac.Equal(wantMAC, gotMAC) {
		return 0, ErrInvalidInitData
	}

	authDateStr := vals.Get("auth_date")
	authUnix, err := strconv.ParseInt(authDateStr, 10, 64)
	if err != nil || authUnix <= 0 {
		return 0, ErrInvalidInitData
	}
	authAt := time.Unix(authUnix, 0)
	if now.Sub(authAt) > initDataMaxAge {
		return 0, ErrExpiredInitData
	}

	userRaw := vals.Get("user")
	if userRaw == "" {
		return 0, ErrInvalidInitData
	}
	var tgUser struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(userRaw), &tgUser); err != nil || tgUser.ID == 0 {
		return 0, ErrInvalidInitData
	}
	if tgUser.ID != adminID {
		return tgUser.ID, ErrNotAdmin
	}
	return tgUser.ID, nil
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write(data)
	return m.Sum(nil)
}

func initDataFromRequest(rHeaderGet func(string) string) string {
	if v := strings.TrimSpace(rHeaderGet("X-Telegram-Init-Data")); v != "" {
		return v
	}
	auth := strings.TrimSpace(rHeaderGet("Authorization"))
	if len(auth) >= 4 && strings.EqualFold(auth[:4], "tma ") {
		return strings.TrimSpace(auth[4:])
	}
	return ""
}
