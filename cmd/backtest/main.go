// Command backtest ejecuta la comparativa de la Fase 6: buy&hold vs
// Chandelier Exit (Go) vs modelo ML (sidecar), con costos realistas, y genera
// un reporte JSON + tabla y opcionalmente un dashboard HTML.
//
// Uso:
//
//	go run ./cmd/backtest --db data/bot.db --symbol ETHUSDT --timeframe 1h
//	go run ./cmd/backtest --csv candles.csv --fee 0.001 --slippage 0.0005
//	go run ./cmd/backtest --synthetic --bars 17520 --ml --ml-url http://127.0.0.1:8099
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"tradingview-bot/internal/backtest"
	"tradingview-bot/internal/benchmark"
	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/ml"
	"tradingview-bot/internal/store"
)

func main() {
	var (
		dbPath    = flag.String("db", "", "ruta a la base SQLite (data/bot.db)")
		csvPath   = flag.String("csv", "", "ruta a CSV con columnas ts,open,high,low,close,volume")
		synthetic = flag.Bool("synthetic", false, "generar velas sintéticas (random walk)")
		bars      = flag.Int("bars", 8760, "número de velas (sintéticas o límite al leer de DB/CSV)")
		symbol    = flag.String("symbol", "ETHUSDT", "símbolo")
		timeframe = flag.String("timeframe", "1h", "timeframe")
		balance   = flag.Float64("balance", 10000, "balance inicial")
		feePct    = flag.Float64("fee", 0.001, "comisión por lado (0.001 = 0.1%)")
		slippage  = flag.Float64("slippage", 0.0005, "slippage por lado (0.0005)")
		stopPct   = flag.Float64("stop", 0.0, "stop loss desde entrada (0 = sin stop)")
		takePct   = flag.Float64("take", 0.0, "take profit desde entrada (0 = sin take)")
		withML    = flag.Bool("ml", false, "incluir estrategia ML vía sidecar")
		mlURL     = flag.String("ml-url", "http://127.0.0.1:8099", "URL del sidecar ML")
		mlConf    = flag.Float64("ml-confidence", 0.6, "umbral de confianza para señales ML")
		outJSON   = flag.String("out", "report.json", "salida JSON del reporte (vacío = solo stdout)")
		outHTML   = flag.String("html", "", "dashboard HTML (vacío = no generar)")
		seed      = flag.Int64("seed", 42, "semilla para velas sintéticas")
	)
	flag.Parse()

	candles, err := loadCandles(*dbPath, *csvPath, *synthetic, *symbol, *timeframe, *bars, *seed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error cargando velas: %v\n", err)
		os.Exit(1)
	}
	if len(candles) < 2 {
		fmt.Fprintln(os.Stderr, "no hay suficientes velas (mínimo 2). Prueba --synthetic")
		os.Exit(1)
	}

	cfg := backtest.Config{
		InitialBalance: *balance,
		FeePct:         *feePct,
		SlippagePct:    *slippage,
		StopPct:        *stopPct,
		TakePct:        *takePct,
		AnnualBars:     8760, // velas/hora → velas/año
	}

	var mlc *ml.Client
	if *withML {
		mlc = ml.NewClient(ml.Config{URL: *mlURL})
		if err := mlc.Health(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "sidecar ML no disponible: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "sidecar ML ok: evaluando señales por barra...")
	}

	rep, err := benchmark.Run(candles, cfg, &benchmark.Runner{
		MLClient:     mlc,
		MLConfidence: *mlConf,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error en el backtest: %v\n", err)
		os.Exit(1)
	}
	if *withML {
		rep.MLName = fmt.Sprintf("ml-xgboost (conf≥%.2f)", *mlConf)
	}

	fmt.Print(rep.Text())
	fmt.Printf("\n**Mejor estrategia:** %s\n", rep.Best())

	if *outJSON != "" {
		if err := writeJSON(*outJSON, rep); err != nil {
			fmt.Fprintf(os.Stderr, "error escribiendo JSON: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nReporte guardado en %s\n", *outJSON)
	}
	if *outHTML != "" {
		if err := writeDashboard(*outHTML, rep, candles); err != nil {
			fmt.Fprintf(os.Stderr, "error escribiendo dashboard: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Dashboard guardado en %s\n", *outHTML)
	}
}

func loadCandles(dbPath, csvPath string, synthetic bool, symbol, timeframe string, bars int, seed int64) ([]domain.Kline, error) {
	switch {
	case dbPath != "":
		s, err := store.Open(dbPath)
		if err != nil {
			return nil, err
		}
		defer s.Close()
		return s.RecentCandles(context.Background(), symbol, timeframe, bars)
	case csvPath != "":
		return loadCSV(csvPath, symbol, timeframe, bars)
	default:
		return syntheticCandles(symbol, timeframe, bars, seed)
	}
}

func loadCSV(path, symbol, timeframe string, limit int) ([]domain.Kline, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	need := []string{"ts", "open", "high", "low", "close", "volume"}
	for _, n := range need {
		if _, ok := col[n]; !ok {
			return nil, fmt.Errorf("CSV: falta la columna %q (columnas: %s)", n, strings.Join(header, ","))
		}
	}
	var out []domain.Kline
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		ts, err := parseTS(rec[col["ts"]])
		if err != nil {
			return nil, err
		}
		var k domain.Kline
		k.Symbol, k.Timeframe, k.Start, k.Closed = symbol, timeframe, ts, true
		if k.Open, err = strconv.ParseFloat(rec[col["open"]], 64); err != nil {
			return nil, err
		}
		if k.High, err = strconv.ParseFloat(rec[col["high"]], 64); err != nil {
			return nil, err
		}
		if k.Low, err = strconv.ParseFloat(rec[col["low"]], 64); err != nil {
			return nil, err
		}
		if k.Close, err = strconv.ParseFloat(rec[col["close"]], 64); err != nil {
			return nil, err
		}
		if k.Volume, err = strconv.ParseFloat(rec[col["volume"]], 64); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

func parseTS(s string) (time.Time, error) {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e12 { // ms
			return time.UnixMilli(n), nil
		}
		return time.Unix(n, 0), nil
	}
	return time.Parse(time.RFC3339, s)
}

func syntheticCandles(symbol, timeframe string, bars int, seed int64) ([]domain.Kline, error) {
	if bars < 2 {
		bars = 2
	}
	rng := rand.New(rand.NewSource(seed))
	start := time.Now().UTC().Truncate(time.Hour).Add(-time.Duration(bars) * time.Hour)
	price := 3000.0
	vol := rng.Float64()
	prevVol := 0.0
	out := make([]domain.Kline, 0, bars)
	for i := 0; i < bars; i++ {
		drift := 0.0002
		noise := (rng.NormFloat64() - 0.02) * 0.02
		change := drift + noise
		prevVol = vol
		vol = math.Max(0.5, math.Min(2.5, vol+change))
		open := price
		close := open * (1 + change)
		hi := math.Max(open, close) * (1 + rng.Float64()*0.008)
		lo := math.Min(open, close) * (1 - rng.Float64()*0.008)
		out = append(out, domain.Kline{
			Symbol:    symbol,
			Timeframe: timeframe,
			Start:     start.Add(time.Duration(i) * time.Hour),
			Open:      open,
			High:      hi,
			Low:       lo,
			Close:     close,
			Volume:    prevVol * 1000 * (1 + rng.Float64()),
			Closed:    true,
		})
		price = close
	}
	return out, nil
}

func writeJSON(path string, rep *benchmark.Report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}
