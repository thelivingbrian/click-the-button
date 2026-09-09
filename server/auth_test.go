package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func signingKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
func signedIdentity(t *testing.T, key *rsa.PrivateKey, changes map[string]any) string {
	t.Helper()
	claims := map[string]any{"iss": "https://accounts.google.com", "aud": "test-client", "sub": "owner-subject", "email": "owner@gmail.com", "email_verified": true, "nonce": "nonce", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix()}
	for k, v := range changes {
		claims[k] = v
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	object, err := signer.Sign(raw)
	if err != nil {
		t.Fatal(err)
	}
	token, err := object.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}
func testGoogle(t *testing.T, s *Station, key *rsa.PrivateKey) {
	t.Helper()
	origin, _ := url.Parse("http://example.com")
	verifier := oidc.NewVerifier("https://accounts.google.com", &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, &oidc.Config{ClientID: "test-client", SupportedSigningAlgs: []string{"RS256"}})
	s.auth = &googleAuth{origin: origin, bootstrapEmail: "owner@gmail.com", config: oauth2.Config{ClientID: "test-client", ClientSecret: "test-secret", RedirectURL: "http://example.com/auth/google/callback", Scopes: []string{"openid", "email", "profile"}, Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token", AuthStyle: oauth2.AuthStyleInParams}}, verify: googleIdentityVerifier(verifier)}
}
func requestWithSession(method, path, body string, v Session) *http.Request {
	r := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(body))
	r.Header.Set("Origin", "http://example.com")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "station_guest", Value: v.ID})
	return r
}

func TestGoogleIdentityCryptographicValidation(t *testing.T) {
	key := signingKey(t)
	s := testStation(t)
	testGoogle(t, s, key)
	identity, err := s.auth.verify(context.Background(), signedIdentity(t, key, nil), "nonce")
	if err != nil || identity.Subject != "owner-subject" || !identity.Verified {
		t.Fatal(identity, err)
	}
	for name, changes := range map[string]map[string]any{
		"issuer": {"iss": "https://attacker.example"}, "audience": {"aud": "other-client"}, "expired": {"exp": time.Now().Add(-time.Hour).Unix()}, "nonce": {"nonce": "wrong"}, "unverified": {"email_verified": false}, "missing-subject": {"sub": ""},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.auth.verify(context.Background(), signedIdentity(t, key, changes), "nonce"); err == nil {
				t.Fatal("invalid token accepted")
			}
		})
	}
	if _, err := s.auth.verify(context.Background(), signedIdentity(t, signingKey(t), nil), "nonce"); err == nil {
		t.Fatal("wrong signature accepted")
	}
}

