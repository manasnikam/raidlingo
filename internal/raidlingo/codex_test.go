package raidlingo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Use the test executable as a fake CLI to exercise real process/stdin/stdout
// boundaries without a Codex installation, network, or paid inference.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "exec" {
		codexFixture()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func codexFixture() {
	for _, key := range []string{"TWITCH_CLIENT_SECRET", "OPENAI_API_KEY", "CODEX_API_KEY", "UNRELATED_PRIVATE_VALUE"} {
		if os.Getenv(key) != "" {
			fmt.Fprintln(os.Stderr, "secret environment inherited")
			os.Exit(2)
		}
	}
	var request struct {
		Text, SourceHint string
		Glossary         []string
	}
	if json.NewDecoder(os.Stdin).Decode(&request) != nil {
		os.Exit(3)
	}
	if strings.Contains(strings.Join(os.Args, " "), request.Text) {
		os.Exit(4)
	}
	var schemaPath string
	for i, arg := range os.Args {
		if arg == "--output-schema" && i+1 < len(os.Args) {
			schemaPath = os.Args[i+1]
		}
	}
	data, err := os.ReadFile(schemaPath)
	if err != nil || !json.Valid(data) {
		os.Exit(5)
	}
	cwd, _ := os.Getwd()
	realDir, _ := filepath.EvalSymlinks(filepath.Dir(schemaPath))
	if cwd != realDir {
		os.Exit(6)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".env")); !os.IsNotExist(err) {
		os.Exit(7)
	}
	if request.SourceHint != "en" || len(request.Glossary) != 1 || request.Glossary[0] != "Raidlingo" {
		os.Exit(8)
	}
	if request.Text == "sleep" {
		time.Sleep(30 * time.Second)
		return
	}
	if request.Text == "fail" {
		fmt.Fprintln(os.Stderr, "401 Unauthorized secret-sentinel")
		os.Exit(9)
	}
	if request.Text == "overflow" {
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", 256<<10))
		return
	}
	emit := func(value any) { _ = json.NewEncoder(os.Stdout).Encode(value) }
	emit(map[string]any{"type": "thread.started", "thread_id": "fixture"})
	// Real exec streams include diagnostics and planning metadata around the
	// translation. Exercise those through the process boundary on every run.
	emit(map[string]any{"type": "item.completed", "item": map[string]any{"type": "error", "message": "recoverable CLI warning"}})
	emit(map[string]any{"type": "item.updated", "item": map[string]any{"type": "todo_list", "items": []any{}}})
	if request.Text == "tool" {
		emit(map[string]any{"type": "item.started", "item": map[string]any{"type": "command_execution", "command": "untrusted tool"}})
	}
	bundle := Bundle{"en", map[string]string{"en": request.Text, "es": "¡Hola, Raidlingo!", "hi": "नमस्ते, Raidlingo!"}}
	if request.Text == "missing-language" {
		delete(bundle.Translations, "hi")
	}
	answer, _ := json.Marshal(bundle)
	if request.Text == "bad" {
		answer = []byte("invalid JSON")
	}
	emit(map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": string(answer)}})
	if request.Text == "partial" {
		return
	}
	emit(map[string]any{"type": "turn.completed"})
	if request.Text == "exit-nonzero" {
		os.Exit(10)
	}
}

func TestCodexConfig(t *testing.T) {
	env := map[string]string{"TWITCH_CLIENT_ID": "test-client", "TWITCH_CLIENT_SECRET": "test-secret", "TRANSLATION_PROVIDER": "codex"}
	get := func(key string) string { return env[key] }
	c, err := LoadConfig(t.TempDir(), get, false, false)
	if err != nil || c.OpenAIKey != "" || c.TranslationProvider != "codex" || c.CodexTimeout != 20*time.Second {
		t.Fatalf("CLI config: %+v %v", c, err)
	}
	for key, value := range map[string]string{"TRANSLATION_PROVIDER": "invalid", "CODEX_TIMEOUT_MS": "0"} {
		old := env[key]
		env[key] = value
		if _, err := LoadConfig(t.TempDir(), get, false, false); err == nil {
			t.Fatalf("accepted %s", key)
		}
		env[key] = old
	}
	env["TRANSLATION_PROVIDER"] = "openai"
	if _, err := LoadConfig(t.TempDir(), get, false, false); err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatal("API key requirement disappeared")
	}
	env["TRANSLATION_PROVIDER"] = "codex"
	c, err = LoadConfig(t.TempDir(), get, false, true)
	if err != nil || c.TranslationProvider != "openai" || c.OpenAIKey != "test-openai" {
		t.Fatal("demo would call real CLI")
	}
}

func TestCodexProcessAndCache(t *testing.T) {
	for _, key := range []string{"TWITCH_CLIENT_SECRET", "OPENAI_API_KEY", "CODEX_API_KEY", "UNRELATED_PRIVATE_VALUE"} {
		t.Setenv(key, "secret-sentinel")
	}
	binary, _ := os.Executable()
	c := Config{TranslationProvider: "codex", CodexBin: binary, CodexTimeout: 5 * time.Second}
	translator := NewTranslator(NewTranslationProvider(c, nil))
	request := TranslationRequest{Text: "Hello नमस्ते $(touch NOT_A_COMMAND); `echo literal`", Source: "en", Target: "hi", Hints: []string{"Raidlingo"}}
	result, err := translator.Translate(context.Background(), request)
	if err != nil || result.Translated != "नमस्ते, Raidlingo!" || result.Translations["en"] != request.Text {
		t.Fatalf("translation boundary: %+v %v", result, err)
	}
	result, err = translator.Translate(context.Background(), request)
	if err != nil || !result.Cached || translator.Stats().Requests != 1 {
		t.Fatal("CLI cache not shared")
	}
}

