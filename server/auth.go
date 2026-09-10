package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const loginLifetime = 30 * 24 * time.Hour

var errSuspended = errors.New("This account is suspended.")

type GoogleIdentity struct {
	Subject, Email string
	Verified       bool
	HostedDomain   string
}
type googleAuth struct {
	origin         *url.URL
	config         oauth2.Config
	bootstrapEmail string
	verify         func(context.Context, string, string) (GoogleIdentity, error)
}

func (s *Station) configureGoogle() error {
	base := strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/")
	file := os.Getenv("GOOGLE_CLIENT_FILE")
	id, secret := os.Getenv("GOOGLE_CLIENT_ID"), os.Getenv("GOOGLE_CLIENT_SECRET")
	var redirects []string
	if file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return errors.New("Cannot read GOOGLE_CLIENT_FILE")
		}
		var client struct {
			Web struct {
				ClientID     string   `json:"client_id"`
				ClientSecret string   `json:"client_secret"`
				Redirects    []string `json:"redirect_uris"`
			}
		}
		if json.Unmarshal(raw, &client) != nil {
			return errors.New("Invalid Google client JSON")
		}
		id, secret, redirects = client.Web.ClientID, client.Web.ClientSecret, client.Web.Redirects
	}
	if id == "" && secret == "" && file == "" {
		return nil
	}
	origin, err := url.Parse(base)
	if err != nil || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return errors.New("Set PUBLIC_BASE_URL to the public origin, without a path")
	}
	local := origin.Hostname() == "localhost" || origin.Hostname() == "127.0.0.1" || origin.Hostname() == "::1"
	if origin.Scheme != "https" && !(local && origin.Scheme == "http") {
		return errors.New("PUBLIC_BASE_URL must use HTTPS, except on localhost")
	}
	if id == "" || secret == "" {
		return errors.New("Google client ID and secret are both required")
	}
	callback := base + "/auth/google/callback"
	if file != "" {
		found := false
		for _, redirect := range redirects {
			if redirect == callback {
				found = true
			}
		}
		if !found {
			return errors.New("Google client JSON does not authorize PUBLIC_BASE_URL/auth/google/callback")
		}
	}
	client := &http.Client{Timeout: 10 * time.Second}
	// Fixed Google endpoints; the JSON file supplies credentials, never an arbitrary issuer.
	// RemoteKeySet caches Google's signing keys and refreshes on key rotation.
	keyContext := oidc.ClientContext(context.Background(), client)
	verifier := oidc.NewVerifier("https://accounts.google.com", oidc.NewRemoteKeySet(keyContext, "https://www.googleapis.com/oauth2/v3/certs"), &oidc.Config{ClientID: id, SupportedSigningAlgs: []string{"RS256"}})
	s.auth = &googleAuth{origin: origin, bootstrapEmail: strings.ToLower(strings.TrimSpace(os.Getenv("BOOTSTRAP_ADMIN_EMAIL"))), config: oauth2.Config{
		ClientID: id, ClientSecret: secret, RedirectURL: callback, Scopes: []string{oidc.ScopeOpenID, "email", "profile"},
		Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token", AuthStyle: oauth2.AuthStyleInParams},
	}}
	s.auth.verify = googleIdentityVerifier(verifier)
	return nil
}

