package model

import "time"

// WebSession — вход в панель по куке. Сам токен не хранится, только его sha256.
type WebSession struct {
	TokenHash  string    `json:"-"` // hex(sha256(токен))
	TelegramID int64     `json:"telegram_id"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}
