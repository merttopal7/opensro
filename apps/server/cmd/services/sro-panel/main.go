/*
===========================================================================

main.go - OpenSRO Customer & Shard Management Panel (sro-panel)

Provides a self-service web dashboard and JSON API where players and shard
owners can register accounts, provision their own Silkroad shards, assign
GMs, toggle Check (maintenance) mode, configure live server max level,
inspect online players, and perform live player interventions.

===========================================================================
*/
package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
	"opensro.online/server/internal/cluster/shard"
	"opensro.online/server/internal/config"
	"opensro.online/server/internal/security/auth"
)

const (
	defaultPanelAddr  = "127.0.0.1:8795"
	sessionCookieName = "sro_panel_session"
	maxJSONBodyBytes  = 64 << 10
)

/*
================
panelServer
================
*/
type panelServer struct {
	moduleRoot    string
	catalogPath   string
	stateDir      string
	accounts      *auth.Accounts
	cookieKey     []byte
	httpClient    *http.Client
	deployMu      sync.Mutex
	deployRunning atomic.Int32
	catalogMu     sync.Mutex
}

/*
================
main
================
*/
func main() {
	moduleRoot, err := config.FindModuleRoot()
	if err != nil {
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			fmt.Fprintf(os.Stderr, "sro-panel: module root: %v\n", err)
			os.Exit(1)
		}
		moduleRoot = cwd
	}
	catalogPath := strings.TrimSpace(os.Getenv("SRO_SHARD_CATALOG_PATH"))
	if catalogPath == "" {
		catalogPath = filepath.Join(moduleRoot, shard.DefaultCatalogPath)
	}
	stateDir := strings.TrimSpace(os.Getenv("SRO_STATE_DIR"))
	if stateDir == "" {
		stateDir = filepath.Join(moduleRoot, ".state", "cluster")
	}

	var accounts *auth.Accounts
	pgDSN := strings.TrimSpace(os.Getenv("SRO_AGENT_POSTGRES_DSN"))
	if pgDSN == "" {
		if raw, readErr := os.ReadFile(filepath.Join(stateDir, "postgres-dsn.txt")); readErr == nil {
			pgDSN = strings.TrimSpace(string(raw))
		}
	}
	if pgDSN != "" {
		accounts, err = auth.OpenAccountsPostgres(pgDSN)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sro-panel: open postgres accounts: %v\n", err)
			os.Exit(1)
		}
	} else {
		dbPath := filepath.Join(stateDir, "agent", "accounts.db")
		accounts, err = auth.OpenAccounts(dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sro-panel: open sqlite accounts: %v\n", err)
			os.Exit(1)
		}
	}
	defer func() {
		_ = accounts.Close()
	}()

	cookieKey, err := loadOrCreateCookieKey(filepath.Join(stateDir, "panel-cookie.key"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "sro-panel: cookie key: %v\n", err)
		os.Exit(1)
	}

	srv := &panelServer{
		moduleRoot:  moduleRoot,
		catalogPath: catalogPath,
		stateDir:    stateDir,
		accounts:    accounts,
		cookieKey:   cookieKey,
		httpClient: &http.Client{
			Timeout: 8 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	srv.syncCatalogToDatabase()

	addr := strings.TrimSpace(os.Getenv("SRO_PANEL_ADDR"))
	if addr == "" {
		addr = defaultPanelAddr
	}

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	fmt.Printf("sro-panel listening on http://%s\n", addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "sro-panel: %v\n", err)
		os.Exit(1)
	}
}

/*
================
routes
================
*/
func (s *panelServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/auth/register", s.handleRegister)
	mux.HandleFunc("/api/auth/login", s.handleLogin)
	mux.HandleFunc("/api/auth/logout", s.handleLogout)
	mux.HandleFunc("/api/auth/me", s.handleMe)
	mux.HandleFunc("/api/me", s.handleMe)
	mux.HandleFunc("/api/shards", s.handleShards)
	mux.HandleFunc("/api/shards/", s.handleShardSubroutes)
	mux.HandleFunc("/", s.handleUI)
	return mux
}

