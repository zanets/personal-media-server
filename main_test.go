package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (*App, string) {
	t.Helper()
	root := t.TempDir()
	content := bytes.Repeat([]byte("0123456789"), 100)
	if err := os.WriteFile(filepath.Join(root, "旅程.mp4"), content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "private.txt"), []byte("not media"), 0600); err != nil {
		t.Fatal(err)
	}
	dbpath := filepath.Join(t.TempDir(), "library.sqlite3")
	db, err := openDB(dbpath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a := &App{db: db, roots: []string{root}}
	count, err := a.scan()
	if err != nil || count != 1 {
		t.Fatalf("scan: %d %v", count, err)
	}
	return a, dbpath
}
func request(a *App, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	return w
}
func library(t *testing.T, a *App) []Media {
	t.Helper()
	w := request(a, "GET", "/api/media", "", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result []Media
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func TestScanAndPersistence(t *testing.T) {
	a, path := fixture(t)
	rows := library(t, a)
	if len(rows) != 1 || rows[0].Title != "旅程" || rows[0].Kind != "video" || rows[0].Tags == nil || rows[0].Notes == nil {
		t.Fatalf("unexpected library: %+v", rows)
	}
	w := request(a, "POST", "/api/update", `{"id":1,"rating":5,"tags":["旅行","旅行",""],"notes":[{"time":12.5,"text":"重點"}],"position":42}`, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := a.scan(); err != nil {
		t.Fatal(err)
	}
	a.db.Close()
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	a.db = db
	t.Cleanup(func() { db.Close() })
	rows = library(t, a)
	m := rows[0]
	if m.Rating != 5 || m.Position != 42 || len(m.Tags) != 1 || m.Tags[0] != "旅行" || m.Notes[0].Time != 12.5 {
		t.Fatalf("metadata not persisted: %+v", m)
	}
}
func TestStreamRanges(t *testing.T) {
	a, _ := fixture(t)
	tests := []struct {
		header       string
		code         int
		length       int
		contentRange string
	}{{"", 200, 1000, ""}, {"bytes=100-199", 206, 100, "bytes 100-199/1000"}, {"bytes=-10", 206, 10, "bytes 990-999/1000"}, {"bytes=995-", 206, 5, "bytes 995-999/1000"}, {"bytes=9999-", 416, -1, "bytes */1000"}}
	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			w := request(a, "GET", "/stream/1", "", map[string]string{"Range": tt.header})
			if w.Code != tt.code {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if tt.length >= 0 && w.Body.Len() != tt.length {
				t.Fatalf("length %d", w.Body.Len())
			}
			if w.Header().Get("Content-Range") != tt.contentRange {
				t.Fatal(w.Header())
			}
			if tt.header == "bytes=100-199" && w.Body.String() != strings.Repeat("0123456789", 10) {
				t.Fatal("wrong range bytes")
			}
		})
	}
	w := request(a, "HEAD", "/stream/1", "", nil)
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "1000" {
		t.Fatal("HEAD failed", w.Code, w.Header())
	}
	os.Remove(filepath.Join(a.roots[0], "旅程.mp4"))
	if request(a, "GET", "/stream/1", "", nil).Code != 404 {
		t.Fatal("missing file not handled")
	}
}
func TestValidationAndBoundary(t *testing.T) {
	a, _ := fixture(t)
	for _, body := range []string{`{"id":1,"rating":6}`, `{"id":1,"rating":1.5}`, `{"id":1,"position":-1}`, `{"id":1,"notes":[{"time":-1,"text":"bad"}]}`, `{"id":1,"notes":[{"time":0,"text":""}]}`, `{"id":1,"path":"/etc/passwd"}`, `{"id":1}`, `{"id":1,"rating":5} {}`} {
		if w := request(a, "POST", "/api/update", body, nil); w.Code != 400 {
			t.Fatalf("accepted bad body %s: %d", body, w.Code)
		}
	}
	if request(a, "POST", "/api/update", `{"id":999,"rating":5}`, nil).Code != 404 {
		t.Fatal("missing id")
	}
	if request(a, "POST", "/api/update", `{"id":1,"rating":5}`, map[string]string{"Origin": "http://evil.example"}).Code != 403 {
		t.Fatal("cross-origin accepted")
	}
	if request(a, "POST", "/api/scan", "{}", map[string]string{"Sec-Fetch-Site": "cross-site"}).Code != 403 {
		t.Fatal("cross-site scan accepted")
	}
	if request(a, "POST", "/api/update", `{"id":1,"rating":4}`, map[string]string{"Origin": "http://example.com"}).Code != 200 {
		t.Fatal("same-origin write rejected")
	}
	outside := filepath.Join(t.TempDir(), "secret.mp4")
	os.WriteFile(outside, []byte("outside"), 0600)
	if err := os.Symlink(outside, filepath.Join(a.roots[0], "escape.mp4")); err != nil {
		t.Skip(err)
	}
	if n, err := a.scan(); err != nil || n != 1 {
		t.Fatalf("scan escaped root: %d %v", n, err)
	}
	a.db.Exec("UPDATE media SET path=? WHERE id=1", outside)
	if request(a, "GET", "/stream/1", "", nil).Code != 403 {
		t.Fatal("stream escaped root")
	}
}
func TestEmbeddedAssets(t *testing.T) {
	a, _ := fixture(t)
	for _, path := range []string{"/", "/app.js", "/style.css", "/vendor/react.production.min.js", "/vendor/react-dom.production.min.js"} {
		w := request(a, "GET", path, "", nil)
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Fatalf("asset %s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/main.go", "/library.sqlite3", "/vendor/react.LICENSE", "/does-not-exist"} {
		if request(a, "GET", path, "", nil).Code != 404 {
			t.Fatal("unexpected static access", path)
		}
	}
}
func TestHTTPIntegration(t *testing.T) {
	a, _ := fixture(t)
	server := httptest.NewServer(a.handler())
	defer server.Close()
	r, err := http.NewRequest("GET", server.URL+"/stream/1", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Range", "bytes=50-59")
	res, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != 206 || string(b) != "0123456789" {
		t.Fatalf("wire stream: %d %q %v", res.StatusCode, b, err)
	}
}
