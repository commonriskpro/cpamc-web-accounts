package validation

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	auth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	executor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	translator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type manager struct {
	items map[string]auth.ProviderExecutor
}

func (m *manager) Executor(p string) (auth.ProviderExecutor, bool) { e, ok := m.items[p]; return e, ok }
func (m *manager) RegisterExecutor(e auth.ProviderExecutor)        { m.items[e.Identifier()] = e }
func (m *manager) UnregisterExecutor(p string)                     { delete(m.items, p) }

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func setup(t *testing.T) (*pluginhost.Host, auth.ProviderExecutor) {
	t.Helper()
	// Every test process refuses network access unless its request carries our fixture transport.
	old := http.DefaultTransport
	http.DefaultTransport = transport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network disabled in native validation")
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	pluginDir := os.Getenv("WEB_ACCOUNTS_PLUGIN_DIR")
	if pluginDir == "" {
		pluginDir = "../../web-accounts"
	}
	dir, err := filepath.Abs(pluginDir)
	if err != nil {
		t.Fatal(err)
	}
	h := pluginhost.New()
	yes := true
	h.ApplyConfig(context.Background(), &config.Config{AuthDir: t.TempDir(), Plugins: config.PluginsConfig{Enabled: true, Dir: dir, Configs: map[string]config.PluginInstanceConfig{"web-accounts": {Enabled: &yes}}}})
	t.Cleanup(h.ShutdownAll)
	if len(h.RegisteredPlugins()) != 1 {
		t.Fatal("actual host rejected native registration")
	}
	m := &manager{items: map[string]auth.ProviderExecutor{}}
	h.RegisterExecutors(m, nil)
	e, ok := m.Executor("web-accounts")
	if !ok {
		t.Fatal("actual host did not install provider executor")
	}
	return h, e
}

func account(t *testing.T, h *pluginhost.Host, p string) *auth.Auth {
	t.Helper()
	obj := map[string]any{"type": "web-accounts", "web_provider": p}
	if p == "claude" {
		obj["cookie"] = "sessionKey=fixture-session-not-real"
	} else {
		obj["access_token"] = "fixture-token-not-real"
	}
	raw, _ := json.Marshal(obj)
	a, handled, err := h.ParseAuth(context.Background(), pluginapi.AuthParseRequest{Provider: "web-accounts", FileName: p + ".json", RawJSON: raw})
	if err != nil || !handled || a == nil {
		t.Fatal("actual host auth parse failed")
	}
	return a
}

func connect(v any, flags byte) []byte {
	b, _ := json.Marshal(v)
	out := make([]byte, 5+len(b))
	out[0] = flags
	binary.BigEndian.PutUint32(out[1:], uint32(len(b)))
	copy(out[5:], b)
	return out
}
func upstream(p string) []byte {
	if p == "kimi" {
		return append(connect(map[string]any{"op": "set", "mask": "block.text", "block": map[string]any{"text": map[string]string{"content": "hello"}}}, 0), connect(map[string]any{}, 2)...)
	}
	var b bytes.Buffer
	for _, event := range []any{
		map[string]any{"type": "message_start"},
		map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}},
		map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "hello"}},
		map[string]any{"type": "content_block_stop", "index": 0},
		map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}},
		map[string]any{"type": "message_stop"},
	} {
		raw, _ := json.Marshal(event)
		fmt.Fprintf(&b, "data: %s\n\n", raw)
	}
	return b.Bytes()
}

func fixtureContext(t *testing.T, provider string, status int, count *atomic.Int32) context.Context {
	t.Helper()
	ctx := context.WithValue(context.Background(), "validation-marker", "request-scope")
	rt := transport(func(r *http.Request) (*http.Response, error) {
		count.Add(1)
		if r.Context().Value("validation-marker") != "request-scope" {
			return nil, errors.New("callback lost request context")
		}
		if r.URL.Scheme != "https" || (r.URL.Host != "claude.ai" && r.URL.Host != "www.kimi.ai") {
			return nil, errors.New("unexpected fixture origin")
		}
		code := status
		var body []byte
		if r.Method == "GET" {
			code = 200
			body = []byte(`[{"uuid":"00000000-0000-4000-8000-000000000001"}]`)
		} else {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			if provider == "kimi" {
				if len(raw) < 5 {
					return nil, errors.New("missing Connect envelope")
				}
				raw = raw[5:]
			}
			if !bytes.Contains(raw, []byte("fixture-system")) {
				return nil, errors.New("system prompt missing")
			}
			body = upstream(provider)
			if code != 200 {
				body = []byte(`{"error":"fixture-token-not-real"}`)
			}
		}
		return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"text/event-stream"}, "Retry-After": {"7"}}, Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})
	return context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(rt))
}

func request(model string, stream bool) executor.Request {
	b, _ := json.Marshal(map[string]any{"model": model, "stream": stream, "messages": []any{map[string]string{"role": "system", "content": "fixture-system"}, map[string]string{"role": "user", "content": "hello"}}})
	return executor.Request{Model: model, Payload: b}
}

