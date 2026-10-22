// Package auth protects the user workspaces with revocable server-side sessions.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const sessionLifetime = 14 * 24 * time.Hour

type Auth struct {
	pool      *pgxpool.Pool
	dummyHash []byte
	mu        sync.Mutex
	attempts  []time.Time
}

func New(pool *pgxpool.Pool) *Auth {
	// Unknown users still perform the same expensive password check.
	dummy, err := bcrypt.GenerateFromPassword([]byte(randomToken()), 12)
	if err != nil {
		panic(err)
	}
	return &Auth{pool: pool, dummyHash: dummy}
}

func randomToken() string {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

func tokenHash(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}

func loopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

// Never trust forwarding headers for bootstrap authorization. A public tunnel
// may connect over loopback, but must not claim pre-existing unowned data.
func localSetup(r *http.Request) bool {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(peer) == nil || !net.ParseIP(peer).IsLoopback() || !loopbackHost(r.Host) {
		return false
	}
	for _, header := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		if r.Header.Get(header) != "" {
			return false
		}
	}
	return true
}

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || !loopbackHost(r.Host)
}

func cookieName(r *http.Request) string {
	if secureRequest(r) {
		return "__Host-echo_session"
	}
	return "echo_local_session"
}

// A custom header prevents HTML form CSRF, including on localhost; the Origin
// check also rejects same-site requests from a different origin/port.
func sameOriginWrite(r *http.Request) bool {
	if r.Header.Get("X-Echo-Request") != "1" {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if raw := r.Header.Get("Origin"); raw != "" {
		origin, err := url.Parse(raw)
		if err != nil || origin.Host != r.Host || (origin.Scheme != "https" && (origin.Scheme != "http" || secureRequest(r))) {
			return false
		}
	}
	return true
}

func reply(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, code int, message string) {
	reply(w, code, map[string]string{"error": message})
}

func (a *Auth) internalError(w http.ResponseWriter, err error) {
	log.Printf("authentication storage: %v", err)
	fail(w, http.StatusServiceUnavailable, "Не удалось выполнить вход. Попробуйте позже.")
}

type User struct {
	ID          string `json:"user_id"`
	Username    string `json:"username"`
	LegacyOwner bool   `json:"legacy_owner"`
}
type userKey struct{}

func UserID(ctx context.Context) string {
	user, _ := ctx.Value(userKey{}).(User)
	return user.ID
}
func (a *Auth) authenticated(r *http.Request) (User, error) {
	cookie, err := r.Cookie(cookieName(r))
	if err != nil || len(cookie.Value) != 43 {
		return User{}, nil
	}
	var user User
	err = a.pool.QueryRow(r.Context(), `SELECT u.id::text, u.username, u.legacy_owner FROM auth_user_sessions s
 JOIN auth_users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>NOW()`, tokenHash(cookie.Value)).Scan(&user.ID, &user.Username, &user.LegacyOwner)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, nil
	}
	return user, err
}

// Wrap protects every API path, including future routes and direct audio URLs.
// Only the static app shell, health check and explicit auth endpoints are public.
func (a *Auth) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOriginWrite(r) {
			fail(w, http.StatusForbidden, "Обновите страницу и попробуйте ещё раз.")
			return
		}
		switch r.URL.Path {
		case "/api/auth/status":
			if r.Method == http.MethodGet {
				a.status(w, r)
				return
			}
		case "/api/auth/register":
			if r.Method == http.MethodPost {
				a.credentials(w, r, true)
				return
			}
		case "/api/auth/login":
			if r.Method == http.MethodPost {
				a.credentials(w, r, false)
				return
			}
		case "/api/auth/logout":
			if r.Method == http.MethodPost {
				a.logout(w, r)
				return
			}
		}
		user, err := a.authenticated(r)
		if err != nil {
			a.internalError(w, err)
			return
		}
		if user.ID == "" {
			fail(w, http.StatusUnauthorized, "Войдите, чтобы открыть записи.")
			return
		}
		w.Header().Set("X-Echo-User", user.ID)
		if expected := r.Header.Get("X-Echo-Account"); expected != "" && expected != user.ID {
			fail(w, http.StatusConflict, "Аккаунт изменился. Войдите ещё раз.")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	})
}

func (a *Auth) status(w http.ResponseWriter, r *http.Request) {
	user, err := a.authenticated(r)
	if err != nil {
		a.internalError(w, err)
		return
	}
	reply(w, 200, map[string]any{"authenticated": user.ID != "", "user_id": user.ID, "username": user.Username, "legacy_owner": user.LegacyOwner})
}

