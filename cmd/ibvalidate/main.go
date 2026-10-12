package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"tradingview-bot/internal/domain"
	"tradingview-bot/internal/ingest"
	"tradingview-bot/internal/investingbulls"
	"tradingview-bot/internal/store"
)

type dataReport struct {
	Timeframe      string `json:"timeframe"`
	Bars           int    `json:"bars"`
	FirstTS        string `json:"first_ts,omitempty"`
	LastTS         string `json:"last_ts,omitempty"`
	Monotonic      bool   `json:"monotonic"`
	Duplicates     int    `json:"duplicates"`
	Gaps           int    `json:"gaps"`
	MaxGapBars     int    `json:"max_gap_bars"`
	Unaligned      int    `json:"unaligned"`
	LastAgeMinutes int    `json:"last_age_minutes"`
	Stale          bool   `json:"stale"`
}

type learnReport struct {
	StrategyID       string   `json:"strategy_id"`
	Status           string   `json:"status"`
	Accepted         bool     `json:"accepted"`
	Reason           string   `json:"reason,omitempty"`
	Trades           int      `json:"trades"`
	WinRate          float64  `json:"win_rate"`
	ProfitFactor     float64  `json:"profit_factor"`
	TotalReturn      float64  `json:"total_return"`
	MaxDrawdown      float64  `json:"max_drawdown"`
	ConfigsEvaluated int      `json:"configs_evaluated"`
	AllowedSetups    []string `json:"allowed_setups,omitempty"`
	ConfirmSkip      string   `json:"confirm_skip,omitempty"`
	CodeVersion      string   `json:"code_version"`
}

type foldReport struct {
	Trades       int     `json:"trades"`
	WinRate      float64 `json:"win_rate"`
	ProfitFactor float64 `json:"profit_factor"`
	TotalReturn  float64 `json:"total_return"`
	MaxDrawdown  float64 `json:"max_drawdown"`
	Bars         int     `json:"bars"`
}

type oosReport struct {
	Passed       bool         `json:"passed"`
	Status       string       `json:"status"`
	Trades       int          `json:"trades"`
	WinRate      float64      `json:"win_rate"`
	ProfitFactor float64      `json:"profit_factor"`
	TotalReturn  float64      `json:"total_return"`
	MaxDrawdown  float64      `json:"max_drawdown"`
	TestBars     int          `json:"test_bars"`
	Folds        []foldReport `json:"folds,omitempty"`
}

type report struct {
	Symbol     string       `json:"symbol"`
	Limit      int          `json:"limit"`
	Backfilled bool         `json:"backfilled"`
	Data       []dataReport `json:"data"`
	Learn      *learnReport `json:"learn,omitempty"`
	OOS        *oosReport   `json:"oos,omitempty"`
	Error      string       `json:"error,omitempty"`
}

func main() {
	dbPath := flag.String("db", "data/bot.db", "ruta a la base SQLite")
	symbol := flag.String("symbol", "BTCUSDT", "símbolo")
	limit := flag.Int("limit", 1000, "velas por timeframe (tope WEEX: 1000)")
	tfsFlag := flag.String("timeframes", "1h,15m,5m", "principal,entrada,confirmación")
	confirm := flag.Bool("confirm", false, "exigir confirmación del tercer timeframe")
	backfill := flag.Bool("backfill", false, "descargar velas desde WEEX")
	restURL := flag.String("rest-url", "https://api-contract.weex.com", "REST de WEEX")
	out := flag.String("out", "", "ruta de salida JSON (vacío = solo stdout)")
	flag.Parse()

	parts := strings.Split(*tfsFlag, ",")
	if len(parts) != 3 {
		fmt.Fprintln(os.Stderr, "timeframes debe ser principal,entrada,confirmación")
		os.Exit(2)
	}
	mtf := investingbulls.DefaultMultiTimeframeConfig()
	mtf.MainTimeframe = strings.TrimSpace(parts[0])
	mtf.EntryTimeframe = strings.TrimSpace(parts[1])
	mtf.ConfirmTimeframe = strings.TrimSpace(parts[2])
	mtf.RequireConfirm = *confirm

	rep := report{Symbol: *symbol, Limit: *limit, Backfilled: *backfill}
	err := run(context.Background(), *dbPath, *symbol, *limit, *restURL, *backfill, mtf, &rep)
	if err != nil {
		rep.Error = err.Error()
	}
	b, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(b))
	if *out != "" {
		if werr := os.WriteFile(*out, b, 0o644); werr != nil {
			fmt.Fprintln(os.Stderr, "escribiendo salida:", werr)
			os.Exit(1)
		}
	}
	if err != nil {
		os.Exit(1)
	}
}

