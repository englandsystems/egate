package main

import (
	"database/sql"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInitEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := initEnv(path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "EGATE_POSTMARK_API_KEY=invalid-") {
		t.Fatalf("starter env has no invalid Postmark key: %s", b)
	}
	if err := initEnv(path); err == nil {
		t.Fatal("initEnv overwrote an existing file")
	}
}

func testApp(t *testing.T, max int) *app {
	t.Helper()
	db, err := sqlOpenForTest(t)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config{Username: "admin", Password: "correct horse battery staple", PostmarkKey: "test", MaxAttempts: max}
	if err := migrate(db, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &app{db: db, cfg: cfg, tpl: templateForTest(t), http: &http.Client{Timeout: time.Second}}
}

func sqlOpenForTest(t *testing.T) (*sql.DB, error) {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/test.sqlite3")
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

func templateForTest(t *testing.T) *template.Template {
	t.Helper()
	return template.Must(template.ParseFS(templateFiles, "templates/*.html"))
}

func TestLoginCreatesSession(t *testing.T) {
	a := testApp(t, 5)
	form := url.Values{"username": {"admin"}, "password": {"correct horse battery staple"}}
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) == 0 || w.Result().Cookies()[0].Name != "egate_session" {
		t.Fatal("session cookie not set")
	}
}

func TestLoginBanAtThreshold(t *testing.T) {
	a := testApp(t, 2)
	for i, want := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
		form := url.Values{"username": {"admin"}, "password": {"wrong"}}
		r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		r.RemoteAddr = "203.0.113.9:1234"
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		a.routes().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("attempt %d: status = %d, want %d", i+1, w.Code, want)
		}
	}
}

func TestAPIKeyMiddleware(t *testing.T) {
	a := testApp(t, 5)
	raw := "eg_test_key"
	if _, err := a.db.Exec(`INSERT INTO api_keys(name,key_hash,prefix,created_at) VALUES(?,?,?,?)`, "test", hash(raw), "eg_test", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	hit := false
	h := a.requireAPIKey(func(w http.ResponseWriter, r *http.Request) { hit = true; w.WriteHeader(204) })
	r := httptest.NewRequest(http.MethodPost, "/v1/email", nil)
	r.Header.Set("Authorization", "Bearer "+raw)
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != 204 || !hit {
		t.Fatalf("status=%d hit=%v", w.Code, hit)
	}
}
