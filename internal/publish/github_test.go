package publish

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBlobSHA(t *testing.T) {
	if got := BlobSHA([]byte("hello")); got != "b6fc4c620b67d95f953a5c1c1230aaab5db5a1b0" {
		t.Fatalf("blob sha = %q", got)
	}
}

func TestContentPath(t *testing.T) {
	got := contentPath("Теория информации/лекция-1/notes.pdf")
	if got != "%D0%A2%D0%B5%D0%BE%D1%80%D0%B8%D1%8F%20%D0%B8%D0%BD%D1%84%D0%BE%D1%80%D0%BC%D0%B0%D1%86%D0%B8%D0%B8/%D0%BB%D0%B5%D0%BA%D1%86%D0%B8%D1%8F-1/notes.pdf" {
		t.Fatalf("contentPath = %q", got)
	}
}

func TestDisabledWithoutToken(t *testing.T) {
	g := &GitHub{Owner: "o", Repo: "r", Branch: "main"}
	if g.Enabled() {
		t.Fatal("must be disabled without token")
	}
	if _, err := g.Upsert(context.Background(), "a.txt", []byte("x"), ""); err == nil {
		t.Fatal("expected error when disabled")
	}
}

func newServer(t *testing.T, existingSHA string, wantPutSHA string, putCode int) (*GitHub, *httptest.Server) {
	t.Helper()
	var gotPut map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		switch r.Method {
		case http.MethodGet:
			if existingSHA == "" {
				http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": existingSHA})
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &gotPut)
			if wantPutSHA != "" && gotPut["sha"] != wantPutSHA {
				t.Errorf("put sha = %q want %q", gotPut["sha"], wantPutSHA)
			}
			if dec, err := base64.StdEncoding.DecodeString(gotPut["content"]); err != nil || string(dec) != "data" {
				t.Errorf("put content = %q err=%v", gotPut["content"], err)
			}
			if gotPut["branch"] != "main" {
				t.Errorf("put branch = %q", gotPut["branch"])
			}
			w.WriteHeader(putCode)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return &GitHub{Token: "tok", Owner: "o", Repo: "r", Branch: "main", API: srv.URL}, srv
}

func TestUpsertCreate(t *testing.T) {
	g, _ := newServer(t, "", "", http.StatusCreated)
	changed, err := g.Upsert(context.Background(), "Матан/лекция-1/transcript.txt", []byte("data"), "add")
	if err != nil || !changed {
		t.Fatalf("create: changed=%v err=%v", changed, err)
	}
}

func TestUpsertUpdateKeepsSHA(t *testing.T) {
	other := BlobSHA([]byte("old"))
	g, _ := newServer(t, other, other, http.StatusOK)
	changed, err := g.Upsert(context.Background(), "a.txt", []byte("data"), "update")
	if err != nil || !changed {
		t.Fatalf("update: changed=%v err=%v", changed, err)
	}
}

func TestUpsertSkipsIdentical(t *testing.T) {
	same := BlobSHA([]byte("data"))
	g, srv := newServer(t, same, "", http.StatusOK)
	var puts int
	srv.Config.Handler = counting(t, srv.Config.Handler, &puts)
	changed, err := g.Upsert(context.Background(), "a.txt", []byte("data"), "noop")
	if err != nil || changed {
		t.Fatalf("skip: changed=%v err=%v", changed, err)
	}
	if puts != 0 {
		t.Fatalf("expected no PUT, got %d", puts)
	}
}

func counting(t *testing.T, next http.Handler, puts *int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			*puts++
		}
		next.ServeHTTP(w, r)
	})
}
