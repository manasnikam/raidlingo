package raidlingo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLANConfig(t *testing.T) {
	c := testConfig(t)
	c.Port = 8123
	callback := c.RedirectURI
	if err := c.EnableLAN("192.168.1.14"); err != nil {
		t.Fatal(err)
	}
	if !c.Production || c.Host != "0.0.0.0" || c.SeatURL != "http://192.168.1.14:8123" || c.OverlayURL != c.SeatURL+"/overlay/" || c.RedirectURI != callback {
		t.Fatal("LAN URLs, bind address, or registered callback changed incorrectly")
	}
	for _, address := range []string{"", "localhost", "127.0.0.1", "0.0.0.0", "8.8.8.8", "169.254.1.1", "192.168.1.14:3000", "192.168.1.14/24", "fd00::1"} {
		if err := c.EnableLAN(address); err == nil {
			t.Errorf("accepted unsupported LAN address %q", address)
		}
	}
	c.Demo = true
	if err := c.EnableLAN("192.168.1.14"); err == nil {
		t.Fatal("LAN mode accepted simulated credentials")
	}
}

func TestLANAssetsStateWebSocketAndLocalOAuth(t *testing.T) {
	f := newFixture(t, func(c *Config, _ *TokenStore, _ *Simulator) {
		if err := c.EnableLAN("192.168.1.14"); err != nil {
			t.Fatal(err)
		}
		c.Root = t.TempDir()
		for _, app := range []string{"seat", "overlay"} {
			dir := filepath.Join(c.Root, "apps", app, "dist")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>"+app+"</html>"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	})
	origin := f.session.Config.SeatURL
	request := func(address, browserOrigin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", address, nil)
		r.Header.Set("Origin", browserOrigin)
		w := httptest.NewRecorder()
		f.app.ServeHTTP(w, r)
		return w
	}
	for path, want := range map[string]string{"/": "<html>seat</html>", "/overlay/": "<html>overlay</html>", "/api/state": origin + "/overlay/"} {
		w := request(origin+path, origin)
		if w.Code != 200 || !strings.Contains(w.Body.String(), want) {
			t.Fatalf("LAN route %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := request("http://attacker.invalid/api/state", origin); w.Code != 403 {
		t.Fatal("LAN enabled arbitrary host access")
	}
	if w := request(origin+"/ws", "http://attacker.invalid"); w.Code != 403 {
		t.Fatal("LAN enabled arbitrary WebSocket origins")
	}
	w := request(origin+"/auth/twitch?seat=A", origin)
	if w.Code != 302 || w.Header().Get("Location") != "http://localhost:3000/auth/twitch?seat=A" || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("LAN request changed the registered OAuth callback host")
	}
	state, cookie := f.start(t, "A")
	w = f.request("/callback?state="+state+"&code=code-100", cookie)
	if w.Code != 302 || w.Header().Get("Location") != origin+"/?seat=A&connected=1" {
		t.Fatalf("OAuth callback did not return to LAN desk: %d %s", w.Code, w.Body.String())
	}
	f.setFault(func(_ http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/ws" {
			r.Header.Set("Origin", origin)
		}
		return false
	})
	conn, err := f.dial(context.Background(), strings.Replace(origin, "http:", "ws:", 1)+"/ws")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(time.Second))
	var message struct {
		Type  string      `json:"type"`
		State PublicState `json:"state"`
	}
	if err := conn.ReadJSON(&message); err != nil || message.Type != "state" || message.State.OverlayURL != origin+"/overlay/" {
		t.Fatalf("LAN state stream failed: %+v, %v", message, err)
	}
}
