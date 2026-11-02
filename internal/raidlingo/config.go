package raidlingo

import (
	"bufio"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type SeatConfig struct{ Channel, Language string }
type Config struct {
	TranslationProvider, CodexBin, CodexModel                                                                                                           string
	CodexTimeout                                                                                                                                        time.Duration
	Root, ClientID, ClientSecret, OpenAIKey, RedirectURI, Host, SeatURL, OverlayURL, TokenPath, LogLevel, Model, BotChannel, OverlayLanguage, VoiceMode string
	Port                                                                                                                                                int
	Production, Demo                                                                                                                                    bool
	Hints                                                                                                                                               []string
	Seats                                                                                                                                               map[string]SeatConfig
	AuthBase, HelixBase, EventSubURL, OpenAIBase                                                                                                        string
	RetryBase, ValidationInterval, OutboxGap, OutboxMaxAge, TranslationMaxAge                                                                           time.Duration
}

// EnableLAN serves the built apps from one private-network origin. Keep OAuth
// on its registered callback; localhost authorization still happens on the host.
func (c *Config) EnableLAN(address string) error {
	ip := net.ParseIP(address)
	if ip == nil || ip.To4() == nil || !ip.IsPrivate() {
		return fmt.Errorf("--lan-ip must be your computer's private IPv4 address (for example 192.168.1.14)")
	}
	if c.Demo {
		return fmt.Errorf("--lan-ip cannot be combined with --demo")
	}
	c.Production = true
	c.Host = "0.0.0.0"
	c.SeatURL = fmt.Sprintf("http://%s:%d", ip.String(), c.Port)
	c.OverlayURL = c.SeatURL + "/overlay/"
	return nil
}

func FindRoot() string {
	cwd, _ := os.Getwd()
	for p := cwd; ; p = filepath.Dir(p) {
		if _, e := os.Stat(filepath.Join(p, "go.mod")); e == nil {
			return p
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	exe, _ := os.Executable()
	return filepath.Dir(filepath.Dir(exe))
}

// LoadEnv never overwrites values supplied by the process environment.
func LoadEnv(path string) error {
	f, e := os.Open(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 64<<10)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("invalid .env assignment")
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) > 0 && (v[0] == '\'' || v[0] == '"') {
			quote := v[0]
			end := strings.LastIndexByte(v[1:], quote)
			if end < 0 {
				return fmt.Errorf("unclosed .env quote for %s", k)
			}
			literal := v[:end+2]
			if quote == '"' {
				parsed, e := strconv.Unquote(literal)
				if e != nil {
					return fmt.Errorf("invalid .env quote for %s", k)
				}
				v = parsed
			} else {
				v = literal[1 : len(literal)-1]
			}
		} else {
			if i := strings.Index(v, " #"); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
		}
		if _, set := os.LookupEnv(k); !set {
			if e := os.Setenv(k, v); e != nil {
				return e
			}
		}
	}
	return scan.Err()
}
func LoadConfig(root string, get func(string) string, production, demo bool) (Config, error) {
	val := func(k, d string) string {
		if v := get(k); v != "" {
			return v
		}
		return d
	}
	c := Config{Root: root, Production: production || get("NODE_ENV") == "production", Demo: demo, ClientID: strings.TrimSpace(get("TWITCH_CLIENT_ID")), ClientSecret: strings.TrimSpace(get("TWITCH_CLIENT_SECRET")), OpenAIKey: strings.TrimSpace(get("OPENAI_API_KEY")), Host: val("HOST", "127.0.0.1"), RedirectURI: val("TWITCH_REDIRECT_URI", "http://localhost:3000/callback"), SeatURL: val("SEAT_URL", "http://localhost:5173"), OverlayURL: val("OVERLAY_URL", "http://localhost:5174"), TokenPath: val("TOKEN_STORE_PATH", ".data/twitch-tokens.json"), LogLevel: val("LOG_LEVEL", "info"), Model: val("OPENAI_TRANSLATE_MODEL", "gpt-4o-mini"), BotChannel: strings.ToLower(strings.TrimSpace(get("BOT_CHANNEL"))), OverlayLanguage: val("OVERLAY_LANGUAGE", "en"), VoiceMode: val("VOICE_MODE", "realtime"), Seats: map[string]SeatConfig{}, AuthBase: "https://id.twitch.tv/oauth2", HelixBase: "https://api.twitch.tv/helix", EventSubURL: "wss://eventsub.wss.twitch.tv/ws", OpenAIBase: "https://api.openai.com/v1", RetryBase: time.Second, ValidationInterval: time.Hour, OutboxGap: 1600 * time.Millisecond, OutboxMaxAge: 45 * time.Second, TranslationMaxAge: 30 * time.Second}
	var err error
	c.Port, err = strconv.Atoi(val("PORT", "3000"))
	if err != nil || c.Port < 1 || c.Port > 65535 {
		return c, fmt.Errorf("PORT must be between 1 and 65535")
	}
	c.TranslationProvider = val("TRANSLATION_PROVIDER", "openai")
	c.CodexBin = val("CODEX_BIN", "codex")
	c.CodexModel = strings.TrimSpace(get("CODEX_TRANSLATE_MODEL"))
	timeoutMS, err := strconv.Atoi(val("CODEX_TIMEOUT_MS", "20000"))
	if err != nil || timeoutMS < 1000 || timeoutMS > 30000 {
		return c, fmt.Errorf("CODEX_TIMEOUT_MS must be between 1000 and 30000")
	}
	c.CodexTimeout = time.Duration(timeoutMS) * time.Millisecond
	if demo {
		c.TranslationProvider = "openai"
		c.ClientID = "test-client"
		c.ClientSecret = "test-secret"
		c.OpenAIKey = "test-openai"
		c.Production = true
		c.RedirectURI = fmt.Sprintf("http://127.0.0.1:%d/callback", c.Port)
		c.Host = "127.0.0.1"
		c.BotChannel = "raidlingo_bot"
	}
	if c.TranslationProvider != "openai" && c.TranslationProvider != "codex" {
		return c, fmt.Errorf("TRANSLATION_PROVIDER must be openai or codex")
	}
	if c.TranslationProvider == "codex" {
		c.Model = c.CodexModel
		if c.Model == "" {
			c.Model = "Codex CLI default"
		}
	}
	missing := []string{}
	required := map[string]string{"TWITCH_CLIENT_ID": c.ClientID, "TWITCH_CLIENT_SECRET": c.ClientSecret}
	if c.TranslationProvider == "openai" {
		required["OPENAI_API_KEY"] = c.OpenAIKey
	}
	for k, v := range required {
		lower := strings.ToLower(v)
		if v == "" || strings.HasPrefix(lower, "replace") || strings.HasPrefix(lower, "your_") || strings.HasPrefix(lower, "<") {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("missing %s; copy .env.example to .env and fill in credentials", strings.Join(missing, ", "))
	}
	redirect, e := url.Parse(c.RedirectURI)
	if e != nil || redirect.Path != "/callback" || redirect.RawQuery != "" || redirect.Fragment != "" {
		return c, fmt.Errorf("TWITCH_REDIRECT_URI must end at /callback with no query or fragment")
	}
	origin := redirect.Scheme + "://" + redirect.Host
	if c.Production {
		c.SeatURL = origin
		c.OverlayURL = origin + "/overlay/"
	}
	for _, v := range []string{c.RedirectURI, c.SeatURL, c.OverlayURL} {
		u, e := url.Parse(v)
		if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return c, fmt.Errorf("app URLs must use http:// or https://")
		}
	}
	if !validLanguage(c.OverlayLanguage) {
		return c, fmt.Errorf("OVERLAY_LANGUAGE must be en, es, or hi")
	}
	if c.VoiceMode != "realtime" && c.VoiceMode != "pipeline" && c.VoiceMode != "off" {
		return c, fmt.Errorf("VOICE_MODE must be realtime, pipeline, or off")
	}
	login := regexp.MustCompile(`^[a-z0-9_]{1,25}$`)
	if c.BotChannel != "" && !login.MatchString(c.BotChannel) {
		return c, fmt.Errorf("BOT_CHANNEL must be a Twitch login")
	}
	for i, slot := range []string{"A", "B"} {
		channel := strings.ToLower(strings.TrimSpace(get("CHANNEL_" + slot)))
		if strings.EqualFold(channel, "<channel_"+slot+">") {
			channel = ""
		}
		if demo {
			channel = []string{"streamer_a", "streamer_b"}[i]
		}
		if channel != "" && !login.MatchString(channel) {
			return c, fmt.Errorf("CHANNEL_%s must be a Twitch login", slot)
		}
		lang := val("LANGUAGE_"+slot, []string{"en", "es"}[i])
		if !validLanguage(lang) {
			return c, fmt.Errorf("LANGUAGE_%s must be en, es, or hi", slot)
		}
		c.Seats[slot] = SeatConfig{channel, lang}
	}
	if c.Seats["A"].Channel != "" && c.Seats["A"].Channel == c.Seats["B"].Channel {
		return c, fmt.Errorf("CHANNEL_A and CHANNEL_B must be different")
	}
	for _, hint := range strings.Split(val("STREAM_HINTS", "Twitch,Raidlingo"), ",") {
		if h := strings.TrimSpace(hint); h != "" {
			c.Hints = append(c.Hints, h)
		}
	}
	if len(c.Hints) > 100 || len(strings.Join(c.Hints, ",")) > 4000 {
		return c, fmt.Errorf("STREAM_HINTS exceeds 100 names / 4000 bytes")
	}
	if !filepath.IsAbs(c.TokenPath) {
		c.TokenPath = filepath.Join(root, c.TokenPath)
	}
	return c, nil
}
