package main

import (
	"bytes"
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

func TestHelpIsClearAndActionable(t *testing.T) {
	var out bytes.Buffer
	printHelp(&out)
	help := out.String()
	for _, want := range []string{
		"egate --init-env .env",
		"egate teach ./EGATE.md",
		"egate --env ./.env",
		"EGATE_POSTMARK_API_KEY",
		"http://127.0.0.1:54283/login",
		"Authorization: Bearer eg_YOUR_KEY",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("help is missing %q", want)
		}
	}
}

func TestTeachWritesAgentGuide(t *testing.T) {
	path := filepath.Join(t.TempDir(), "EGATE.md")
	if err := teach(path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	guide := string(b)
	for _, want := range []string{
		"github.com/englandsystems/egate/sdk",
		"EGATE_HOST",
		"EGATE_API_KEY",
		"sdk.NewClient",
		"client.SendEmail",
		"errors.As(err, &apiErr)",
		"POST {EGATE_HOST}/v1/email",
	} {
		if !strings.Contains(guide, want) {
			t.Errorf("guide is missing %q", want)
		}
	}

	if err := os.WriteFile(path, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := teach(path); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != sdkGuide {
		t.Fatal("teach did not replace an existing guide")
	}
}

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
	if !strings.Contains(string(b), "EGATE_PORT=54283") {
		t.Fatalf("starter env has no standard egate port: %s", b)
	}
	if err := initEnv(path); err == nil {
		t.Fatal("initEnv overwrote an existing file")
	}
}

func TestReadConfigPort(t *testing.T) {
	t.Setenv("EGATE_DB", "test.sqlite3")
	t.Setenv("EGATE_ADMIN_USERNAME", "admin")
	t.Setenv("EGATE_ADMIN_PASSWORD", "password")
	t.Setenv("EGATE_POSTMARK_API_KEY", "test")
	t.Setenv("EGATE_PORT", "54321")

	cfg, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 54321 || cfg.Listen != "127.0.0.1:54321" {
		t.Fatalf("port=%d listen=%q", cfg.Port, cfg.Listen)
	}
}

func TestReadConfigHost(t *testing.T) {
	t.Setenv("EGATE_DB", "test.sqlite3")
	t.Setenv("EGATE_ADMIN_USERNAME", "admin")
	t.Setenv("EGATE_ADMIN_PASSWORD", "password")
	t.Setenv("EGATE_POSTMARK_API_KEY", "test")
	t.Setenv("EGATE_HOST", "0.0.0.0")

	cfg, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "0.0.0.0:54283" {
		t.Fatalf("listen=%q", cfg.Listen)
	}
}

func TestReadConfigRejectsInvalidPort(t *testing.T) {
	t.Setenv("EGATE_DB", "test.sqlite3")
	t.Setenv("EGATE_ADMIN_USERNAME", "admin")
	t.Setenv("EGATE_ADMIN_PASSWORD", "password")
	t.Setenv("EGATE_POSTMARK_API_KEY", "test")
	t.Setenv("EGATE_PORT", "65536")

	if _, err := readConfig(); err == nil {
		t.Fatal("readConfig accepted an invalid port")
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
	if location := w.Header().Get("Location"); location != "/admin" {
		t.Fatalf("location = %q, want /admin", location)
	}
}

func TestPublicDocumentationAtRoot(t *testing.T) {
	a := testApp(t, 5)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "API documentation") || !strings.Contains(body, "POST</span> /v1/email") {
		t.Fatalf("root did not render API documentation: %s", body)
	}
	if strings.Contains(body, `href="/login"`) {
		t.Fatal("public documentation advertises the private login route")
	}
}

func TestAdminRequiresLogin(t *testing.T) {
	a := testApp(t, 5)
	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Fatalf("status = %d, location = %q", w.Code, w.Header().Get("Location"))
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