func TestCodexFailuresAndCancellation(t *testing.T) {
	binary, _ := os.Executable()
	for _, mode := range []string{"fail", "overflow", "tool", "bad", "partial", "missing-language", "exit-nonzero", "sleep"} {
		t.Run(mode, func(t *testing.T) {
			timeout := 5 * time.Second
			if mode == "sleep" {
				timeout = 100 * time.Millisecond
			}
			translator := NewTranslator(CodexProvider(Config{CodexBin: binary, CodexTimeout: timeout}))
			started := time.Now()
			_, err := translator.Translate(context.Background(), TranslationRequest{Text: mode, Source: "en", Target: "es", Hints: []string{"Raidlingo"}})
			if err == nil {
				t.Fatal("invalid CLI result accepted")
			}
			if strings.Contains(err.Error(), "secret-sentinel") {
				t.Fatal("CLI diagnostic leaked")
			}
			if translator.Stats().Entries != 0 {
				t.Fatal("failure was cached")
			}
			if mode == "sleep" && time.Since(started) > 2*time.Second {
				t.Fatal("child was not cancelled promptly")
			}
		})
	}
}

func TestCodexFailureIgnoresRecoverableWarnings(t *testing.T) {
	warning := "WARNING: proceeding, even though we could not create PATH aliases: Operation not permitted (os error 1)\n"
	for _, tc := range []struct{ detail, want string }{
		{"Error loading config.toml: unknown variant secret-sentinel", "configuration is incompatible"},
		{"401 Unauthorized secret-sentinel", "sign-in is required"},
		{"429 Too Many Requests secret-sentinel", "usage limit reached"},
		{"Network connection failed secret-sentinel", "CLI connectivity"},
		{"Error: Permission denied secret-sentinel", "blocked by local permissions"},
	} {
		err := codexFailure(warning + tc.detail)
		if !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret-sentinel") {
			t.Errorf("diagnostic classification: %v", err)
		}
	}
}

func TestCodexRejectedItemsDoNotExposeContent(t *testing.T) {
	for _, kind := range []string{"command_execution", "file_change", "mcp_tool_call", "web_search", "collab_tool_call", "dynamic_tool_call", "secret-sentinel"} {
		t.Run(kind, func(t *testing.T) {
			var stream strings.Builder
			encoder := json.NewEncoder(&stream)
			_ = encoder.Encode(map[string]any{"type": "item.started", "item": map[string]any{
				"type": kind, "text": "secret-sentinel", "command": "secret-sentinel", "arguments": "secret-sentinel",
			}})
			_, err := parseCodexOutput([]byte(stream.String()))
			if err == nil || strings.Contains(err.Error(), "secret-sentinel") {
				t.Fatalf("unsafe diagnostic: %v", err)
			}
			if kind != "secret-sentinel" && !strings.Contains(err.Error(), kind) {
				t.Fatalf("missing safe event label: %v", err)
			}
		})
	}
}

func TestCodexMetadataRequiresSuccessfulTranslation(t *testing.T) {
	answer, _ := json.Marshal(Bundle{"en", map[string]string{"en": "Hello", "es": "Hola", "hi": "नमस्ते"}})
	agent := map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": string(answer)}}
	completed := map[string]any{"type": "turn.completed"}
	for _, metadata := range []string{"reasoning", "todo_list", "error"} {
		for _, lifecycle := range []string{"item.started", "item.updated", "item.completed"} {
			t.Run(metadata+"/"+lifecycle, func(t *testing.T) {
				prefix := map[string]any{"type": lifecycle, "item": map[string]any{
					"type": metadata, "message": "secret-sentinel", "text": string(answer),
				}}
				for _, tc := range []struct {
					name   string
					events []any
					ok     bool
				}{
					{"complete", []any{prefix, agent, completed}, true},
					{"no-answer", []any{prefix, completed}, false},
					{"no-completion", []any{prefix, agent}, false},
					{"turn-failed", []any{prefix, agent, map[string]any{"type": "turn.failed", "error": map[string]string{"message": "secret-sentinel"}}}, false},
					{"fatal-error", []any{prefix, agent, completed, map[string]any{"type": "error", "message": "secret-sentinel"}}, false},
					{"tool-after-warning", []any{prefix, agent, completed, map[string]any{"type": "item.completed", "item": map[string]any{"type": "command_execution"}}}, false},
				} {
					t.Run(tc.name, func(t *testing.T) {
						var stream strings.Builder
						encoder := json.NewEncoder(&stream)
						for _, event := range tc.events {
							_ = encoder.Encode(event)
						}
						bundle, err := parseCodexOutput([]byte(stream.String()))
						if tc.ok {
							if err != nil || bundle.Translations["es"] != "Hola" {
								t.Fatalf("metadata rejected successful translation: %+v, %v", bundle, err)
							}
						} else if err == nil || strings.Contains(err.Error(), "secret-sentinel") {
							t.Fatalf("accepted failed turn or exposed diagnostic: %v", err)
						}
					})
				}
			})
		}
	}
}
