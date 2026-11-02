package raidlingo

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"raidlingo/internal/ws"
	"strings"
	"sync"
	"time"
)

type oauthState struct {
	slot, verifier string
	expires        time.Time
}
type App struct {
	Session        *Session
	mu             sync.Mutex
	states         map[string]oauthState
	origins, hosts map[string]bool
	mock           http.Handler
}

func randomID() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func NewApp(s *Session, mock http.Handler) *App {
	a := &App{Session: s, states: map[string]oauthState{}, origins: map[string]bool{}, hosts: map[string]bool{}, mock: mock}
	for _, address := range []string{s.Config.RedirectURI, s.Config.SeatURL, s.Config.OverlayURL} {
		u, _ := url.Parse(address)
		a.origins[u.Scheme+"://"+u.Host] = true
		a.hosts[strings.ToLower(u.Hostname())] = true
		if u.Hostname() == "localhost" {
			u.Host = strings.Replace(u.Host, "localhost", "127.0.0.1", 1)
			a.origins[u.Scheme+"://"+u.Host] = true
			a.hosts["127.0.0.1"] = true
		}
	}
	return a
}
func jsonReply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func errorReply(w http.ResponseWriter, status int, message string) {
	jsonReply(w, status, map[string]string{"error": message})
}
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, e := url.Parse("http://" + r.Host)
	if e != nil || host.User != nil || !a.hosts[strings.ToLower(host.Hostname())] {
		errorReply(w, 403, "Unrecognized host. Open the configured desk or overlay URL.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if a.mock != nil && strings.HasPrefix(r.URL.Path, "/mock/") {
		http.StripPrefix("/mock", a.mock).ServeHTTP(w, r)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		errorReply(w, 405, "Method not allowed.")
		return
	}
	switch r.URL.Path {
	case "/health":
		state := a.Session.Snapshot()
		jsonReply(w, 200, map[string]any{"ok": true, "phase": 2, "relayReady": state.Relay.Ready, "twitchReady": state.Seats["A"].EventSub.Subscriptions["channel.chat.message"] == "enabled" && state.Seats["B"].EventSub.Subscriptions["channel.chat.message"] == "enabled"})
	case "/api/state":
		jsonReply(w, 200, a.Session.Snapshot())
	case "/auth/twitch":
		a.startAuth(w, r)
	case "/callback":
		a.callback(w, r)
	case "/ws":
		a.browser(w, r)
	default:
		if a.Session.Config.Production {
			if strings.HasPrefix(r.URL.Path, "/overlay/") {
				http.StripPrefix("/overlay/", http.FileServer(http.Dir(filepath.Join(a.Session.Config.Root, "apps/overlay/dist")))).ServeHTTP(w, r)
			} else if r.URL.Path == "/overlay" {
				http.Redirect(w, r, "/overlay/", 301)
			} else {
				http.FileServer(http.Dir(filepath.Join(a.Session.Config.Root, "apps/seat/dist"))).ServeHTTP(w, r)
			}
		} else if r.URL.Path == "/" {
			http.Redirect(w, r, a.Session.Config.SeatURL, 302)
		} else {
			http.NotFound(w, r)
		}
	}
}
func cookieName(state string) string {
	if len(state) > 16 {
		state = state[:16]
	}
	return "raidlingo_oauth_" + state
}
func (a *App) startAuth(w http.ResponseWriter, r *http.Request) {
	slot := r.URL.Query().Get("seat")
	if slot != "A" && slot != "B" && slot != "bot" {
		errorReply(w, 400, "seat must be A, B, or bot.")
		return
	}
	callback, _ := url.Parse(a.Session.Config.RedirectURI)
	requestURL, _ := url.Parse("http://" + r.Host)
	if !strings.EqualFold(callback.Hostname(), requestURL.Hostname()) {
		// Host-only OAuth cookies must be set on the callback's hostname.
		http.Redirect(w, r, callback.Scheme+"://"+callback.Host+"/auth/twitch?seat="+slot, http.StatusFound)
		return
	}
	a.mu.Lock()
	for key, s := range a.states {
		if time.Now().After(s.expires) {
			delete(a.states, key)
		}
	}
	if len(a.states) >= 100 {
		a.mu.Unlock()
		errorReply(w, 429, "Too many pending Twitch logins. Wait 10 minutes and retry.")
		return
	}
	state, verifier := randomID(), randomID()
	a.states[state] = oauthState{slot, verifier, time.Now().Add(10 * time.Minute)}
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName(state), Value: verifier, Path: "/callback", HttpOnly: true, Secure: strings.HasPrefix(a.Session.Config.RedirectURI, "https:"), SameSite: http.SameSiteLaxMode, MaxAge: 600})
	http.Redirect(w, r, a.Session.OAuth.AuthorizeURL(state, slot), 302)
}
func (a *App) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	cookie, _ := r.Cookie(cookieName(state))
	a.mu.Lock()
	pending, ok := a.states[state]
	valid := ok && cookie != nil && time.Now().Before(pending.expires) && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(pending.verifier)) == 1
	if valid {
		delete(a.states, state)
	}
	a.mu.Unlock()
	if !valid {
		errorReply(w, 400, "Invalid or expired Twitch OAuth state. Start Connect Twitch again in this browser.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName(state), Path: "/callback", MaxAge: -1, HttpOnly: true, Secure: strings.HasPrefix(a.Session.Config.RedirectURI, "https:"), SameSite: http.SameSiteLaxMode})
	if q.Get("error") != "" {
		errorReply(w, 400, "Twitch authorization was declined. Return to the desk and approve the requested permissions.")
		return
	}
	if q.Get("code") == "" {
		errorReply(w, 400, "Twitch did not return an authorization code.")
		return
	}
	if err := a.Session.Authorize(r.Context(), pending.slot, q.Get("code")); err != nil {
		errorReply(w, 500, err.Error())
		return
	}
	suffix := "seat=" + pending.slot + "&connected=1"
	if pending.slot == "bot" {
		suffix = "bot=connected"
	}
	http.Redirect(w, r, strings.TrimRight(a.Session.Config.SeatURL, "/")+"/?"+suffix, 302)
}
func (a *App) browser(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin != "" && !a.origins[origin] {
		errorReply(w, 403, "Unrecognized WebSocket origin.")
		return
	}
	conn, err := ws.Upgrade(w, r)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.MaxPayload = 16384
	conn.SetReadDeadline(time.Now().Add(70 * time.Second))
	conn.OnPong = func() { conn.SetReadDeadline(time.Now().Add(70 * time.Second)) }
	messages, unsubscribe := a.Session.Subscribe()
	defer unsubscribe()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = conn.ReadText() }()
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-done:
			return
		case <-r.Context().Done():
			return
		case data, ok := <-messages:
			if !ok {
				return
			}
			if conn.WriteText(data) != nil {
				return
			}
		case <-heartbeat.C:
			if conn.Ping() != nil {
				return
			}
		}
	}
}
func (a *App) Address() string {
	return fmt.Sprintf("%s:%d", a.Session.Config.Host, a.Session.Config.Port)
}
