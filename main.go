package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

//go:embed templates/*.html
var templateFiles embed.FS

type config struct {
	DB, Username, Password, PostmarkKey, Host, Listen string
	Port, MaxAttempts                                 int
}

const defaultPort = 54283

type app struct {
	db   *sql.DB
	cfg  config
	tpl  *template.Template
	http *http.Client
}

type apiKeyView struct {
	ID                    int64
	Name, Prefix, Created string
}
type pageData struct {
	Error, Message, NewKey, CSRF string
	Keys                         []apiKeyView
}

func main() {
	envPath := flag.String("env", "", "path to the required environment file")
	initEnvPath := flag.String("init-env", "", "create a starter environment file and exit")
	flag.Parse()
	if *initEnvPath != "" {
		if err := initEnv(*initEnvPath); err != nil {
			log.Fatal(err)
		}
		log.Printf("created %s", *initEnvPath)
		return
	}
	if *envPath == "" {
		log.Fatal("usage: egate --env /path/to/egate.env (or egate --init-env .env)")
	}
	if err := loadEnv(*envPath); err != nil {
		log.Fatal(err)
	}
	cfg, err := readConfig()
	if err != nil {
		log.Fatal(err)
	}

	db, err := sql.Open("sqlite", cfg.DB)
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err := migrate(db, cfg); err != nil {
		log.Fatal(err)
	}

	tpl := template.Must(template.ParseFS(templateFiles, "templates/*.html"))
	a := &app{db: db, cfg: cfg, tpl: tpl, http: &http.Client{Timeout: 15 * time.Second}}
	srv := &http.Server{Addr: cfg.Listen, Handler: a.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("egate listening on %s", cfg.Listen)
	log.Fatal(srv.ListenAndServe())
}

const starterEnv = `EGATE_DB=./data/egate.sqlite3
EGATE_ADMIN_USERNAME=admin
EGATE_ADMIN_PASSWORD=replace-with-a-long-random-password
EGATE_POSTMARK_API_KEY=invalid-replace-with-postmark-server-token
EGATE_PORT=54283
EGATE_LOGIN_MAX_ATTEMPTS=5
`

func initEnv(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("environment file path is required")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create env file: %w", err)
	}
	if _, err := io.WriteString(f, starterEnv); err != nil {
		f.Close()
		return fmt.Errorf("write env file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close env file: %w", err)
	}
	return nil
}

func loadEnv(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read env file: %w", err)
	}
	for n, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return fmt.Errorf("env file line %d is invalid", n+1)
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if len(v) >= 2 && ((v[0] == '\'' && v[len(v)-1] == '\'') || (v[0] == '"' && v[len(v)-1] == '"')) {
			v = v[1 : len(v)-1]
		}
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return nil
}

func readConfig() (config, error) {
	c := config{DB: os.Getenv("EGATE_DB"), Username: os.Getenv("EGATE_ADMIN_USERNAME"), Password: os.Getenv("EGATE_ADMIN_PASSWORD"), PostmarkKey: os.Getenv("EGATE_POSTMARK_API_KEY"), Host: "127.0.0.1", Port: defaultPort, MaxAttempts: 5}
	for k, v := range map[string]string{"EGATE_DB": c.DB, "EGATE_ADMIN_USERNAME": c.Username, "EGATE_ADMIN_PASSWORD": c.Password, "EGATE_POSTMARK_API_KEY": c.PostmarkKey} {
		if v == "" {
			return c, fmt.Errorf("%s is required", k)
		}
	}
	if v := os.Getenv("EGATE_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return c, errors.New("EGATE_PORT must be an integer from 1 to 65535")
		}
		c.Port = n
	}
	if v := os.Getenv("EGATE_HOST"); v != "" {
		c.Host = v
	}
	c.Listen = net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	if v := os.Getenv("EGATE_LOGIN_MAX_ATTEMPTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return c, errors.New("EGATE_LOGIN_MAX_ATTEMPTS must be a positive integer")
		}
		c.MaxAttempts = n
	}
	return c, nil
}

