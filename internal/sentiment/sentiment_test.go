package sentiment

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFearAndGreed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"Fear and Greed Index","data":[{"value":"20","value_classification":"Fear","timestamp":"1672515780","time_until_update":"86400"}]}`))
	}))
	defer srv.Close()

	c := New(Config{FearGreedURL: srv.URL, CoinGeckoURL: "http://unused"})
	fg, err := c.FearAndGreed(context.Background())
	if err != nil {
		t.Fatalf("FearAndGreed: %v", err)
	}
	if fg.Value != 20 || fg.Classification != "Fear" {
		t.Errorf("respuesta inválida: %+v", fg)
	}
}

func TestFearAndGreedNoData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"x","data":[]}`))
	}))
	defer srv.Close()

	c := New(Config{FearGreedURL: srv.URL, CoinGeckoURL: "http://unused"})
	if _, err := c.FearAndGreed(context.Background()); err == nil {
		t.Error("sin datos debería fallar")
	}
}

func TestTrending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-cg-demo-api-key") != "secret" {
			t.Errorf("header de API key no enviado")
		}
		w.Write([]byte(`{"coins":[
			{"item":{"id":"bitcoin","name":"Bitcoin","symbol":"btc"}},
			{"item":{"id":"ethereum","name":"Ethereum","symbol":"eth"}},
			{"item":{"id":"solana","name":"Solana","symbol":"sol"}}
		]}`))
	}))
	defer srv.Close()

	c := New(Config{FearGreedURL: "http://unused", CoinGeckoURL: srv.URL, CoinGeckoKey: "secret"})
	got, err := c.Trending(context.Background())
	if err != nil {
		t.Fatalf("Trending: %v", err)
	}
	want := []string{"BTC", "ETH", "SOL"}
	if len(got) != len(want) {
		t.Fatalf("trending = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("trending[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestBuildDigest(t *testing.T) {
	text := BuildDigest(FearGreed{Value: 45, Classification: "Fear"}, []string{"BTC", "ETH"})
	for _, want := range []string{"Sentimiento", "45", "Fear", "BTC", "ETH"} {
		if !strings.Contains(text, want) {
			t.Errorf("digest no contiene %q: %q", want, text)
		}
	}
}
