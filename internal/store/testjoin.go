package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/logx"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
)

const testJoinKey = "test_join"

func (s *Store) GetTestJoin() (model.TestJoin, error) {
	logx.Debugf("store", "GetTestJoin: enter")
	var out model.TestJoin
	if s == nil {
		logx.Debugf("store", "GetTestJoin: nil store")
		return out, nil
	}
	raw, ok, err := s.GetSetting(testJoinKey)
	if err != nil {
		logx.Errorf("store", "GetTestJoin: get setting: %v", err)
		return out, err
	}
	if !ok || strings.TrimSpace(raw) == "" {
		out.Status = model.TestIdle
		out.Name = model.TestGuestName
		logx.Debugf("store", "GetTestJoin: idle default")
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		logx.Errorf("store", "GetTestJoin: unmarshal: %v", err)
		return model.TestJoin{}, fmt.Errorf("store: test join: %w", err)
	}
	if out.Status == "" {
		out.Status = model.TestIdle
	}
	if strings.TrimSpace(out.Name) == "" {
		out.Name = model.TestGuestName
	}
	logx.Debugf("store", "GetTestJoin: out status=%s want=%s name=%s", out.Status, out.Want, out.Name)
	return out, nil
}

func (s *Store) PutTestJoin(j model.TestJoin) error {
	logx.Debugf("store", "PutTestJoin: enter status=%s want=%s name=%s url_empty=%v", j.Status, j.Want, j.Name, strings.TrimSpace(j.URL) == "")
	if s == nil {
		logx.Errorf("store", "PutTestJoin: nil store")
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
		logx.Errorf("store", "PutTestJoin: marshal: %v", err)
		return fmt.Errorf("store: test join marshal: %w", err)
	}
	if err := s.SetSetting(testJoinKey, string(b)); err != nil {
		logx.Errorf("store", "PutTestJoin: set setting: %v", err)
		return err
	}
	logx.Debugf("store", "PutTestJoin: done status=%s want=%s", j.Status, j.Want)
	return nil
}
