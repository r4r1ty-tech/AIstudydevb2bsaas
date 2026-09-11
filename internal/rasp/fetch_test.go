package rasp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchOmitsWeekAndSetsHeaders(t *testing.T) {
	var hits []string
	var ua, accept, lang string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.RequestURI())
		ua = r.Header.Get("User-Agent")
		accept = r.Header.Get("Accept")
		lang = r.Header.Get("Accept-Language")
		http.SetCookie(w, &http.Cookie{Name: "ssau", Value: "1", Path: "/"})
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "<html>ok</html>")
	}))
	t.Cleanup(srv.Close)

	prevP, prevW := primaryOrigin, wwwOrigin
	primaryOrigin, wwwOrigin = srv.URL, srv.URL+"-unused"
	t.Cleanup(func() {
		primaryOrigin, wwwOrigin = prevP, prevW
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body, status, err := Fetch(ctx, 531023229, 0)
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	if !strings.Contains(string(body), "ok") {
		t.Fatalf("body = %q", body)
	}
	if !strings.Contains(ua, "Chrome/122") {
		t.Errorf("UA = %q", ua)
	}
	if !strings.Contains(accept, "text/html") {
		t.Errorf("Accept = %q", accept)
	}
	if lang != "ru-RU,ru;q=0.9" {
		t.Errorf("Accept-Language = %q", lang)
	}
	if len(hits) < 2 {
		t.Fatalf("hits = %v, want warm + target", hits)
	}
	if hits[0] != "/rasp" {
		t.Errorf("warm = %s", hits[0])
	}
	if hits[len(hits)-1] != "/rasp?groupId=531023229" {
		t.Errorf("target = %s", hits[len(hits)-1])
	}
}

func TestFetchSelectedWeekAnd403Fallback(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, "no")
	}))
	t.Cleanup(primary.Close)

	var wwwHits []string
	www := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wwwHits = append(wwwHits, r.URL.RequestURI())
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "www-ok")
	}))
	t.Cleanup(www.Close)

	prevP, prevW := primaryOrigin, wwwOrigin
	primaryOrigin, wwwOrigin = primary.URL, www.URL
	t.Cleanup(func() {
		primaryOrigin, wwwOrigin = prevP, prevW
	})

	body, status, err := Fetch(context.Background(), 42, 7)
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	if string(body) != "www-ok" {
		t.Fatalf("body = %q", body)
	}
	found := false
	for _, h := range wwwHits {
		if h == "/rasp?groupId=42&selectedWeek=7" {
			found = true
		}
	}
	if !found {
		t.Fatalf("www hits = %v", wwwHits)
	}
}

func TestFetchReturnsNon200WithoutError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "boom")
	}))
	t.Cleanup(srv.Close)
	prevP, prevW := primaryOrigin, wwwOrigin
	primaryOrigin, wwwOrigin = srv.URL, srv.URL
	t.Cleanup(func() {
		primaryOrigin, wwwOrigin = prevP, prevW
	})

	body, status, err := Fetch(context.Background(), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if status != 500 {
		t.Fatalf("status = %d", status)
	}
	if string(body) != "boom" {
		t.Fatalf("body = %q", body)
	}
}
