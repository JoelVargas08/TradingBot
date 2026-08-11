package sentiment

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	FearGreedURL string
	CoinGeckoURL string
	CoinGeckoKey string
	HTTPTimeout  time.Duration
}

func (c Config) withDefaults() Config {
	if c.FearGreedURL == "" {
		c.FearGreedURL = "https://api.alternative.me/fng"
	}
	if c.CoinGeckoURL == "" {
		c.CoinGeckoURL = "https://api.coingecko.com/api/v3"
	}
	if c.HTTPTimeout <= 0 {
		c.HTTPTimeout = 10 * time.Second
	}
	return c
}

type Client struct {
	http *http.Client
	cfg  Config
}

func New(cfg Config) *Client {
	cfg = cfg.withDefaults()
	return &Client{http: &http.Client{Timeout: cfg.HTTPTimeout}, cfg: cfg}
}

type FearGreed struct {
	Value          int
	Classification string
}

func (c *Client) FearAndGreed(ctx context.Context) (FearGreed, error) {
	u := c.cfg.FearGreedURL + "/?limit=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return FearGreed{}, err
	}
	body, err := c.do(req)
	if err != nil {
		return FearGreed{}, err
	}
	var resp struct {
		Data []struct {
			Value          string `json:"value"`
			Classification string `json:"value_classification"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return FearGreed{}, err
	}
	if len(resp.Data) == 0 {
		return FearGreed{}, fmt.Errorf("fear&greed sin datos")
	}
	v, err := strconv.Atoi(resp.Data[0].Value)
	if err != nil {
		return FearGreed{}, err
	}
	return FearGreed{Value: v, Classification: resp.Data[0].Classification}, nil
}

func (c *Client) Trending(ctx context.Context) ([]string, error) {
	u := c.cfg.CoinGeckoURL + "/search/trending"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.cfg.CoinGeckoKey != "" {
		req.Header.Set("x-cg-demo-api-key", c.cfg.CoinGeckoKey)
	}
	body, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Coins []struct {
			Item struct {
				Symbol string `json:"symbol"`
			} `json:"item"`
		} `json:"coins"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	var out []string
	for _, coin := range resp.Coins {
		if coin.Item.Symbol == "" {
			continue
		}
		out = append(out, strings.ToUpper(coin.Item.Symbol))
		if len(out) >= 5 {
			break
		}
	}
	return out, nil
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func BuildDigest(fg FearGreed, trending []string) string {
	var b strings.Builder
	b.WriteString("🧠 <b>Sentimiento del mercado</b>\n\n")
	fmt.Fprintf(&b, "Fear & Greed: <b>%d</b> (%s)", fg.Value, fg.Classification)
	if len(trending) > 0 {
		fmt.Fprintf(&b, "\nTrending: %s", strings.Join(trending, ", "))
	}
	return b.String()
}