func migrate(db *sql.DB, cfg config) error {
	_, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS admin (id INTEGER PRIMARY KEY CHECK(id=1), username TEXT NOT NULL, password_hash TEXT NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS login_failures (id INTEGER PRIMARY KEY, ip TEXT NOT NULL, attempted_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS login_failures_ip_time ON login_failures(ip, attempted_at);
CREATE TABLE IF NOT EXISTS login_bans (ip TEXT PRIMARY KEY, banned_until INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS sessions (token_hash TEXT PRIMARY KEY, csrf_token TEXT NOT NULL, expires_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS sessions_expiry ON sessions(expires_at);
CREATE TABLE IF NOT EXISTS api_keys (id INTEGER PRIMARY KEY, name TEXT NOT NULL, key_hash TEXT NOT NULL UNIQUE, prefix TEXT NOT NULL, created_at INTEGER NOT NULL, revoked_at INTEGER);
CREATE INDEX IF NOT EXISTS api_keys_hash ON api_keys(key_hash);`)
	if err != nil {
		return err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(cfg.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO admin(id,username,password_hash,updated_at) VALUES(1,?,?,?) ON CONFLICT(id) DO UPDATE SET username=excluded.username,password_hash=excluded.password_hash,updated_at=excluded.updated_at`, cfg.Username, h, time.Now().Unix())
	return err
}

func (a *app) routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	})
	m.HandleFunc("GET /login", a.loginPage)
	m.HandleFunc("POST /login", a.login)
	m.HandleFunc("POST /logout", a.requireAdmin(a.logout))
	m.HandleFunc("GET /", a.requireAdmin(a.dashboard))
	m.HandleFunc("POST /keys", a.requireAdmin(a.createKey))
	m.HandleFunc("POST /keys/{id}/revoke", a.requireAdmin(a.revokeKey))
	m.HandleFunc("POST /v1/email", a.requireAPIKey(a.sendEmail))
	return securityHeaders(m)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
func hash(s string) string { x := sha256.Sum256([]byte(s)); return hex.EncodeToString(x[:]) }
func token(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func (a *app) cleanup() {
	now := time.Now().Unix()
	a.db.Exec(`DELETE FROM login_failures WHERE attempted_at < ?`, now-24*3600)
	a.db.Exec(`DELETE FROM login_bans WHERE banned_until < ?`, now)
	a.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now)
}

func (a *app) loginPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, "login.html", pageData{})
}
func (a *app) login(w http.ResponseWriter, r *http.Request) {
	a.cleanup()
	ip := clientIP(r)
	now := time.Now()
	var bannedUntil int64
	if err := a.db.QueryRow(`SELECT banned_until FROM login_bans WHERE ip=?`, ip).Scan(&bannedUntil); err == nil && bannedUntil > now.Unix() {
		w.Header().Set("Retry-After", strconv.FormatInt(bannedUntil-now.Unix(), 10))
		http.Error(w, "too many failed attempts; try again later", http.StatusTooManyRequests)
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour).Unix()
	var failures int
	a.db.QueryRow(`SELECT COUNT(*) FROM login_failures WHERE ip=? AND attempted_at>=?`, ip, cutoff).Scan(&failures)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	var user, passhash string
	err := a.db.QueryRow(`SELECT username,password_hash FROM admin WHERE id=1`).Scan(&user, &passhash)
	if err != nil || r.FormValue("username") != user || bcrypt.CompareHashAndPassword([]byte(passhash), []byte(r.FormValue("password"))) != nil {
		a.db.Exec(`INSERT INTO login_failures(ip,attempted_at) VALUES(?,?)`, ip, now.Unix())
		failures++
		if failures >= a.cfg.MaxAttempts {
			until := now.Add(24 * time.Hour).Unix()
			a.db.Exec(`INSERT INTO login_bans(ip,banned_until) VALUES(?,?) ON CONFLICT(ip) DO UPDATE SET banned_until=excluded.banned_until`, ip, until)
			w.Header().Set("Retry-After", "86400")
			http.Error(w, "too many failed attempts; banned for 24 hours", http.StatusTooManyRequests)
			return
		}
		a.renderStatus(w, "login.html", pageData{Error: "Invalid username or password."}, http.StatusUnauthorized)
		return
	}
	a.db.Exec(`DELETE FROM login_failures WHERE ip=?`, ip)
	raw, csrf := token(32), token(24)
	a.db.Exec(`INSERT INTO sessions(token_hash,csrf_token,expires_at) VALUES(?,?,?)`, hash(raw), csrf, time.Now().Add(12*time.Hour).Unix())
	http.SetCookie(w, &http.Cookie{Name: "egate_session", Value: raw, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *app) session(r *http.Request) (string, bool) {
	c, e := r.Cookie("egate_session")
	if e != nil {
		return "", false
	}
	var csrf string
	e = a.db.QueryRow(`SELECT csrf_token FROM sessions WHERE token_hash=? AND expires_at>?`, hash(c.Value), time.Now().Unix()).Scan(&csrf)
	return csrf, e == nil
}
func (a *app) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		csrf, ok := a.session(r)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if r.Method == "POST" && r.FormValue("csrf") != csrf {
			http.Error(w, "invalid CSRF token", 403)
			return
		}
		next(w, r)
	}
}
func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("egate_session"); e == nil {
		a.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, hash(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "egate_session", Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/login", 303)
}
func (a *app) dashboard(w http.ResponseWriter, r *http.Request) {
	csrf, _ := a.session(r)
	rows, err := a.db.Query(`SELECT id,name,prefix,datetime(created_at,'unixepoch') FROM api_keys WHERE revoked_at IS NULL ORDER BY id DESC`)
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer rows.Close()
	d := pageData{CSRF: csrf}
	for rows.Next() {
		var k apiKeyView
		rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.Created)
		d.Keys = append(d.Keys, k)
	}
	a.render(w, "dashboard.html", d)
}
func (a *app) createKey(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name is required", 400)
		return
	}
	raw := "eg_" + token(32)
	prefix := raw[:11]
	_, err := a.db.Exec(`INSERT INTO api_keys(name,key_hash,prefix,created_at) VALUES(?,?,?,?)`, name, hash(raw), prefix, time.Now().Unix())
	if err != nil {
		http.Error(w, "database error", 500)
		return
	}
	csrf, _ := a.session(r)
	d := pageData{CSRF: csrf, NewKey: raw, Message: "Copy this key now. It cannot be shown again."}
	rows, _ := a.db.Query(`SELECT id,name,prefix,datetime(created_at,'unixepoch') FROM api_keys WHERE revoked_at IS NULL ORDER BY id DESC`)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var k apiKeyView
			rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.Created)
			d.Keys = append(d.Keys, k)
		}
	}
	a.render(w, "dashboard.html", d)
}
func (a *app) revokeKey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad key id", 400)
		return
	}
	a.db.Exec(`UPDATE api_keys SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, time.Now().Unix(), id)
	http.Redirect(w, r, "/", 303)
}

func (a *app) requireAPIKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v := r.Header.Get("Authorization")
		raw, ok := strings.CutPrefix(v, "Bearer ")
		if !ok {
			http.Error(w, "missing bearer token", 401)
			return
		}
		var one int
		err := a.db.QueryRow(`SELECT 1 FROM api_keys WHERE key_hash=? AND revoked_at IS NULL`, hash(raw)).Scan(&one)
		if err != nil {
			http.Error(w, "invalid API key", 401)
			return
		}
		next(w, r)
	}
}

type emailRequest struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Subject  string `json:"subject"`
	TextBody string `json:"text_body"`
	HTMLBody string `json:"html_body"`
	ReplyTo  string `json:"reply_to,omitempty"`
}

func (a *app) sendEmail(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var in emailRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	if in.From == "" || in.To == "" || in.Subject == "" || (in.TextBody == "" && in.HTMLBody == "") {
		http.Error(w, "from, to, subject, and a body are required", 400)
		return
	}
	payload := map[string]string{"From": in.From, "To": in.To, "Subject": in.Subject, "TextBody": in.TextBody, "HtmlBody": in.HTMLBody, "ReplyTo": in.ReplyTo}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(r.Context(), "POST", "https://api.postmarkapp.com/email", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Postmark-Server-Token", a.cfg.PostmarkKey)
	resp, err := a.http.Do(req)
	if err != nil {
		http.Error(w, "email provider unavailable", 502)
		return
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(out)
}
func (a *app) render(w http.ResponseWriter, name string, d pageData) { a.renderStatus(w, name, d, 200) }
func (a *app) renderStatus(w http.ResponseWriter, name string, d pageData, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := a.tpl.ExecuteTemplate(w, name, d); err != nil {
		log.Printf("template: %v", err)
	}
}
