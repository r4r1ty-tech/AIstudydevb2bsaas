package webapp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/config"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/model"
	"github.com/r4r1ty-tech/AIstudydevb2bsaas/internal/store"
)

func TestTestJoinAPI(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := New(&config.Config{
		ListenAddr:    config.DefaultListen,
		Timezone:      config.DefaultTimezone,
		PanelPassword: "panel-secret",
		AdminID:       config.DefaultAdminID,
	}, st, nil, "")

	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(body))
		req.Header.Set("X-Panel-Password", "panel-secret")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}

	rec := post(`{"want":"dummy"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no url: %d %s", rec.Code, rec.Body.Bytes())
	}

	rec = post(`{"url":"https://evil.example/b/x","want":"dummy"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad host: %d %s", rec.Code, rec.Body.Bytes())
	}

	rec = post(`{"url":"https://bbb.ssau.ru/b/abc","want":"dummy"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("dummy: %d %s", rec.Code, rec.Body.Bytes())
	}
	var got model.TestJoin
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://bbb.ssau.ru/b/abc" || got.Want != model.TestWantDummy || got.Status != model.TestJoining {
		t.Fatalf("dummy body: %+v", got)
	}

	rec = post(`{"want":"leave"}`)
	if rec.Code != http.StatusOK {
		body, _ := io.ReadAll(rec.Body)
		t.Fatalf("leave: %d %s", rec.Code, body)
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Want != model.TestWantOff || got.Status != model.TestIdle {
		t.Fatalf("left: %+v", got)
	}
}