func run(ctx context.Context, dbPath, symbol string, limit int, restURL string, backfill bool, mtf investingbulls.MultiTimeframeConfig, rep *report) error {
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("abriendo db: %w", err)
	}
	defer st.Close()

	timeframes := uniqueTimeframes(mtf)

	if backfill {
		prov := ingest.NewWEEXProvider(st, ingest.WeexConfig{RestURL: restURL})
		for _, tf := range timeframes {
			if err := prov.Backfill(ctx, symbol, tf, limit); err != nil {
				return fmt.Errorf("backfill %s: %w", tf, err)
			}
		}
	}

	for _, tf := range timeframes {
		ks, err := st.RecentCandles(ctx, symbol, tf, limit)
		if err != nil {
			return fmt.Errorf("leyendo velas %s: %w", tf, err)
		}
		rep.Data = append(rep.Data, quality(tf, ks))
	}

	lst, learned, err := investingbulls.LearnMultiTimeframeAndPersist(ctx, st, symbol, limit, investingbulls.DefaultLearnConfig(), mtf)
	if err != nil {
		return fmt.Errorf("learn mtf: %w", err)
	}
	base := learned.Model.Base
	rep.Learn = &learnReport{
		StrategyID:       lst.ID,
		Status:           string(lst.Status),
		Accepted:         learned.Accepted,
		Reason:           learned.Reason,
		Trades:           base.Trades,
		WinRate:          base.WinRate,
		ProfitFactor:     base.ProfitFactor,
		TotalReturn:      base.TotalReturn,
		MaxDrawdown:      base.MaxDrawdown,
		ConfigsEvaluated: base.ConfigsEvaluated,
		AllowedSetups:    setupNames(learned.Model.AllowedSetups),
		ConfirmSkip:      learned.Model.ConfirmSkipReason,
		CodeVersion:      base.CodeVersion,
	}

	bt, err := investingbulls.ValidateMultiTimeframeCandidate(ctx, st, lst.ID, symbol, limit, investingbulls.DefaultWalkForwardConfig())
	if err != nil {
		return fmt.Errorf("validate mtf: %w", err)
	}
	rep.OOS = &oosReport{
		Passed:       bt.Passed,
		Status:       bt.Status,
		Trades:       bt.Trades,
		WinRate:      bt.WinRate,
		ProfitFactor: bt.ProfitFactor,
		TotalReturn:  bt.TotalReturn,
		MaxDrawdown:  bt.MaxDrawdown,
		TestBars:     bt.TestBars,
		Folds:        foldReports(bt.OOSFolds),
	}
	return nil
}

func uniqueTimeframes(mtf investingbulls.MultiTimeframeConfig) []string {
	seen := map[string]bool{}
	var out []string
	for _, tf := range []string{mtf.MainTimeframe, mtf.EntryTimeframe, mtf.ConfirmTimeframe} {
		if tf == "" || seen[tf] {
			continue
		}
		seen[tf] = true
		out = append(out, tf)
	}
	return out
}

func setupNames(setups []investingbulls.SetupType) []string {
	out := make([]string, 0, len(setups))
	for _, s := range setups {
		out = append(out, string(s))
	}
	return out
}

func foldReports(folds []domain.OOSFold) []foldReport {
	out := make([]foldReport, 0, len(folds))
	for _, f := range folds {
		out = append(out, foldReport{
			Trades:       f.Trades,
			WinRate:      f.WinRate,
			ProfitFactor: f.ProfitFactor,
			TotalReturn:  f.TotalReturn,
			MaxDrawdown:  f.MaxDrawdown,
			Bars:         f.Bars,
		})
	}
	return out
}

func quality(tf string, ks []domain.Kline) dataReport {
	q := dataReport{Timeframe: tf, Bars: len(ks), Monotonic: true}
	if len(ks) == 0 {
		return q
	}
	q.FirstTS = ks[0].Start.UTC().Format(time.RFC3339)
	q.LastTS = ks[len(ks)-1].Start.UTC().Format(time.RFC3339)
	step, ok := interval(tf)
	for i := 1; i < len(ks); i++ {
		d := ks[i].Start.Sub(ks[i-1].Start)
		if d <= 0 {
			q.Monotonic = false
		}
		if d == 0 {
			q.Duplicates++
		}
		if ok && d > step {
			q.Gaps++
			if extra := int(d/step) - 1; extra > q.MaxGapBars {
				q.MaxGapBars = extra
			}
		}
	}
	if ok {
		ms := step.Milliseconds()
		for _, k := range ks {
			if k.Start.UnixMilli()%ms != 0 {
				q.Unaligned++
			}
		}
		age := time.Since(ks[len(ks)-1].Start)
		q.LastAgeMinutes = int(age.Minutes())
		q.Stale = age > 2*step
	}
	return q
}

func interval(tf string) (time.Duration, bool) {
	switch tf {
	case "1m":
		return time.Minute, true
	case "5m":
		return 5 * time.Minute, true
	case "15m":
		return 15 * time.Minute, true
	case "30m":
		return 30 * time.Minute, true
	case "1h":
		return time.Hour, true
	case "2h":
		return 2 * time.Hour, true
	case "4h":
		return 4 * time.Hour, true
	case "12h":
		return 12 * time.Hour, true
	case "1d":
		return 24 * time.Hour, true
	case "1w":
		return 7 * 24 * time.Hour, true
	}
	return 0, false
}