func TestGoogleCallbackExchangePKCESessionRotationAndReplay(t *testing.T) {
	s := testStation(t)
	key := signingKey(t)
	testGoogle(t, s, key)
	guest := testGuest(t, s)
	board, _ := s.board(guest)
	if err := s.click(board.Poll.ID, guest, 0, "before-login", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	handler := s.routes()
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, requestWithSession("POST", "/auth/google", "", guest))
	if rr.Code != 303 {
		t.Fatal(rr.Code, rr.Body.String())
	}
	location, err := url.Parse(rr.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := location.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" || q.Get("scope") != "openid email profile" {
		t.Fatal("incomplete authorization request")
	}
	state := q.Get("state")
	var cookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == "station_oauth" {
			cookie = c
		}
		if c.Name == "station_guest" && c.SameSite != http.SameSiteLaxMode {
			t.Fatal("callback session cookie must allow top-level redirect")
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("missing safe callback cookie")
	}
	var pkce string
	if err = s.db.QueryRow("SELECT verifier FROM oauth_attempts WHERE state_hash=?", hashState(state)).Scan(&pkce); err != nil {
		t.Fatal(err)
	}
	exchanges := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		exchanges++
		if r.URL.String() != "https://oauth2.googleapis.com/token" {
			t.Fatal("unexpected destination")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("code_verifier") != pkce || r.FormValue("redirect_uri") != s.auth.config.RedirectURL || r.FormValue("code") != "valid-code" {
			t.Fatal("incorrect token exchange")
		}
		b, _ := json.Marshal(map[string]any{"access_token": "unused-token", "token_type": "Bearer", "id_token": signedIdentity(t, key, map[string]any{"nonce": q.Get("nonce")})})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
	})}
	callback := func(browser Session, stateValue string) *httptest.ResponseRecorder {
		r := requestWithSession("GET", "/auth/google/callback?code=valid-code&state="+stateValue, "", browser)
		r.AddCookie(cookie)
		r = r.WithContext(context.WithValue(r.Context(), oauth2.HTTPClient, client))
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, r)
		return out
	}
	if out := callback(guest, "wrong-state"); out.Code != 400 {
		t.Fatal("wrong state accepted")
	}
	if out := callback(testGuest(t, s), state); out.Code != 400 {
		t.Fatal("callback accepted in another browser")
	}
	if exchanges != 0 {
		t.Fatal("invalid callback contacted Google")
	}
	out := callback(guest, state)
	if out.Code != 303 || out.Header().Get("Location") != "/account" {
		t.Fatal(out.Code, out.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, c := range out.Result().Cookies() {
		if c.Name == "station_guest" {
			sessionCookie = c
		}
	}
	if sessionCookie == nil || sessionCookie.Value == guest.ID {
		t.Fatal("session not rotated")
	}
	member, err := s.session(sessionCookie.Value)
	if err != nil || member.Role != "admin" {
		t.Fatal(member, err)
	}
	if err = s.click(board.Poll.ID, member, 1, "after-login", time.Now().UnixMilli()); !errors.Is(err, errVoted) {
		t.Fatal("login bypassed browser ballot", err)
	}
	before, _ := s.session(guest.ID)
	if before.AccountID != "" {
		t.Fatal("old cookie gained privileges")
	}
	if out := callback(guest, state); out.Code != 400 {
		t.Fatal("callback replay accepted")
	}
	if exchanges != 1 {
		t.Fatal("replayed token exchange")
	}
	admin := httptest.NewRecorder()
	handler.ServeHTTP(admin, requestWithSession("GET", "/admin", "", member))
	if admin.Code != 200 || !strings.Contains(admin.Body.String(), "Administration") {
		t.Fatal(admin.Code, admin.Body.String())
	}
	home := httptest.NewRecorder()
	handler.ServeHTTP(home, requestWithSession("GET", "/", "", member))
	if strings.Contains(home.Body.String(), "owner@gmail.com") {
		t.Fatal("email exposed on public page")
	}
}

func TestAdminBootstrapIsExplicitAndBoundToSubject(t *testing.T) {
	s := testStation(t)
	s.auth = &googleAuth{bootstrapEmail: "owner@gmail.com"}
	now := time.Now().UnixMilli()
	other, err := s.finishLogin(testGuest(t, s), GoogleIdentity{Subject: "first-user", Email: "first@gmail.com", Verified: true}, now)
	if err != nil || other.Role != "member" {
		t.Fatal("first signup became admin", other, err)
	}
	owner, err := s.finishLogin(testGuest(t, s), GoogleIdentity{Subject: "owner", Email: "owner@gmail.com", Verified: true}, now)
	if err != nil || owner.Role != "admin" {
		t.Fatal(owner, err)
	}
	renamed, err := s.finishLogin(owner, GoogleIdentity{Subject: "owner", Email: "new-name@gmail.com", Verified: true}, now)
	if err != nil || renamed.Role != "admin" || renamed.AccountID != owner.AccountID {
		t.Fatal("email change lost identity", renamed, err)
	}
	reused, err := s.finishLogin(testGuest(t, s), GoogleIdentity{Subject: "different-subject", Email: "owner@gmail.com", Verified: true}, now)
	if err != nil || reused.Role != "member" {
		t.Fatal("bootstrap repeated", reused, err)
	}
	if _, err = s.finishLogin(testGuest(t, s), GoogleIdentity{Subject: "unverified", Email: "owner@gmail.com"}, now); err == nil {
		t.Fatal("unverified identity accepted")
	}
}

func TestSessionExpiryLogoutAndSuspension(t *testing.T) {
	s := testStation(t)
	s.auth = &googleAuth{bootstrapEmail: "owner@gmail.com"}
	now := time.Now().UnixMilli()
	admin, _ := s.finishLogin(testGuest(t, s), GoogleIdentity{Subject: "owner", Email: "owner@gmail.com", Verified: true}, now)
	member, _ := s.finishLogin(testGuest(t, s), GoogleIdentity{Subject: "member", Email: "member@gmail.com", Verified: true}, now)
	board, _ := s.board(member)
	candidate, err := s.createEvent(member, "one", "Should the library open later?", "Yes\nNo", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.moderate(admin, "nominate", candidate, now); err != nil {
		t.Fatal(err)
	}
	board, _ = s.board(member)
	if err := s.moderate(admin, "suspend", member.AccountID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.click(board.Poll.ID, member, 0, "suspended", now); !errors.Is(err, errSuspended) {
		t.Fatal(err)
	}
	if err := s.discuss(board.Poll.ID, member, "blocked comment", now); !errors.Is(err, errSuspended) {
		t.Fatal(err)
	}
	if _, err := s.createEvent(member, "one", "Blocked community event", "Yes\nNo", now); !errors.Is(err, errSuspended) {
		t.Fatal(err)
	}
	if _, err := s.finishLogin(testGuest(t, s), GoogleIdentity{Subject: "member", Email: "member@gmail.com", Verified: true}, now); !errors.Is(err, errSuspended) {
		t.Fatal(err)
	}
	if err := s.moderate(admin, "suspend", admin.AccountID, now); err == nil {
		t.Fatal("admin suspended self")
	}
	if err := s.moderate(admin, "restore-account", member.AccountID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.click(board.Next.ID, member, 0, "restored", now); err != nil {
		t.Fatal(err)
	}
	s.db.Exec("UPDATE account_sessions SET expires=? WHERE session=?", now-1, admin.ID)
	expired, _ := s.session(admin.ID)
	if expired.AccountID != "" || expired.Role != "" {
		t.Fatal("expired privileges retained")
	}
	if err := s.moderate(admin, "pause-submissions", "", now); err == nil {
		t.Fatal("expired admin accepted")
	}
	// All sessions for the same identity are revoked by sign out everywhere.
	s.auth = nil
	second, _ := s.finishLogin(testGuest(t, s), GoogleIdentity{Subject: "member", Email: "member@gmail.com", Verified: true}, now)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, requestWithSession("POST", "/logout", "all=true", member))
	if rr.Code != 303 {
		t.Fatal(rr.Code)
	}
	for _, v := range []Session{member, second} {
		got, _ := s.session(v.ID)
		if got.AccountID != "" {
			t.Fatal("logout retained access")
		}
	}
}

func TestOAuthCancellationExpiryAndProductionOrigin(t *testing.T) {
	s := testStation(t)
	key := signingKey(t)
	testGoogle(t, s, key)
	guest := testGuest(t, s)
	state := randomID()
	s.db.Exec("INSERT INTO oauth_attempts VALUES(?,?,?,?,?)", hashState(state), guest.ID, "nonce", "pkce", time.Now().Add(time.Minute).UnixMilli())
	r := requestWithSession("GET", "/auth/google/callback?error=access_denied&state="+state, "", guest)
	r.AddCookie(&http.Cookie{Name: "station_oauth", Value: state})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, r)
	if rr.Code != 303 || rr.Header().Get("Location") != "/account?login=cancelled" {
		t.Fatal(rr.Code)
	}
	s.db.Exec("INSERT INTO oauth_attempts VALUES(?,?,?,?,?)", hashState(state), guest.ID, "nonce", "pkce", time.Now().Add(-time.Minute).UnixMilli())
	rr = httptest.NewRecorder()
	s.routes().ServeHTTP(rr, r)
	if rr.Code != 400 {
		t.Fatal("expired attempt accepted")
	}
	s.auth.origin, _ = url.Parse("https://click-the-button.com")
	rr = httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest("GET", "http://127.0.0.1/account", nil))
	if rr.Code != 308 || rr.Header().Get("Location") != "https://click-the-button.com/account" {
		t.Fatal("bad canonical redirect")
	}
	r = httptest.NewRequest("GET", "http://click-the-button.com/account", nil)
	rr = httptest.NewRecorder()
	s.routes().ServeHTTP(rr, r)
	if rr.Code != 200 {
		t.Fatal(rr.Code)
	}
	for _, cookie := range rr.Result().Cookies() {
		if !cookie.Secure {
			t.Fatal("production cookie insecure behind TLS proxy")
		}
	}
	r = httptest.NewRequest("POST", "http://click-the-button.com/logout", nil)
	r.Header.Set("Origin", "https://click-the-button.com")
	rr = httptest.NewRecorder()
	s.routes().ServeHTTP(rr, r)
	if rr.Code != 303 {
		t.Fatal("proxy origin rejected", rr.Code)
	}
	r.Header.Set("Origin", "https://attacker.example")
	rr = httptest.NewRecorder()
	s.routes().ServeHTTP(rr, r)
	if rr.Code != 403 {
		t.Fatal("cross origin accepted")
	}
}

func TestGoogleConfigRejectsWrongEnvironment(t *testing.T) {
	s := testStation(t)
	file := filepath.Join(t.TempDir(), "credentials.json")
	raw := `{"web":{"client_id":"test-client","client_secret":"never-print-this-secret","redirect_uris":["http://localhost:8080/auth/google/callback"]}}`
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_CLIENT_FILE", file)
	t.Setenv("PUBLIC_BASE_URL", "http://localhost:8080")
	if err := s.configureGoogle(); err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"http://click-the-button.com", "https://click-the-button.com", "http://localhost:8080/path", "http://localhost:8080?x=y"} {
		t.Setenv("PUBLIC_BASE_URL", base)
		err := s.configureGoogle()
		if err == nil {
			t.Fatal("invalid configuration accepted", base)
		}
		if strings.Contains(err.Error(), "never-print") {
			t.Fatal("secret leaked")
		}
	}
}
