package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTimeframeDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"1m":  time.Minute,
		"15m": 15 * time.Minute,
		"1h":  time.Hour,
		"4h":  4 * time.Hour,
		"1d":  24 * time.Hour,
		"1w":  7 * 24 * time.Hour,
	}
	for tf, want := range cases {
		got, err := timeframeDuration(tf)
		if err != nil {
			t.Fatalf("timeframeDuration(%q): %v", tf, err)
		}
		if got != want {
			t.Errorf("timeframeDuration(%q) = %v, want %v", tf, got, want)
		}
	}
	for _, bad := range []string{"", "h", "0h", "1x", "abc"} {
		if _, err := timeframeDuration(bad); err == nil {
			t.Errorf("timeframeDuration(%q) debería fallar", bad)
		}
	}
}

func TestRunDownloadsAndExportsCSV(t *testing.T) {
	const total = 150
	now := time.Now().UTC().Truncate(time.Hour)
	bars := make([]int64, 0, total+11)
	for i := total + 10; i >= 0; i-- {
		bars = append(bars, now.Add(-time.Duration(i)*time.Hour).UnixMilli())
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/capi/v3/market/historyKlines" {
			t.Errorf("path = %q", r.URL.Path)
		}
		start, _ := strconv.ParseInt(r.URL.Query().Get("startTime"), 10, 64)
		end, _ := strconv.ParseInt(r.URL.Query().Get("endTime"), 10, 64)
		var rows []string
		for i := len(bars) - 1; i >= 0 && len(rows) < 100; i-- {
			ts := bars[i]
			if ts < start || ts > end {
				continue
			}
			rows = append(rows, fmt.Sprintf(`[%d,"60000.0","60100.0","59900.0","60050.5","123.45",%d,"60000.0",100,"100.0","200.0","0"]`, ts, ts+3600000))
		}
		w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "bot.db")
	csvPath := filepath.Join(dir, "candles.csv")
	var rep report
	if err := run(context.Background(), dbPath, "BTCUSDT", "1h", total, srv.URL, csvPath, false, &rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.Stored != total {
		t.Errorf("stored = %d, want %d", rep.Stored, total)
	}
	if rep.Gaps != 0 {
		t.Errorf("gaps = %d, want 0", rep.Gaps)
	}
	if rep.FirstTS == "" || rep.LastTS == "" {
		t.Errorf("rango vacío: %+v", rep)
	}

	f, err := os.Open(csvPath)
	if err != nil {
		t.Fatalf("abriendo CSV: %v", err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("leyendo CSV: %v", err)
	}
	if len(rows) != total+1 {
		t.Errorf("filas CSV = %d, want %d", len(rows), total+1)
	}
	if got := strings.Join(rows[0], ","); got != "ts,open,high,low,close,volume" {
		t.Errorf("cabecera CSV = %q", got)
	}
}