/*
================
handleRegister
================
*/
func (s *panelServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req struct {
		Username string `json:"username"`
		UserID   string `json:"userId"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Geçersiz istek formatı.")
		return
	}
	username := strings.TrimSpace(req.UserID)
	if username == "" {
		username = strings.TrimSpace(req.Username)
	}
	info, err := s.accounts.Create(r.Context(), username, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrAccountExists) {
			writeError(w, http.StatusConflict, "Bu kullanıcı adı zaten kayıtlı.")
			return
		}
		if errors.Is(err, auth.ErrPasswordInvalid) {
			writeError(w, http.StatusBadRequest, "Şifre en az 8, en fazla 72 karakter olmalıdır.")
			return
		}
		writeError(w, http.StatusBadRequest, "Kullanıcı adı geçersiz (yalnızca harf, rakam, tire ve alt çizgi kullanılabilir).")
		return
	}
	token := s.signSession(info.ID, time.Now().Add(7*24*time.Hour))
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"accountId": info.ID,
		"userId":    info.ID,
		"token":     token,
	})
}

/*
================
handleLogin
================
*/
func (s *panelServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req struct {
		Username string `json:"username"`
		UserID   string `json:"userId"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Geçersiz istek formatı.")
		return
	}
	username := strings.TrimSpace(req.UserID)
	if username == "" {
		username = strings.TrimSpace(req.Username)
	}
	canonical, hash, ok := s.accounts.Credential(username)
	if !ok {
		writeError(w, http.StatusUnauthorized, "Kullanıcı adı veya şifre hatalı.")
		return
	}
	if err := bcrypt.CompareHashAndPassword(hash, []byte(req.Password)); err != nil {
		writeError(w, http.StatusUnauthorized, "Kullanıcı adı veya şifre hatalı.")
		return
	}
	token := s.signSession(canonical, time.Now().Add(7*24*time.Hour))
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"accountId": canonical,
		"userId":    canonical,
		"token":     token,
	})
}

/*
================
handleLogout
================
*/
func (s *panelServer) handleLogout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

/*
================
handleMe
================
*/
func (s *panelServer) handleMe(w http.ResponseWriter, r *http.Request) {
	accountID, ok := s.authenticatedAccount(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "Oturum açmanız gerekiyor.")
		return
	}
	info, err := s.accounts.Lookup(accountID)
	var silkOwn int64
	if err == nil {
		silkOwn = info.Silk.Own
	}
	isGlobalAdmin := s.accounts.IsGM(accountID)
	shards := s.buildShardViews(accountID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"accountId": accountID,
		"silkOwn":   silkOwn,
		"isAdmin":   isGlobalAdmin,
		"user": map[string]any{
			"userId":        accountID,
			"silkOwn":       silkOwn,
			"isGlobalAdmin": isGlobalAdmin,
		},
		"shards": shards,
	})
}

/*
================
authenticatedAccount
================
*/
func (s *panelServer) authenticatedAccount(r *http.Request) (string, bool) {
	var raw string
	if authHeader := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		raw = strings.TrimSpace(authHeader[7:])
	}
	if raw == "" {
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			raw = strings.TrimSpace(cookie.Value)
		}
	}
	if raw == "" {
		return "", false
	}
	accountID, ok := s.verifySession(raw, time.Now())
	if !ok {
		return "", false
	}
	canonical, _, exists := s.accounts.Credential(accountID)
	if !exists {
		return "", false
	}
	return canonical, true
}

/*
================
signSession
================
*/
func (s *panelServer) signSession(accountID string, expiresAt time.Time) string {
	payload := fmt.Sprintf("%s|%d", accountID, expiresAt.Unix())
	encoded := base64.RawURLEncoding.EncodeToString([]byte(payload))
	mac := hmac.New(sha256.New, s.cookieKey)
	_, _ = mac.Write([]byte(encoded))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + sig
}

/*
================
verifySession
================
*/
func (s *panelServer) verifySession(token string, now time.Time) (string, bool) {
	encoded, sig, ok := strings.Cut(token, ".")
	if !ok || encoded == "" || sig == "" {
		return "", false
	}
	mac := hmac.New(sha256.New, s.cookieKey)
	_, _ = mac.Write([]byte(encoded))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", false
	}
	accountID, expStr, ok := strings.Cut(string(raw), "|")
	if !ok || accountID == "" {
		return "", false
	}
	var expUnix int64
	if _, err := fmt.Sscanf(expStr, "%d", &expUnix); err != nil {
		return "", false
	}
	if now.Unix() > expUnix {
		return "", false
	}
	return accountID, true
}

/*
================
setSessionCookie
================
*/
func (s *panelServer) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   7 * 24 * 3600,
		SameSite: http.SameSiteLaxMode,
	})
}

/*
================
loadOrCreateCookieKey
================
*/
func loadOrCreateCookieKey(path string) ([]byte, error) {
	if raw, err := os.ReadFile(path); err == nil {
		decoded, decErr := hex.DecodeString(strings.TrimSpace(string(raw)))
		if decErr == nil && len(decoded) >= 32 {
			return decoded, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

/*
================
decodeJSON
================
*/
func decodeJSON(r *http.Request, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBodyBytes))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dst)
}

/*
================
writeJSON
================
*/
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

/*
================
writeError
================
*/
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"ok":      false,
		"message": message,
	})
}
