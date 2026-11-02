package raidlingo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// CodexProvider is an opt-in local translator. Only JSON chat data crosses stdin;
// it never receives the relay's environment, working tree, or Twitch credentials.
func CodexProvider(c Config) TranslationProvider {
	semaphore := make(chan struct{}, 1)
	return func(ctx context.Context, r TranslationRequest) (Bundle, error) {
		ctx, cancel := context.WithTimeout(ctx, c.CodexTimeout)
		defer cancel()
		select {
		case semaphore <- struct{}{}:
			defer func() { <-semaphore }()
		case <-ctx.Done():
			return Bundle{}, ctx.Err()
		}
		dir, err := os.MkdirTemp("", "raidlingo-translation-")
		if err != nil {
			return Bundle{}, errors.New("could not create Codex translation directory")
		}
		defer os.RemoveAll(dir)
		schemaPath := filepath.Join(dir, "translation.schema.json")
		if err = os.WriteFile(schemaPath, []byte(translationSchema), 0600); err != nil {
			return Bundle{}, errors.New("could not write Codex translation schema")
		}
		payload, _ := json.Marshal(map[string]any{"text": r.Text, "sourceHint": r.Source, "glossary": r.Hints})
		cmd := exec.CommandContext(ctx, c.CodexBin, codexArgs(c, schemaPath)...)
		cmd.Dir = dir
		cmd.Env = codexEnvironment(os.Environ())
		cmd.Stdin = bytes.NewReader(payload)
		cmd.WaitDelay = time.Second
		isolateCodexProcess(cmd)
		stdout := &limitedOutput{limit: 128 << 10, cancel: cancel}
		stderr := &limitedOutput{limit: 32 << 10, cancel: cancel}
		cmd.Stdout, cmd.Stderr = stdout, stderr
		err = cmd.Run()
		if stdout.overflow || stderr.overflow {
			return Bundle{}, errors.New("Codex translation exceeded the output limit; no chat posted")
		}
		if ctx.Err() != nil {
			return Bundle{}, fmt.Errorf("Codex translation cancelled or timed out: %w", ctx.Err())
		}
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return Bundle{}, errors.New("Codex CLI not found; install Codex or set CODEX_BIN")
			}
			return Bundle{}, codexFailure(stderr.String())
		}
		return parseCodexOutput(stdout.Bytes())
	}
}

func codexArgs(c Config, schemaPath string) []string {
	args := []string{"exec", "--ignore-user-config", "--strict-config", "--ephemeral", "--skip-git-repo-check", "--sandbox", "read-only", "--color", "never", "--json", "--output-schema", schemaPath}
	// These overrides reduce access for this child only. They don't modify the
	// user's Codex configuration, authentication, rules, or managed requirements.
	for _, setting := range []string{
		`approval_policy="never"`, `web_search="disabled"`, `mcp_servers={}`,
		`project_doc_max_bytes=0`, `skills.include_instructions=false`, `skills.bundled.enabled=false`,
		`model_reasoning_effort="low"`, `history.persistence="none"`,
		"developer_instructions=" + strconv.Quote(translationInstructions+" Do not use tools or inspect files. Only translate the supplied JSON data and return the required translation object."),
	} {
		args = append(args, "-c", setting)
	}
	for _, feature := range []string{
		"shell_tool", "unified_exec", "shell_snapshot", "view_image", "apps", "plugins", "remote_plugin",
		"browser_use", "browser_use_external", "computer_use", "in_app_browser", "code_mode", "code_mode_host",
		"multi_agent", "multi_agent_v2", "memories", "goals", "skill_mcp_dependency_install", "skill_search", "unbounded_connection_retries",
	} {
		args = append(args, "--disable", feature)
	}
	if c.CodexModel != "" {
		args = append(args, "--model", c.CodexModel)
	}
	return append(args, "-")
}