func (s *Station) initializeAccounts() error {
	// Additive migrations preserve existing guest sessions, ballots, and account fixtures.
	columns := []struct{ table, name, definition string }{
		{"accounts", "email", "TEXT NOT NULL DEFAULT ''"}, {"accounts", "role", "TEXT NOT NULL DEFAULT 'member' CHECK(role IN ('member','admin'))"},
		{"accounts", "suspended", "INTEGER NOT NULL DEFAULT 0"}, {"accounts", "created", "INTEGER NOT NULL DEFAULT 0"},
		{"account_sessions", "expires", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, c := range columns {
		rows, err := s.db.Query("PRAGMA table_info(" + c.table + ")")
		if err != nil {
			return err
		}
		found := false
		for rows.Next() {
			var cid, notNull, pk int
			var name, kind string
			var def any
			if err = rows.Scan(&cid, &name, &kind, &notNull, &def, &pk); err != nil {
				break
			}
			if name == c.name {
				found = true
			}
		}
		rowErr := rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if rowErr != nil {
			return rowErr
		}
		if !found {
			if _, err = s.db.Exec("ALTER TABLE " + c.table + " ADD COLUMN " + c.name + " " + c.definition); err != nil {
				return err
			}
		}
	}
	_, err := s.db.Exec(`
 CREATE TABLE IF NOT EXISTS oauth_attempts(state_hash TEXT PRIMARY KEY, session TEXT NOT NULL REFERENCES sessions(id), nonce TEXT NOT NULL, verifier TEXT NOT NULL, expires INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS bootstrap_admin(id INTEGER PRIMARY KEY CHECK(id=1), account TEXT NOT NULL REFERENCES accounts(id));
 CREATE TABLE IF NOT EXISTS moderation_events(poll TEXT PRIMARY KEY REFERENCES polls(id), hidden INTEGER NOT NULL DEFAULT 0, nominated INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS admin_audit(id INTEGER PRIMARY KEY, actor TEXT NOT NULL REFERENCES accounts(id), action TEXT NOT NULL, target TEXT NOT NULL, ts INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS site_settings(id INTEGER PRIMARY KEY CHECK(id=1), submissions_paused INTEGER NOT NULL DEFAULT 0);
 INSERT OR IGNORE INTO site_settings(id) VALUES(1);
 `)
	return err
}

func hashState(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}
func (s *Station) secureCookies(r *http.Request) bool {
	return r.TLS != nil || (s.auth != nil && s.auth.origin.Scheme == "https")
}
func (s *Station) setSessionCookie(w http.ResponseWriter, r *http.Request, v Session) {
	http.SetCookie(w, &http.Cookie{Name: "station_guest", Value: v.ID, Path: "/", HttpOnly: true, Secure: s.secureCookies(r), SameSite: http.SameSiteLaxMode, MaxAge: 365 * 24 * 3600})
}
func (s *Station) oauthCookie(w http.ResponseWriter, r *http.Request, state string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: "station_oauth", Value: state, Path: "/auth/google", HttpOnly: true, Secure: s.secureCookies(r), SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

func (s *Station) googleStart(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		http.Error(w, "Google sign-in is not configured.", 503)
		return
	}
	v, err := s.getSession(w, r, true)
	if err != nil {
		http.Error(w, "Could not start sign-in", 500)
		return
	}
	state, nonce, pkce := randomID(), randomID(), oauth2.GenerateVerifier()
	s.mu.Lock()
	tx, err := s.db.Begin()
	if err == nil {
		defer tx.Rollback()
		_, err = tx.Exec("DELETE FROM oauth_attempts WHERE session=? OR expires<=?", v.ID, time.Now().UnixMilli())
		if err == nil {
			_, err = tx.Exec("INSERT INTO oauth_attempts VALUES(?,?,?,?,?)", hashState(state), v.ID, nonce, pkce, time.Now().Add(10*time.Minute).UnixMilli())
		}
		if err == nil {
			err = tx.Commit()
		}
	}
	s.mu.Unlock()
	if err != nil {
		http.Error(w, "Could not start sign-in", 500)
		return
	}
	s.setSessionCookie(w, r, v)
	s.oauthCookie(w, r, state, 600)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, s.auth.config.AuthCodeURL(state, oauth2.S256ChallengeOption(pkce), oidc.Nonce(nonce), oauth2.SetAuthURLParam("prompt", "select_account")), http.StatusSeeOther)
}

func (s *Station) googleCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if s.auth == nil {
		http.Error(w, "Google sign-in is not configured.", 503)
		return
	}
	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie("station_oauth")
	if err != nil || len(state) != 48 || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		http.Error(w, "Sign-in expired or did not start in this browser. Please try again.", 400)
		return
	}
	guest, err := s.getSession(w, r, false)
	if err != nil {
		http.Error(w, "Sign-in browser session expired. Please try again.", 400)
		return
	}
	var nonce, pkce string
	// Consume state atomically before contacting Google: callbacks cannot be replayed.
	err = s.db.QueryRow("DELETE FROM oauth_attempts WHERE state_hash=? AND session=? AND expires>? RETURNING nonce,verifier", hashState(state), guest.ID, time.Now().UnixMilli()).Scan(&nonce, &pkce)
	s.oauthCookie(w, r, "", -1)
	if err != nil {
		http.Error(w, "Sign-in expired or was already used. Please try again.", 400)
		return
	}
	if r.URL.Query().Get("error") != "" {
		http.Redirect(w, r, "/account?login=cancelled", http.StatusSeeOther)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 4096 {
		http.Error(w, "Missing Google authorization code", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	token, err := s.auth.config.Exchange(ctx, code, oauth2.VerifierOption(pkce))
	if err != nil {
		http.Error(w, "Could not complete Google sign-in. Please try again.", 502)
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "Google did not return an identity token", 502)
		return
	}
	identity, err := s.auth.verify(ctx, raw, nonce)
	if err != nil {
		http.Error(w, "Could not verify your Google identity. Please try again.", 403)
		return
	}
	v, err := s.finishLogin(guest, identity, time.Now().UnixMilli())
	if errors.Is(err, errSuspended) {
		http.Error(w, err.Error(), 403)
		return
	}
	if err != nil {
		http.Error(w, "Could not create your account session", 500)
		return
	}
	s.setSessionCookie(w, r, v)
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

func (s *Station) finishLogin(guest Session, identity GoogleIdentity, now int64) (Session, error) {
	if !identity.Verified || identity.Subject == "" || identity.Email == "" {
		return Session{}, errors.New("Verified Google identity required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRow("SELECT id FROM accounts WHERE provider='google' AND subject=?", identity.Subject).Scan(&id)
	if err == sql.ErrNoRows {
		id = randomID()
		_, err = tx.Exec("INSERT INTO accounts(id,provider,subject,email,created) VALUES(?,'google',?,?,?)", id, identity.Subject, identity.Email, now)
	}
	if err != nil {
		return Session{}, err
	}
	var suspended bool
	if err = tx.QueryRow("SELECT suspended FROM accounts WHERE id=?", id).Scan(&suspended); err != nil {
		return Session{}, err
	}
	if suspended {
		return Session{}, errSuspended
	}
	if _, err = tx.Exec("UPDATE accounts SET email=? WHERE id=?", identity.Email, id); err != nil {
		return Session{}, err
	}
	// Google is authoritative for Gmail addresses and verified Workspace domains.
	authoritative := strings.HasSuffix(strings.ToLower(identity.Email), "@gmail.com") || identity.HostedDomain != ""
	if s.auth != nil && s.auth.bootstrapEmail != "" && authoritative && strings.EqualFold(identity.Email, s.auth.bootstrapEmail) {
		result, e := tx.Exec("INSERT OR IGNORE INTO bootstrap_admin(id,account) VALUES(1,?)", id)
		if e != nil {
			return Session{}, e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return Session{}, e
		}
		if n == 1 {
			if _, err = tx.Exec("UPDATE accounts SET role='admin' WHERE id=?", id); err != nil {
				return Session{}, err
			}
			if _, err = tx.Exec("INSERT INTO admin_audit(actor,action,target,ts) VALUES(?,'bootstrap-admin',?,?)", id, id, now); err != nil {
				return Session{}, err
			}
		}
	}
	// Rotate the session at authentication, retaining existing browser ballot limits.
	v := Session{ID: randomID()}
	result, err := tx.Exec("INSERT INTO sessions(id,handle,tokens,updated) SELECT ?,handle,tokens,updated FROM sessions WHERE id=?", v.ID, guest.ID)
	if err != nil {
		return Session{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Session{}, err
	}
	if n != 1 {
		return Session{}, errors.New("Browser session no longer exists")
	}
	if _, err = tx.Exec("INSERT INTO ballots(poll,session) SELECT poll,? FROM ballots WHERE session=?", v.ID, guest.ID); err != nil {
		return Session{}, err
	}
	if _, err = tx.Exec("INSERT INTO actions(poll,session,request) SELECT poll,?,request FROM actions WHERE session=?", v.ID, guest.ID); err != nil {
		return Session{}, err
	}
	if _, err = tx.Exec("UPDATE discussion SET session=? WHERE session=?", v.ID, guest.ID); err != nil {
		return Session{}, err
	}
	if _, err = tx.Exec("INSERT OR IGNORE INTO participation_prompts(session,dismissed) SELECT ?,1 FROM participation_prompts WHERE session=?", v.ID, guest.ID); err != nil {
		return Session{}, err
	}
	if _, err = tx.Exec("DELETE FROM account_sessions WHERE session=?", guest.ID); err != nil {
		return Session{}, err
	}
	if _, err = tx.Exec("INSERT INTO account_sessions(session,account,expires) VALUES(?,?,?)", v.ID, id, now+loginLifetime.Milliseconds()); err != nil {
		return Session{}, err
	}
	if err = tx.Commit(); err != nil {
		return Session{}, err
	}
	return s.session(v.ID)
}

type queryRower interface{ QueryRow(string, ...any) *sql.Row }

func sessionFrom(db queryRower, id string, now int64) (Session, error) {
	var v Session
	err := db.QueryRow(`SELECT s.id,s.handle,COALESCE(a.id,''),COALESCE(a.email,''),COALESCE(a.role,''),COALESCE(a.suspended,0)
 FROM sessions s LEFT JOIN account_sessions link ON link.session=s.id AND link.expires>?
 LEFT JOIN accounts a ON a.id=link.account WHERE s.id=?`, now, id).Scan(&v.ID, &v.Handle, &v.AccountID, &v.Email, &v.Role, &v.Suspended)
	return v, err
}

func (s *Station) logout(w http.ResponseWriter, r *http.Request) {
	v, err := s.getSession(w, r, false)
	if err != nil {
		http.Redirect(w, r, "/account", 303)
		return
	}
	if r.FormValue("all") == "true" && v.AccountID != "" {
		_, err = s.db.Exec("DELETE FROM account_sessions WHERE account=?", v.AccountID)
	} else {
		_, err = s.db.Exec("DELETE FROM account_sessions WHERE session=?", v.ID)
	}
	if err != nil {
		http.Error(w, "Could not sign out", 500)
		return
	}
	if _, err = s.db.Exec("DELETE FROM oauth_attempts WHERE session=?", v.ID); err != nil {
		http.Error(w, "Could not cancel pending sign-in", 500)
		return
	}
	s.oauthCookie(w, r, "", -1)
	// Keep the now-anonymous browser session and its existing open-event ballots.
	http.Redirect(w, r, "/account", 303)
}

func googleIdentityVerifier(verifier *oidc.IDTokenVerifier) func(context.Context, string, string) (GoogleIdentity, error) {
	return func(ctx context.Context, raw, nonce string) (GoogleIdentity, error) {
		token, err := verifier.Verify(ctx, raw)
		if err != nil {
			return GoogleIdentity{}, errors.New("Invalid Google identity")
		}
		if subtle.ConstantTimeCompare([]byte(token.Nonce), []byte(nonce)) != 1 {
			return GoogleIdentity{}, errors.New("Google login nonce did not match")
		}
		var claims struct {
			Email        string `json:"email"`
			Verified     bool   `json:"email_verified"`
			HostedDomain string `json:"hd"`
		}
		if token.Claims(&claims) != nil || token.Subject == "" || !claims.Verified {
			return GoogleIdentity{}, errors.New("Google did not provide a verified email address")
		}
		return GoogleIdentity{Subject: token.Subject, Email: claims.Email, Verified: claims.Verified, HostedDomain: claims.HostedDomain}, nil
	}
}
