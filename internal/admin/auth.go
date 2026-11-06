package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/big"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
	"golang.org/x/crypto/bcrypt"
)

const (
	cookieName   = "thefeed_admin"
	idleLimit    = 8 * time.Hour
	sessionLimit = 7 * 24 * time.Hour
	codeLife     = 24 * time.Hour
	maxFails     = 5
	lockFor      = 5 * time.Minute
	codeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ" // no 0/O, 1/I/L
)

var usernameRe = regexp.MustCompile(`^[\w.-]{3,32}$`)

// bcryptCost is 12; only the test binary lowers it (export_test.go), since
// cost 12 under -race takes seconds per hash.
var bcryptCost = 12

// Records in the "auth" bucket, under the keys "admin", "setup" and "failures".
type adminRecord struct {
	Username string    `json:"username"`
	Hash     []byte    `json:"hash"`
	Created  time.Time `json:"created"`
}

type setupRecord struct {
	Hash    []byte    `json:"hash"` // SHA-256 of the 8 code characters
	Created time.Time `json:"created"`
}

type failures struct {
	Count       int       `json:"count"`
	LockedUntil time.Time `json:"lockedUntil"`
}

// session is stored under the SHA-256 of its token, never the token itself.
type session struct {
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"lastSeen"`
	Client   string    `json:"client"`
}

func (s session) live(now time.Time) bool {
	return now.Sub(s.LastSeen) < idleLimit && now.Sub(s.Created) < sessionLimit
}

func setupCodePath(dir string) string { return filepath.Join(dir, "setup-code") }

func (a *App) hasAdmin() (bool, error) { return a.auth.Get("admin", &adminRecord{}) }

// ensureSetupCode makes a setup code when no admin exists and there's no
// unexpired code already, so a restart keeps the code the installer printed.
func (a *App) ensureSetupCode() error {
	if ok, err := a.hasAdmin(); err != nil || ok {
		return err
	}
	var s setupRecord
	ok, err := a.auth.Get("setup", &s)
	if err != nil || ok && a.clock.Now().Sub(s.Created) < codeLife {
		return err
	}
	_, err = issueCode(a.auth, a.cfg.AdminDir, a.clock.Now())
	return err
}

// issueCode stores the hash of a new setup code, writes the plain code to a
// root-only file and the journal, and returns it as XXXX-XXXX.
func issueCode(auth *Bucket, dir string, now time.Time) (string, error) {
	raw := make([]byte, 8)
	for i := range raw {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(codeAlphabet))))
		if err != nil {
			return "", err
		}
		raw[i] = codeAlphabet[n.Int64()]
	}
	h := sha256.Sum256(raw)
	if err := auth.Put("setup", setupRecord{Hash: h[:], Created: now}); err != nil {
		return "", err
	}
	code := string(raw[:4]) + "-" + string(raw[4:])
	if err := os.WriteFile(setupCodePath(dir), []byte(code+"\n"), 0o600); err != nil {
		return "", err
	}
	log.Printf("admin: setup code %s (also in %s); it works once, for 24 hours", code, setupCodePath(dir))
	return code, nil
}

// ResetAdmin deletes the admin account and every session, clears the failure
// count, and returns a new setup code. The panel must not be running.
func ResetAdmin(cfg Config) (string, error) {
	if cfg.Clock == nil {
		cfg.Clock = ClockFunc(time.Now)
	}
	if _, err := os.Stat(dbPath(cfg.AdminDir)); err != nil {
		return "", fmt.Errorf("no panel database here (pass the panel's --admin-dir): %w", err)
	}
	db, err := openDB(cfg.AdminDir)
	if err != nil {
		return "", err
	}
	defer db.Close()
	auth, err := newBucket(db, "auth")
	if err != nil {
		return "", err
	}
	sessions, err := newBucket(db, "auth.sessions")
	if err != nil {
		return "", err
	}
	if err := auth.Delete("admin"); err != nil {
		return "", err
	}
	if err := auth.Delete("failures"); err != nil {
		return "", err
	}
	if err := sessions.deleteWhere(func(_, _ []byte) bool { return true }); err != nil {
		return "", err
	}
	return issueCode(auth, cfg.AdminDir, cfg.Clock.Now())
}

// --- failures and lockout ---

// lockedFor says how much longer sign-in stays refused; 0 means it isn't.
func (a *App) lockedFor() time.Duration {
	var f failures
	a.auth.Get("failures", &f)
	return max(f.LockedUntil.Sub(a.clock.Now()), 0)
}

// failed counts one wrong password or code and returns the tries left before
// the lock; 0 means it just locked. Only the count is stored; the source
// address goes to the journal.
func (a *App) failed(r *http.Request, what string) (int, error) {
	var f failures
	a.auth.Get("failures", &f)
	f.Count++
	log.Printf("admin: wrong %s from %s (%d in a row)", what, clientAddr(r), f.Count)
	left := maxFails - f.Count
	if left <= 0 {
		f, left = failures{LockedUntil: a.clock.Now().Add(lockFor)}, 0
		log.Printf("admin: sign-in locked for %s", lockFor)
	}
	return left, a.auth.Put("failures", f)
}

func writeLocked(w http.ResponseWriter, d time.Duration) {
	secs := int(math.Ceil(d.Seconds()))
	mins := int(math.Ceil(d.Minutes()))
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	WriteJSON(w, http.StatusTooManyRequests, errorBody{
		Errors:     []FieldError{{Message: fmt.Sprintf("Too many tries. Wait %d %s, or reset the password on the server.", mins, plural(mins, "minute", "minutes"))}},
		RetryAfter: secs,
	})
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func clientAddr(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// dummyHash is compared against when the username is wrong, so a wrong
// username takes as long as a wrong password.
var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("not the password"), bcryptCost)
	return h
})

// passwordErrors checks a new password against the spec's rules.
func passwordErrors(user, pass, repeat, current string) []FieldError {
	var msg, field = "", "password"
	switch {
	case utf8.RuneCountInString(pass) < 12:
		msg = "Use at least 12 characters."
	case len(pass) > 72:
		msg = "Use at most 72 bytes. Letters outside A to Z count as 2 or more."
	case pass == user:
		msg = "Don't use the username as the password."
	case current != "" && pass == current:
		msg = "Use a password different from the current one."
	case repeat != pass:
		field, msg = "repeat", "The passwords don't match."
	default:
		return nil
	}
	return []FieldError{{Field: field, Message: msg}}
}

func normalizeCode(s string) []byte {
	s = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(s))
	return []byte(s)
}

// --- sessions ---

type sessionCtx struct{}

type current struct {
	key string
	s   session
}

func tokenKey(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// lookupSession finds the live session behind the request's cookie.
func (a *App) lookupSession(r *http.Request) (current, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return current{}, false
	}
	key := tokenKey(c.Value)
	var s session
	if ok, err := a.sessions.Get(key, &s); err != nil || !ok {
		return current{}, false
	}
	now := a.clock.Now()
	if !s.live(now) {
		a.sessions.Delete(key)
		return current{}, false
	}
	// Last-seen is written at most once a minute, so a UI polling
	// every 5 seconds doesn't fsync each poll; idle timing is ±1 minute.
	// The touch re-reads inside its write transaction, so a session that a
	// logout or password change deleted meanwhile isn't written back.
	if now.Sub(s.LastSeen) >= time.Minute {
		alive := false
		a.sessions.Update(func(b *bolt.Bucket) error {
			raw := b.Get([]byte(key))
			if raw == nil || json.Unmarshal(raw, &s) != nil || !s.live(now) {
				return nil
			}
			s.LastSeen = now
			raw, err := json.Marshal(s)
			if err != nil {
				return err
			}
			alive = true
			return b.Put([]byte(key), raw)
		})
		if !alive {
			return current{}, false
		}
	}
	return current{key, s}, true
}

func (a *App) signedIn(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur, ok := a.lookupSession(r)
		if !ok {
			WriteError(w, http.StatusUnauthorized, FieldError{Message: "Log in first."})
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), sessionCtx{}, cur)))
	})
}

// startSession signs the request's client in and answers like GET /api/auth/session.
func (a *App) startSession(w http.ResponseWriter, r *http.Request, user string) {
	now := a.clock.Now()
	a.sessions.deleteWhere(func(_, raw []byte) bool {
		var s session
		return json.Unmarshal(raw, &s) != nil || !s.live(now)
	})
	tok := make([]byte, 32)
	rand.Read(tok)
	token := base64.RawURLEncoding.EncodeToString(tok)
	client := r.UserAgent()
	if len(client) > 200 {
		client = client[:200]
	}
	if err := a.sessions.Put(tokenKey(token), session{Created: now, LastSeen: now, Client: client}); err != nil {
		WriteError(w, http.StatusInternalServerError, FieldError{Message: "Couldn't save the session."})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/",
		MaxAge: int(sessionLimit.Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	WriteJSON(w, http.StatusOK, sessionOut{Username: user, Since: now})
}

type sessionOut struct {
	Username string    `json:"username"`
	Since    time.Time `json:"since"`
}

// --- handlers ---

func (a *App) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	ok, err := a.hasAdmin()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, FieldError{Message: "Couldn't read the panel's database."})
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"needed": !ok, "reset": ResetCommand()})
}

func (a *App) handleSetup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code     string `json:"code"`
		Username string `json:"username"`
		Password string `json:"password"`
		Repeat   string `json:"repeat"`
	}
	if !ReadJSON(w, r, &in) {
		return
	}
	a.authMu.Lock()
	defer a.authMu.Unlock()
	if d := a.lockedFor(); d > 0 {
		writeLocked(w, d)
		return
	}
	if ok, _ := a.hasAdmin(); ok {
		WriteError(w, http.StatusConflict, FieldError{Message: "The admin account already exists. Log in instead."})
		return
	}
	var errs []FieldError
	var s setupRecord
	found, _ := a.auth.Get("setup", &s)
	h := sha256.Sum256(normalizeCode(in.Code))
	wrongCode := !found || subtle.ConstantTimeCompare(h[:], s.Hash) != 1
	switch {
	case wrongCode:
		errs = append(errs, FieldError{"code", "That code doesn't match. Use the one the installer printed."})
	case a.clock.Now().Sub(s.Created) >= codeLife:
		errs = append(errs, FieldError{"code", "That code has expired. Restart thefeed-admin to get a new one."})
	}
	user := strings.TrimSpace(in.Username)
	if user == "" {
		user = "admin"
	}
	if !usernameRe.MatchString(user) {
		errs = append(errs, FieldError{"username", "Use 3 to 32 letters, digits, dots, dashes or underscores."})
	}
	errs = append(errs, passwordErrors(user, in.Password, in.Repeat, "")...)
	if wrongCode {
		if left, _ := a.failed(r, "setup code"); left == 0 {
			writeLocked(w, lockFor)
			return
		}
	}
	if len(errs) > 0 {
		WriteError(w, http.StatusBadRequest, errs...)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcryptCost)
	if err == nil {
		err = a.auth.Delete("setup")
	}
	if err == nil {
		err = a.auth.Put("admin", adminRecord{Username: user, Hash: hash, Created: a.clock.Now()})
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, FieldError{Message: "Couldn't save the admin account."})
		return
	}
	os.Remove(setupCodePath(a.cfg.AdminDir))
	a.auth.Delete("failures")
	log.Printf("admin: admin account %q created from %s", user, clientAddr(r))
	a.startSession(w, r, user)
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !ReadJSON(w, r, &in) {
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	switch {
	case in.Username == "":
		WriteError(w, http.StatusBadRequest, FieldError{"username", "Type the username."})
		return
	case in.Password == "":
		WriteError(w, http.StatusBadRequest, FieldError{"password", "Type the password."})
		return
	}
	a.authMu.Lock()
	defer a.authMu.Unlock()
	if d := a.lockedFor(); d > 0 {
		writeLocked(w, d)
		return
	}
	var adm adminRecord
	found, _ := a.auth.Get("admin", &adm)
	hash := adm.Hash
	if !found || in.Username != adm.Username {
		hash = dummyHash()
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(in.Password)) != nil || !found || in.Username != adm.Username {
		left, _ := a.failed(r, "password")
		if left == 0 {
			writeLocked(w, lockFor)
			return
		}
		msg := "Wrong username or password."
		if left <= maxFails-3 {
			msg += fmt.Sprintf(" %d %s left before a 5-minute wait.", left, plural(left, "try", "tries"))
		}
		WriteJSON(w, http.StatusUnauthorized, errorBody{Errors: []FieldError{{Message: msg}}, TriesLeft: left})
		return
	}
	a.auth.Delete("failures")
	a.startSession(w, r, adm.Username)
}

// checkPassword is the operator typing the password again before an action
// that reaches root, so a stolen session cookie alone can't do it. It
// answers the request itself when the password is missing or wrong; a
// wrong one counts toward the sign-in lockout.
func (a *App) checkPassword(w http.ResponseWriter, r *http.Request, pass string) bool {
	if pass == "" {
		WriteError(w, http.StatusForbidden, FieldError{"password", "Type your password to do this."})
		return false
	}
	a.authMu.Lock()
	defer a.authMu.Unlock()
	if d := a.lockedFor(); d > 0 {
		writeLocked(w, d)
		return false
	}
	var adm adminRecord
	if ok, err := a.auth.Get("admin", &adm); err != nil || !ok {
		WriteError(w, http.StatusInternalServerError, FieldError{Message: "Couldn't read the admin account."})
		return false
	}
	if bcrypt.CompareHashAndPassword(adm.Hash, []byte(pass)) == nil {
		a.auth.Delete("failures")
		return true
	}
	if left, _ := a.failed(r, "password to confirm an action"); left == 0 {
		writeLocked(w, lockFor)
		return false
	}
	WriteError(w, http.StatusForbidden, FieldError{"password", "That isn't your password."})
	return false
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		a.sessions.Delete(tokenKey(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	WriteJSON(w, http.StatusOK, map[string]any{})
}

func (a *App) handleSession(w http.ResponseWriter, r *http.Request) {
	cur := r.Context().Value(sessionCtx{}).(current)
	var adm adminRecord
	a.auth.Get("admin", &adm)
	WriteJSON(w, http.StatusOK, sessionOut{Username: adm.Username, Since: cur.s.Created})
}

func (a *App) handlePassword(w http.ResponseWriter, r *http.Request) {
	cur := r.Context().Value(sessionCtx{}).(current)
	var in struct {
		Current  string `json:"current"`
		Password string `json:"password"`
		Repeat   string `json:"repeat"`
	}
	if !ReadJSON(w, r, &in) {
		return
	}
	a.authMu.Lock()
	defer a.authMu.Unlock()
	if d := a.lockedFor(); d > 0 {
		writeLocked(w, d)
		return
	}
	var adm adminRecord
	if ok, err := a.auth.Get("admin", &adm); err != nil || !ok {
		WriteError(w, http.StatusInternalServerError, FieldError{Message: "Couldn't read the admin account."})
		return
	}
	var errs []FieldError
	wrong := bcrypt.CompareHashAndPassword(adm.Hash, []byte(in.Current)) != nil
	if wrong {
		errs = append(errs, FieldError{"current", "That isn't the current password."})
	}
	errs = append(errs, passwordErrors(adm.Username, in.Password, in.Repeat, in.Current)...)
	if wrong {
		if left, _ := a.failed(r, "current password"); left == 0 {
			writeLocked(w, lockFor)
			return
		}
	}
	if len(errs) > 0 {
		WriteError(w, http.StatusBadRequest, errs...)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcryptCost)
	if err == nil {
		adm.Hash = hash
		err = a.auth.Put("admin", adm)
	}
	if err == nil {
		err = a.sessions.deleteWhere(func(k, _ []byte) bool { return string(k) != cur.key })
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, FieldError{Message: "Couldn't change the password."})
		return
	}
	a.auth.Delete("failures")
	log.Printf("admin: password changed from %s; other sessions ended", clientAddr(r))
	WriteJSON(w, http.StatusOK, map[string]any{})
}

func (a *App) handleSessions(w http.ResponseWriter, r *http.Request) {
	cur := r.Context().Value(sessionCtx{}).(current)
	type row struct {
		Since    time.Time `json:"since"`
		LastSeen time.Time `json:"lastSeen"`
		Client   string    `json:"client"`
		Current  bool      `json:"current"`
	}
	now := a.clock.Now()
	rows := []row{}
	a.sessions.deleteWhere(func(k, raw []byte) bool {
		var s session
		if json.Unmarshal(raw, &s) != nil || !s.live(now) {
			return true
		}
		rows = append(rows, row{s.Created, s.LastSeen, s.Client, string(k) == cur.key})
		return false
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].LastSeen.After(rows[j].LastSeen) })
	WriteJSON(w, http.StatusOK, map[string]any{"sessions": rows})
}

// --- request checks used by Handler ---

// sameOrigin is true when the browser-set Origin is this panel's own origin,
// built from how the request actually arrived (so an SSH tunnel on any local
// port still matches).
func sameOrigin(r *http.Request) bool {
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	o := r.Header.Get("Origin")
	return o != "" && strings.EqualFold(o, scheme+"://"+r.Host)
}

func isJSON(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	return host == "localhost" || net.ParseIP(host).IsLoopback()
}
