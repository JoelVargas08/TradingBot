package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const samplePine = `//@version=6
strategy("Test", shorttitle="T", overlay=true, initial_capital=10000)
emaFast = ta.ema(close, 20)
emaSlow = ta.ema(close, 50)
longCond = ta.crossover(emaFast, emaSlow)
if longCond
    strategy.entry("Long", strategy.long)
alert('{"strategy":"test","action":"buy"}', alert.freq_once_per_bar_close)`

const sampleSpec = `{"symbol":"BTCUSDT","timeframe":"1h","indicators":[{"id":"ema_fast","type":"ema","length":20},{"id":"ema_slow","type":"ema","length":50}],"entries":[{"side":"buy","conditions":[{"left":"ema_fast","op":"cross_above","right":"ema_slow"}]}],"exits":[{"side":"buy","conditions":[{"left":"ema_fast","op":"<","right":"ema_slow"}]}],"stop_pct":3.0,"take_profit_pct":6.0}`

func mockServer(t *testing.T, wantPath, wantBearer string, content string) *httptest.Server {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			t.Errorf("path = %s, want %s", r.URL.Path, wantPath)
		}
		if wantBearer != "" && r.Header.Get("Authorization") != "Bearer "+wantBearer {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		if wantBearer == "" && r.Header.Get("x-api-key") == "" {
			t.Errorf("falta x-api-key para anthropic")
		}
		w.Header().Set("Content-Type", "application/json")
		body := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": content}},
			},
		}
		json.NewEncoder(w).Encode(body)
	})
	return httptest.NewServer(h)
}

func TestGeneratePineScript(t *testing.T) {
	artifact, _ := json.Marshal(PineScriptResult{
		Name:        "Estrategia Test",
		Description: "EMA crossover",
		PineScript:  samplePine,
		Spec:        sampleSpec,
	})
	srv := mockServer(t, "/v1/chat/completions", "sk-test", string(artifact))
	defer srv.Close()

	client := New(Config{
		Provider: ProviderOpenAI,
		APIKey:   "sk-test",
		Model:    "gpt-4o",
		BaseURL:  srv.URL + "/v1",
	})
	res, err := client.GeneratePineScript(context.Background(), "Compra cuando EMA20 cruza EMA50")
	if err != nil {
		t.Fatalf("GeneratePineScript: %v", err)
	}
	if !strings.Contains(res.PineScript, "//@version=6") {
		t.Errorf("pinescript sin version v6: %q", res.PineScript)
	}
	if res.Name == "" || res.Spec == "" {
		t.Errorf("artefacto incompleto: %+v", res)
	}
}

func TestGeneratePineScriptCodeFence(t *testing.T) {
	artifact, _ := json.Marshal(PineScriptResult{
		Name:       "X",
		PineScript: samplePine,
		Spec:       sampleSpec,
	})
	raw := "```json\n" + string(artifact) + "\n```"
	srv := mockServer(t, "/v1/chat/completions", "sk-test", raw)
	defer srv.Close()

	client := New(Config{Provider: ProviderOpenAI, APIKey: "sk-test", BaseURL: srv.URL + "/v1"})
	res, err := client.GeneratePineScript(context.Background(), "texto")
	if err != nil {
		t.Fatalf("GeneratePineScript: %v", err)
	}
	if res.PineScript == "" {
		t.Error("pinescript vacío")
	}
}

func TestGeneratePineScriptAnthropic(t *testing.T) {
	artifact, _ := json.Marshal(PineScriptResult{
		Name:       "X",
		PineScript: samplePine,
		Spec:       sampleSpec,
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"text": string(artifact)}},
		})
	}))
	defer srv.Close()

	client := New(Config{Provider: ProviderAnthropic, APIKey: "ak", Model: "claude-3-5-sonnet", BaseURL: srv.URL})
	res, err := client.GeneratePineScript(context.Background(), "texto")
	if err != nil {
		t.Fatalf("GeneratePineScript: %v", err)
	}
	if res.PineScript == "" {
		t.Error("pinescript vacío")
	}
}

func TestGeneratePineScriptInvalidJSON(t *testing.T) {
	srv := mockServer(t, "/v1/chat/completions", "sk", "esto no es json")
	defer srv.Close()
	client := New(Config{Provider: ProviderOpenAI, APIKey: "sk", BaseURL: srv.URL + "/v1"})
	if _, err := client.GeneratePineScript(context.Background(), "texto"); err == nil {
		t.Fatal("debería fallar con JSON inválido")
	}
}

func TestGeneratePineScriptEmptyText(t *testing.T) {
	client := New(Config{Provider: ProviderOpenAI, APIKey: "sk"})
	if _, err := client.GeneratePineScript(context.Background(), "   "); err == nil {
		t.Fatal("debería fallar con texto vacío")
	}
}

func TestHTTPErrorPropagated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"rate limit"}}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()
	client := New(Config{Provider: ProviderOpenAI, APIKey: "sk", BaseURL: srv.URL + "/v1"})
	if _, err := client.GeneratePineScript(context.Background(), "texto"); err == nil {
		t.Fatal("debería fallar con HTTP 429")
	}
}