// A single, bounded limiter cannot be bypassed by spoofing proxy IP headers.
// The process allows ten login/registration attempts per minute in total.
func (a *Auth) allowAttempt() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	i := 0
	for i < len(a.attempts) && now.Sub(a.attempts[i]) >= time.Minute {
		i++
	}
	a.attempts = a.attempts[i:]
	if len(a.attempts) >= 10 {
		return false
	}
	a.attempts = append(a.attempts, now)
	return true
}

func (a *Auth) credentials(w http.ResponseWriter, r *http.Request, register bool) {
	if !a.allowAttempt() {
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "Слишком много попыток. Подождите минуту.")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	decoder := json.NewDecoder(r.Body)
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		fail(w, 400, "Введите имя и пароль.")
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	if utf8.RuneCountInString(input.Username) < 2 || utf8.RuneCountInString(input.Username) > 50 || len(input.Password) > 72 || (register && utf8.RuneCountInString(input.Password) < 12) {
		fail(w, 400, "Имя должно содержать от 2 до 50 символов. Придумайте пароль от 12 символов, без слишком длинных фраз.")
		return
	}
	var user User
	if register {
		hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), 12)
		if err != nil {
			a.internalError(w, err)
			return
		}
		tx, err := a.pool.Begin(r.Context())
		if err != nil {
			a.internalError(w, err)
			return
		}
		defer tx.Rollback(context.Background())
		user = User{ID: uuid.NewString(), Username: input.Username}
		result, err := tx.Exec(r.Context(), `INSERT INTO auth_users(id,username,password_hash) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, user.ID, user.Username, hash)
		if err != nil {
			a.internalError(w, err)
			return
		}
		if result.RowsAffected() == 0 {
			fail(w, 409, "Это имя уже занято. Выберите другое или войдите.")
			return
		}
		// Only the first direct local account can claim pre-authentication data.
		// Internet registration always starts with an empty library.
		if localSetup(r) {
			if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext(current_schema()),731)`); err != nil {
				a.internalError(w, err)
				return
			}
			if err = tx.QueryRow(r.Context(), `UPDATE auth_users SET legacy_owner=true WHERE id=$1 AND NOT EXISTS(SELECT 1 FROM auth_users WHERE legacy_owner) RETURNING legacy_owner`, user.ID).Scan(&user.LegacyOwner); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				a.internalError(w, err)
				return
			}
			if user.LegacyOwner {
				if _, err = tx.Exec(r.Context(), `UPDATE recordings SET user_id=$1 WHERE user_id IS NULL`, user.ID); err != nil {
					a.internalError(w, err)
					return
				}
			}
		}
		if err = tx.Commit(r.Context()); err != nil {
			a.internalError(w, err)
			return
		}
	} else {
		var hash []byte
		err := a.pool.QueryRow(r.Context(), `SELECT id::text,username,password_hash,legacy_owner FROM auth_users WHERE lower(username)=lower($1)`, input.Username).Scan(&user.ID, &user.Username, &hash, &user.LegacyOwner)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			a.internalError(w, err)
			return
		}
		if hash == nil {
			hash = a.dummyHash
		}
		valid := bcrypt.CompareHashAndPassword(hash, []byte(input.Password)) == nil
		if !valid || user.ID == "" {
			fail(w, 401, "Неверное имя или пароль.")
			return
		}
	}
	if _, err := a.pool.Exec(r.Context(), "DELETE FROM auth_user_sessions WHERE expires_at<=NOW()"); err != nil {
		a.internalError(w, err)
		return
	}
	token := randomToken()
	expires := time.Now().Add(sessionLifetime)
	if _, err := a.pool.Exec(r.Context(), "INSERT INTO auth_user_sessions(token_hash,user_id,expires_at) VALUES ($1,$2,$3)", tokenHash(token), user.ID, expires); err != nil {
		a.internalError(w, err)
		return
	}
	if old, err := r.Cookie(cookieName(r)); err == nil {
		if _, err := a.pool.Exec(r.Context(), "DELETE FROM auth_user_sessions WHERE token_hash=$1", tokenHash(old.Value)); err != nil {
			a.internalError(w, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName(r), Value: token, Path: "/", HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode, MaxAge: int(sessionLifetime.Seconds()), Expires: expires})
	reply(w, 200, map[string]any{"authenticated": true, "user_id": user.ID, "username": user.Username, "legacy_owner": user.LegacyOwner})
}

func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(cookieName(r)); err == nil {
		if _, err := a.pool.Exec(r.Context(), "DELETE FROM auth_user_sessions WHERE token_hash = $1", tokenHash(cookie.Value)); err != nil {
			a.internalError(w, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName(r), Path: "/", HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode, MaxAge: -1})
	reply(w, http.StatusOK, map[string]bool{"authenticated": false})
}
