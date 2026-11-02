// Comando mlhistory siembra histórico profundo de WEEX en SQLite para el
// entrenamiento de la red neuronal. Usa /historyKlines (paginado y con rangos)
// en lugar de /klines, que solo entrega las últimas 1000 velas.
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/ingest"
	"tradingview-bot/internal/store"
)

type report struct {
	Symbol    string `json:"symbol"`
	Timeframe string `json:"timeframe"`
	Requested int    `json:"requested"`
	Stored    int    `json:"stored"`
	FirstTS   string `json:"first_ts,omitempty"`
	LastTS    string `json:"last_ts,omitempty"`
	Gaps      int    `json:"gaps"`
	CSV       string `json:"csv,omitempty"`
	Error     string `json:"error,omitempty"`
}

func main() {
	dbPath := flag.String("db", "data/bot.db", "ruta a la base SQLite")
	symbol := flag.String("symbol", "BTCUSDT", "símbolo")
	timeframe := flag.String("timeframe", "1h", "timeframe")
	bars := flag.Int("bars", 8000, "velas históricas a descargar")
	restURL := flag.String("rest-url", "https://api-contract.weex.com", "REST de WEEX")
	csvPath := flag.String("csv", "", "exportar CSV (ts,open,high,low,close,volume)")
	skipInvalid := flag.Bool("skip-invalid", false, "descartar velas históricas corruptas en vez de abortar")
	timeout := flag.Duration("timeout", 10*time.Minute, "timeout global")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	rep := report{Symbol: *symbol, Timeframe: *timeframe, Requested: *bars, CSV: *csvPath}
	if err := run(ctx, *dbPath, *symbol, *timeframe, *bars, *restURL, *csvPath, *skipInvalid, &rep); err != nil {
		rep.Error = err.Error()
	}
	b, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(b))
	if rep.Error != "" {
		os.Exit(1)
	}
}

func run(ctx context.Context, dbPath, symbol, timeframe string, bars int, restURL, csvPath string, skipInvalid bool, rep *report) error {
	if bars <= 0 {
		return fmt.Errorf("bars debe ser > 0")
	}
	step, err := timeframeDuration(timeframe)
	if err != nil {
		return err
	}

	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("abriendo db: %w", err)
	}
	defer st.Close()

	provider := ingest.NewWEEXProvider(st, ingest.WeexConfig{RestURL: restURL, SkipInvalidHistory: skipInvalid})
	if err := provider.Backfill(ctx, symbol, timeframe, bars); err != nil {
		return err
	}

	ks, err := st.RecentCandles(ctx, symbol, timeframe, bars)
	if err != nil {
		return fmt.Errorf("leyendo velas: %w", err)
	}
	if err := ingest.ValidateKlinesData(ks); err != nil {
		return fmt.Errorf("validando velas: %w", err)
	}
	rep.Stored = len(ks)
	if len(ks) > 0 {
		rep.FirstTS = ks[0].Start.UTC().Format(time.RFC3339)
		rep.LastTS = ks[len(ks)-1].Start.UTC().Format(time.RFC3339)
		stepMs := step.Milliseconds()
		for i := 1; i < len(ks); i++ {
			if ks[i].Start.UnixMilli()-ks[i-1].Start.UnixMilli() > stepMs*3/2 {
				rep.Gaps++
			}
		}
	}
	if csvPath != "" {
		if err := writeCSV(csvPath, ks); err != nil {
			return err
		}
	}
	if rep.Stored < bars {
		fmt.Fprintf(os.Stderr, "aviso: se almacenaron %d velas de %d pedidas\n", rep.Stored, bars)
	}
	if err := st.Checkpoint(ctx); err != nil {
		return fmt.Errorf("checkpoint final: %w", err)
	}
	return nil
}

func timeframeDuration(tf string) (time.Duration, error) {
	tf = strings.TrimSpace(tf)
	if len(tf) < 2 {
		return 0, fmt.Errorf("timeframe inválido: %q", tf)
	}
	n, err := strconv.Atoi(tf[:len(tf)-1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("timeframe inválido: %q", tf)
	}
	var unit time.Duration
	switch tf[len(tf)-1] {
	case 'm':
		unit = time.Minute
	case 'h':
		unit = time.Hour
	case 'd':
		unit = 24 * time.Hour
	case 'w':
		unit = 7 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("timeframe inválido: %q", tf)
	}
	return time.Duration(n) * unit, nil
}

func writeCSV(path string, ks []domain.Kline) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creando CSV: %w", err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"ts", "open", "high", "low", "close", "volume"}); err != nil {
		return err
	}
	for _, k := range ks {
		row := []string{
			strconv.FormatInt(k.Start.UnixMilli(), 10),
			strconv.FormatFloat(k.Open, 'f', -1, 64),
			strconv.FormatFloat(k.High, 'f', -1, 64),
			strconv.FormatFloat(k.Low, 'f', -1, 64),
			strconv.FormatFloat(k.Close, 'f', -1, 64),
			strconv.FormatFloat(k.Volume, 'f', -1, 64),
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	return w.Error()
}