func codexEnvironment(environment []string) []string {
	allowed := map[string]bool{"PATH": true, "HOME": true, "USERPROFILE": true, "CODEX_HOME": true, "TMPDIR": true, "TMP": true, "TEMP": true, "SYSTEMROOT": true, "LANG": true, "LC_ALL": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true}
	result := []string{}
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if allowed[strings.ToUpper(key)] {
			result = append(result, entry)
		}
	}
	return result
}

type limitedOutput struct {
	bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > b.limit-b.Len() {
		p = p[:b.limit-b.Len()]
		b.overflow = true
		b.cancel()
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

func codexFailure(detail string) error {
	// CLI diagnostics may include prompts or credentials. Return curated text
	// because these errors are also sent to public desk/overlay clients.
	var diagnostics []string
	for _, line := range strings.Split(strings.ToLower(detail), "\n") {
		// Codex can continue without its optional PATH aliases. This warning
		// must not hide a later authentication, quota, or configuration error.
		if strings.HasPrefix(strings.TrimSpace(line), "warning: proceeding,") {
			continue
		}
		diagnostics = append(diagnostics, line)
	}
	lower := strings.Join(diagnostics, "\n")
	switch {
	case strings.Contains(lower, "error loading config"), strings.Contains(lower, "unexpected argument"), strings.Contains(lower, "unknown feature"):
		return errors.New("Codex CLI configuration is incompatible; check your CLI version and Raidlingo's Codex settings")
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "operation not permitted"):
		return errors.New("Codex CLI was blocked by local permissions; run npm run check:live from your terminal")
	case strings.Contains(lower, "429"), strings.Contains(lower, "usage limit"), strings.Contains(lower, "rate limit"), strings.Contains(lower, "quota"):
		return errors.New("Codex usage limit reached; wait for your allowance to reset")
	case strings.Contains(lower, "401"), strings.Contains(lower, "not logged in"), strings.Contains(lower, "unauthorized"), strings.Contains(lower, "authentication"):
		return errors.New("Codex sign-in is required; run codex login in your terminal")
	default:
		return errors.New("Codex translation failed; check codex login status and CLI connectivity")
	}
}

func parseCodexOutput(data []byte) (Bundle, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 128<<10)
	var answer string
	completed := false
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return Bundle{}, errors.New("Codex returned invalid event JSON")
		}
		switch event.Type {
		case "turn.failed", "error":
			return Bundle{}, codexFailure(string(scanner.Bytes()))
		case "turn.completed":
			completed = true
		case "item.started", "item.updated", "item.completed":
			switch event.Item.Type {
			case "agent_message":
				if event.Type == "item.completed" {
					answer = event.Item.Text
				}
			case "reasoning", "todo_list", "error":
				// These are metadata, not external actions. In particular, an
				// item of type "error" can report a recoverable CLI warning.
				// It never supplies the answer: a completed turn and valid
				// translation are still required. Top-level errors remain fatal.
			default:
				return Bundle{}, codexUnexpectedItem(event.Item.Type)
			}
		}
	}
	if scanner.Err() != nil || !completed || answer == "" {
		return Bundle{}, errors.New("Codex returned an incomplete translation")
	}
	var bundle Bundle
	decoder := json.NewDecoder(strings.NewReader(answer))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil {
		return Bundle{}, errors.New("Codex returned invalid translation JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Bundle{}, errors.New("Codex returned trailing translation content")
	}
	return bundle, nil
}

func codexUnexpectedItem(kind string) error {
	// Only known protocol labels are safe to show on the public desk. Never
	// echo arbitrary event fields, tool arguments, or message content.
	switch kind {
	case "command_execution", "file_change", "mcp_tool_call", "web_search", "collab_tool_call", "dynamic_tool_call":
		return fmt.Errorf("Codex returned unsupported item %q; response discarded", kind)
	default:
		return errors.New("Codex returned an unknown item type; response discarded")
	}
}
