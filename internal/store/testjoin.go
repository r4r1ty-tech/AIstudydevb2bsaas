package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const testJoinKey = "test_join"

func (s *Store) GetTestJoin() (model.TestJoin, error) {
	var out model.TestJoin
	if s == nil {
		return out, nil
	}
	raw, ok, err := s.GetSetting(testJoinKey)
	if err != nil {
		return out, err
	}
	if !ok || strings.TrimSpace(raw) == "" {
		out.Status = model.TestIdle
		out.Name = model.TestGuestName
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return model.TestJoin{}, fmt.Errorf("store: test join: %w", err)
	}
	if out.Status == "" {
		out.Status = model.TestIdle
	}
	if strings.TrimSpace(out.Name) == "" {
		out.Name = model.TestGuestName
	}
	return out, nil
}

func (s *Store) PutTestJoin(j model.TestJoin) error {
	if s == nil {
		return fmt.Errorf("store: nil")
	}
	if strings.TrimSpace(j.Name) == "" {
		j.Name = model.TestGuestName
	}
	if j.Status == "" {
		j.Status = model.TestIdle
	}
	j.UpdatedAt = time.Now().UTC()
	b, err := json.Marshal(j)
	if err != nil {
		return fmt.Errorf("store: test join marshal: %w", err)
	}
	return s.SetSetting(testJoinKey, string(b))
}

// Админ задаётся в env ADMIN_TELEGRAM_ID, в sqlite не хранится.