func TestNativeHostExecution(t *testing.T) {
	h, e := setup(t)
	for _, pair := range [][2]string{{"claude", "web-claude/claude-sonnet-4-6"}, {"kimi", "web-kimi/k3"}} {
		p, model := pair[0], pair[1]
		a := account(t, h, p)
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", p, stream), func(t *testing.T) {
				var calls atomic.Int32
				ctx, cancel := context.WithTimeout(fixtureContext(t, p, 200, &calls), 5*time.Second)
				defer cancel()
				opts := executor.Options{SourceFormat: translator.Format("openai")}
				if !stream {
					resp, err := e.Execute(ctx, a, request(model, false), opts)
					if err != nil {
						t.Fatal(err)
					}
					var b struct {
						Model   string
						Choices []struct {
							Message      struct{ Content string }
							FinishReason string `json:"finish_reason"`
						}
					}
					if err := json.Unmarshal(resp.Payload, &b); err != nil {
						t.Fatal(err)
					}
					if b.Model != model || len(b.Choices) != 1 || b.Choices[0].Message.Content != "hello" || b.Choices[0].FinishReason != "stop" {
						t.Fatalf("invalid normalized completion: %s", resp.Payload)
					}
				} else {
					r, err := e.ExecuteStream(ctx, a, request(model, true), opts)
					if err != nil {
						t.Fatal(err)
					}
					var all bytes.Buffer
					for {
						select {
						case chunk, ok := <-r.Chunks:
							if !ok {
								goto drained
							}
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
							all.Write(chunk.Payload)
						case <-ctx.Done():
							t.Fatal("native stream did not terminate")
						}
					}
				drained:
					if !bytes.Contains(all.Bytes(), []byte("[DONE]")) {
						t.Fatalf("missing final stream marker: %s", all.Bytes())
					}
					id := ""
					answer := ""
					for _, line := range strings.Split(all.String(), "\n") {
						if !strings.HasPrefix(line, "data: ") || strings.Contains(line, "[DONE]") {
							continue
						}
						var b struct {
							ID      string
							Model   string
							Object  string
							Choices []struct {
								Delta        struct{ Content string }
								FinishReason *string `json:"finish_reason"`
							}
						}
						if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &b); err != nil {
							t.Fatal(err)
						}
						if b.Model != model || b.Object != "chat.completion.chunk" || len(b.Choices) != 1 {
							t.Fatalf("invalid chunk: %s", line)
						}
						if id != "" && b.ID != id {
							t.Fatal("unstable completion ID")
						}
						id = b.ID
						answer += b.Choices[0].Delta.Content
						if f := b.Choices[0].FinishReason; f != nil && *f != "stop" {
							t.Fatal("non-OpenAI finish reason")
						}
					}
					if answer != "hello" {
						t.Fatalf("content = %q", answer)
					}
				}
				if calls.Load() == 0 {
					t.Fatal("actual host never used request-scoped fixture transport")
				}
			})
		}
	}
}

func TestNativeHostSafeUpstreamStatuses(t *testing.T) {
	h, e := setup(t)
	a := account(t, h, "kimi")
	for _, code := range []int{401, 403, 429, 502} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var calls atomic.Int32
			ctx, cancel := context.WithTimeout(fixtureContext(t, "kimi", code, &calls), 5*time.Second)
			defer cancel()
			_, err := e.Execute(ctx, a, request("web-kimi/k3", false), executor.Options{SourceFormat: translator.Format("openai")})
			if err == nil {
				t.Fatal("upstream failure reported success")
			}
			if strings.Contains(err.Error(), "fixture-token") {
				t.Fatal("upstream secret leaked")
			}
			var status interface{ StatusCode() int }
			if !errors.As(err, &status) || status.StatusCode() != code {
				t.Fatalf("status not preserved: %T %v", err, err)
			}
		})
	}
}

func TestNativeHostUnloadActiveStream(t *testing.T) {
	h, e := setup(t)
	a := account(t, h, "kimi")
	closed := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(transport(func(r *http.Request) (*http.Response, error) {
		reader, writer := io.Pipe()
		go func() {
			defer close(closed)
			defer writer.Close()
			_, _ = writer.Write(connect(map[string]any{"op": "set", "mask": "block.text", "block": map[string]any{"text": map[string]string{"content": "hello"}}}, 0))
			// Read cancellation or explicit stream close must unblock the fixture body.
			<-r.Context().Done()
			_ = writer.CloseWithError(r.Context().Err())
		}()
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: reader, Request: r}, nil
	})))
	r, err := e.ExecuteStream(ctx, a, request("web-kimi/k3", true), executor.Options{SourceFormat: translator.Format("openai")})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case chunk := <-r.Chunks:
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
	case <-ctx.Done():
		t.Fatal("native stream did not start")
	}
	// Drain output concurrently so shutdown cannot be held by consumer backpressure.
	outputClosed := make(chan struct{})
	go func() {
		for range r.Chunks {
		}
		close(outputClosed)
	}()
	unloaded := make(chan bool, 1)
	go func() { unloaded <- h.UnloadPluginContext(ctx, "web-accounts") }()
	select {
	case ok := <-unloaded:
		if !ok {
			t.Fatal("native unload refused active stream")
		}
	case <-ctx.Done():
		t.Fatal("plugin unload hung with active upstream")
	}
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("upstream request outlived plugin unload")
	}
	select {
	case <-outputClosed:
	case <-ctx.Done():
		t.Fatal("output outlived plugin unload")
	}
	if len(h.RegisteredPlugins()) != 0 {
		t.Fatal("unloaded plugin remained registered")
	}
}
