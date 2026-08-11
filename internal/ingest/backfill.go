package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/throttle"
)

type Backfiller struct {
	client   *http.Client
	baseURL  string
	throttle *throttle.Limiter
	store    domain.CandleStore
}

func NewBackfiller(store domain.CandleStore, baseURL string) *Backfiller {
	return &Backfiller{
		client:   &http.Client{Timeout: 15 * time.Second},
		baseURL:  strings.TrimRight(baseURL, "/"),
		throttle: throttle.New(10, 20),
		store:    store,
	}
}

func (bf *Backfiller) Backfill(ctx context.Context, symbol, timeframe string, since time.Time) (int, error) {
	if since.IsZero() {
		return 0, fmt.Errorf("since requerido")
	}
	start := since.UnixMilli()
	total := 0
	step := parseInterval(timeframe)
	for {
		if err := bf.throttle.Wait(ctx); err != nil {
			return total, err
		}
		bars, err := bf.fetch(ctx, symbol, timeframe, start)
		if err != nil {
			return total, err
		}
		if len(bars) == 0 {
			break
		}
		for _, k := range bars {
			if err := bf.store.SaveCandle(ctx, k); err != nil && !errors.Is(err, domain.ErrDuplicate) {
				return total, err
			}
			total++
		}
		if len(bars) < 1000 {
			break
		}
		start = bars[len(bars)-1].Start.Add(step).UnixMilli()
	}
	return total, nil
}

func (bf *Backfiller) fetch(ctx context.Context, symbol, timeframe string, start int64) ([]domain.Kline, error) {
	u := fmt.Sprintf("%s/api/v3/klines?symbol=%s&interval=%s&startTime=%d&limit=1000",
		bf.baseURL, symbol, timeframe, start)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := bf.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("binance rest %d: %s", resp.StatusCode, string(body))
	}
	return parseKlines(body, symbol, timeframe)
}

func parseKlines(body []byte, symbol, timeframe string) ([]domain.Kline, error) {
	var rows [][]json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("parseando klines: %w", err)
	}
	out := make([]domain.Kline, 0, len(rows))
	for _, row := range rows {
		if len(row) < 6 {
			return nil, fmt.Errorf("fila de kline con %d campos, want ≥6", len(row))
		}
		var openTime int64
		if err := json.Unmarshal(row[0], &openTime); err != nil {
			return nil, err
		}
		var o, h, l, c, v string
		for i, dst := range []*string{&o, &h, &l, &c, &v} {
			if err := json.Unmarshal(row[1+i], dst); err != nil {
				return nil, err
			}
		}
		k := domain.Kline{Symbol: symbol, Timeframe: timeframe, Start: time.UnixMilli(openTime), Closed: true}
		var err error
		if k.Open, err = strconv.ParseFloat(o, 64); err != nil {
			return nil, err
		}
		if k.High, err = strconv.ParseFloat(h, 64); err != nil {
			return nil, err
		}
		if k.Low, err = strconv.ParseFloat(l, 64); err != nil {
			return nil, err
		}
		if k.Close, err = strconv.ParseFloat(c, 64); err != nil {
			return nil, err
		}
		if k.Volume, err = strconv.ParseFloat(v, 64); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

func parseInterval(interval string) time.Duration {
	if len(interval) < 2 {
		return time.Hour
	}
	n, err := strconv.Atoi(interval[:len(interval)-1])
	if err != nil {
		n = 1
	}
	switch interval[len(interval)-1] {
	case 'm':
		return time.Duration(n) * time.Minute
	case 'h':
		return time.Duration(n) * time.Hour
	case 'd':
		return time.Duration(n) * 24 * time.Hour
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour
	default:
		return time.Hour
	}
}
