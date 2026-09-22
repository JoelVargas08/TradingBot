package investingbulls

import (
    "context"
    "encoding/json"
    "fmt"
    "math"
    "sort"
    "time"

    "tradingview-bot/internal/domain"
)

type MultiTimeframeConfig struct {
    MainTimeframe string
    EntryTimeframe string
    ConfirmTimeframe string
    RequireConfirm bool
    MainSwingLeft int
    MainSwingRight int
    EntrySwingLeft int
    EntrySwingRight int
    ConfirmSwingLeft int
    ConfirmSwingRight int
}

func DefaultMultiTimeframeConfig() MultiTimeframeConfig {
    return MultiTimeframeConfig{MainTimeframe:"1h", EntryTimeframe:"15m", ConfirmTimeframe:"5m", MainSwingLeft:2, MainSwingRight:2, EntrySwingLeft:2, EntrySwingRight:2, ConfirmSwingLeft:2, ConfirmSwingRight:2}
}

type MultiTimeframeModel struct {
    Version int
    Family string
    Symbol string
    Timeframes MultiTimeframeConfig
    Base LearnedModel
    AllowedSetups []SetupType
    SetupStats map[SetupType]SetupStats
    LearnedAt time.Time
}

type SetupStats struct { Trades int; WinRate float64; ProfitFactor float64; TotalReturn float64; MaxDrawdown float64 }
type MultiTimeframeLearnResult struct { Model MultiTimeframeModel; Trades []LearnedTrade; Accepted bool; Reason string; SpecJSON string }

func normalizeMTF(c MultiTimeframeConfig) MultiTimeframeConfig {
    d:=DefaultMultiTimeframeConfig()
    if c.MainTimeframe=="" {c.MainTimeframe=d.MainTimeframe}; if c.EntryTimeframe=="" {c.EntryTimeframe=d.EntryTimeframe}; if c.ConfirmTimeframe=="" {c.ConfirmTimeframe=d.ConfirmTimeframe}
    if c.MainSwingLeft<=0 {c.MainSwingLeft=2}; if c.MainSwingRight<=0 {c.MainSwingRight=2}; if c.EntrySwingLeft<=0 {c.EntrySwingLeft=2}; if c.EntrySwingRight<=0 {c.EntrySwingRight=2}; if c.ConfirmSwingLeft<=0 {c.ConfirmSwingLeft=2}; if c.ConfirmSwingRight<=0 {c.ConfirmSwingRight=2}
    return c
}

func LearnMultiTimeframe(main, entry, confirm []domain.Kline, cfg LearnConfig, mtf MultiTimeframeConfig) (MultiTimeframeLearnResult,error) {
    mtf=normalizeMTF(mtf); if len(main)<100 || len(entry)<100 {return MultiTimeframeLearnResult{},fmt.Errorf("learn mtf: se necesitan al menos 100 velas en 1H y 15m")}
    cfg.Timeframe=mtf.EntryTimeframe
    base,err:=Learn(entry,cfg); if err!=nil{return MultiTimeframeLearnResult{},err}
    if base.SpecJSON=="" { return MultiTimeframeLearnResult{},fmt.Errorf("learn mtf: el aprendizaje base no produjo un candidato") }
    allowed:=[]SetupType{SetupCHOCHLong,SetupCHOCHShort,SetupContinuationLong,SetupContinuationShort}
    trades:=simulateMultiTimeframe(main,entry,confirm,cfg,mtf,base.Model,allowed)
    stats:=statsBySetup(trades,cfg.InitialBalance)
    kept:=make([]SetupType,0,4); for _,s:=range allowed {if m,ok:=stats[s];ok && m.Trades>=2 && m.ProfitFactor>=1 {kept=append(kept,s)}}
    if len(kept)==0 {kept=allowed}
    trades=filterTradesBySetup(trades,kept); m:=tradeMetrics(trades,cfg.InitialBalance)
    model:=MultiTimeframeModel{Version:1,Family:"investing_bulls_mtf",Symbol:cfg.Symbol,Timeframes:mtf,Base:base.Model,AllowedSetups:kept,SetupStats:stats,LearnedAt:time.Now().UTC()}
    raw,err:=json.MarshalIndent(model,"","  "); if err!=nil{return MultiTimeframeLearnResult{},err}
    accepted:=m.trades>=cfg.MinTrades && m.profitFactor>1; reason:="candidato MTF generado; requiere validación OOS"; if !accepted {reason="candidato MTF generado pero no supera el filtro in-sample"}
    return MultiTimeframeLearnResult{Model:model,Trades:trades,Accepted:accepted,Reason:reason,SpecJSON:string(raw)},nil
}

func simulateMultiTimeframe(main,entry,confirm []domain.Kline,cfg LearnConfig,mtf MultiTimeframeConfig,base LearnedModel,allowed []SetupType) []LearnedTrade {
    allow:=map[SetupType]bool{}; for _,s:=range allowed {allow[s]=true}; var out []LearnedTrade; inTrade:=false; var open LearnedTrade; stop,target:=0.0,0.0
    for i:=20;i<len(entry)-1;i++ {
        if inTrade {
            k:=entry[i]; hitStop,hitTarget:=false,false
            if open.Direction==domain.DirectionBuy {hitStop=k.Low<=stop;hitTarget=k.High>=target} else {hitStop=k.High>=stop;hitTarget=k.Low<=target}
            if hitStop||hitTarget {exit,reason:=stop,"stop";if hitTarget&&!hitStop {exit,reason=target,"take"};open.ExitBar=i;open.ExitPrice=exit;open.Reason=reason;open.PnL=tradePnL(open.Direction,open.EntryPrice,exit)-2*cfg.FeePct-2*cfg.SlippagePct;out=append(out,open);inTrade=false}; continue
        }
        mainPrefix:=candlesThrough(main,entry[i].Start); if len(mainPrefix)<30 {continue}; ms:=Analyze(mainPrefix,mtf.MainSwingLeft,mtf.MainSwingRight); if ms.Trend!=TrendBullish&&ms.Trend!=TrendBearish {continue}
        ep:=entry[:i+1]; es:=Analyze(ep,base.SwingLeft,base.SwingRight); cs:=ClassifySetups(es); var candidate *ClassifiedSetup
        for j:=len(cs)-1;j>=0;j-- {c:=cs[j];if c.Index>=i {continue};if !allow[c.SetupType] {continue};if c.Direction==domain.DirectionBuy&&ms.Trend!=TrendBullish {continue};if c.Direction==domain.DirectionSell&&ms.Trend!=TrendBearish {continue};candidate=&c;break}
        if candidate==nil {continue}
        fib,ok:=fibonacciFromLatestImpulse(es,base.Fib);if !ok {continue}
        ic:=DefaultImbalanceConfig();imbs:=UpdateImbalances(DetectImbalances(ep,ic),ep,ic);bc:=DefaultOrderBlockConfig();blocks:=UpdateOrderBlocks(DetectOrderBlocks(ep,es.Breaks,bc),ep,bc)
        setups:=EvaluateConfluence(ep,es,fib,imbs,blocks,base.Confluence);valid:=false;var matched Setup
        for _,s:=range setups {if s.Index==i&&s.Direction==candidate.Direction&&s.Valid {valid=true;matched=s;break}};if !valid {continue}
        if mtf.RequireConfirm&&!confirmationMatches(confirm,entry[i].Start,candidate.Direction,mtf){continue}
        epPrice:=entry[i+1].Open;if epPrice<=0 {epPrice=entry[i+1].Close};plan,ok:=buildLearningPlan(matched,fib,epPrice,es,blocks,base.TradePlan);if !ok {continue}
        open=LearnedTrade{Direction:candidate.Direction,EntryBar:i+1,EntryPrice:epPrice*entryMultiplier(candidate.Direction,cfg.SlippagePct),Setup:candidate.SetupType};stop,target=plan.StopLoss,plan.TakeProfit;inTrade=true
    }
    if inTrade {last:=entry[len(entry)-1];open.ExitBar=len(entry)-1;open.ExitPrice=last.Close;open.Reason="end";open.PnL=tradePnL(open.Direction,open.EntryPrice,last.Close)-2*cfg.FeePct-2*cfg.SlippagePct;out=append(out,open)}
    return out
}

func candlesThrough(ks []domain.Kline,ts time.Time) []domain.Kline {n:=sort.Search(len(ks),func(i int)bool{return !ks[i].Start.Before(ts)});if n==0{return nil};if n<len(ks)&&ks[n].Start.Equal(ts){return ks[:n+1]};return ks[:n]}
func confirmationMatches(ks []domain.Kline,ts time.Time,dir domain.Direction,c MultiTimeframeConfig) bool {p:=candlesThrough(ks,ts);if len(p)<10{return false};s:=Analyze(p,c.ConfirmSwingLeft,c.ConfirmSwingRight);if len(s.Breaks)==0{return false};return s.Breaks[len(s.Breaks)-1].Direction==dir}

func statsBySetup(trades []LearnedTrade,initial float64) map[SetupType]SetupStats {groups:=map[SetupType][]LearnedTrade{};for _,t:=range trades {s:=t.Setup;if s.Valid(){groups[s]=append(groups[s],t)}};out:=map[SetupType]SetupStats{};for s,ts:=range groups {m:=tradeMetrics(ts,initial);out[s]=SetupStats{Trades:m.trades,WinRate:m.winRate,ProfitFactor:m.profitFactor,TotalReturn:m.totalReturn,MaxDrawdown:m.maxDrawdown}};return out}
func filterTradesBySetup(trades []LearnedTrade,allowed []SetupType) []LearnedTrade {set:=map[SetupType]bool{};for _,s:=range allowed{set[s]=true};out:=make([]LearnedTrade,0,len(trades));for _,t:=range trades{if set[t.Setup]{out=append(out,t)}};return out}

func LearnMultiTimeframeAndPersist(ctx context.Context,store LearnerStore,symbol string,limit int,cfg LearnConfig,mtf MultiTimeframeConfig)(domain.Strategy,MultiTimeframeLearnResult,error){
    mtf=normalizeMTF(mtf);if limit<=0{limit=5000};main,err:=store.RecentCandles(ctx,symbol,mtf.MainTimeframe,limit);if err!=nil{return domain.Strategy{},MultiTimeframeLearnResult{},fmt.Errorf("1H: %w",err)};entry,err:=store.RecentCandles(ctx,symbol,mtf.EntryTimeframe,limit);if err!=nil{return domain.Strategy{},MultiTimeframeLearnResult{},fmt.Errorf("15m: %w",err)};var confirm []domain.Kline;if mtf.RequireConfirm{confirm,err=store.RecentCandles(ctx,symbol,mtf.ConfirmTimeframe,limit);if err!=nil{return domain.Strategy{},MultiTimeframeLearnResult{},fmt.Errorf("5m: %w",err)}}
    main=closedCandles(main);entry=closedCandles(entry);confirm=closedCandles(confirm)
    cfg.Symbol=symbol;result,err:=LearnMultiTimeframe(main,entry,confirm,cfg,mtf);if err!=nil{return domain.Strategy{},result,err};now:=time.Now().UTC();id:=fmt.Sprintf("investing-bulls-mtf-%s-%d",symbol,now.Unix());st:=domain.Strategy{ID:id,Name:fmt.Sprintf("Investing Bulls MTF %s",symbol),Description:"1H dirección + 15m entrada + 5m confirmación opcional; CHOCH/BOS, Fibonacci, imbalance y order block.",Status:domain.StrategyCandidate,Source:"investing_bulls_learn_mtf",Spec:result.SpecJSON,CreatedAt:now,UpdatedAt:now};if err:=store.UpsertStrategy(ctx,st);err!=nil{return domain.Strategy{},result,err};m:=tradeMetrics(result.Trades,cfg.InitialBalance);bt:=domain.BacktestResult{StrategyID:id,Trades:m.trades,WinRate:m.winRate,ProfitFactor:m.profitFactor,MaxDrawdown:m.maxDrawdown,TotalReturn:m.totalReturn,TestBars:len(entry),Passed:result.Accepted,Status:"in_sample_candidate",MetricsAt:now};if err:=store.SaveBacktest(ctx,bt);err!=nil{return domain.Strategy{},result,err};return st,result,nil
}

func ValidateMultiTimeframeCandidate(ctx context.Context,store LearnerStore,strategyID,symbol string,limit int,wcfg WalkForwardConfig)(domain.BacktestResult,error){
    st,err:=store.GetStrategy(ctx,strategyID);if err!=nil{return domain.BacktestResult{},err};var model MultiTimeframeModel;if err=json.Unmarshal([]byte(st.Spec),&model);err!=nil{return domain.BacktestResult{},fmt.Errorf("mtf spec: %w",err)};if model.Symbol!=""&&model.Symbol!=symbol{return domain.BacktestResult{},fmt.Errorf("mtf symbol no coincide")};main,err:=store.RecentCandles(ctx,symbol,model.Timeframes.MainTimeframe,limit);if err!=nil{return domain.BacktestResult{},err};entry,err:=store.RecentCandles(ctx,symbol,model.Timeframes.EntryTimeframe,limit);if err!=nil{return domain.BacktestResult{},err};var confirm []domain.Kline;if model.Timeframes.RequireConfirm{confirm,err=store.RecentCandles(ctx,symbol,model.Timeframes.ConfirmTimeframe,limit);if err!=nil{return domain.BacktestResult{},err}}
    main=closedCandles(main);entry=closedCandles(entry);confirm=closedCandles(confirm)
    wcfg=normalizeWalkForwardConfig(wcfg);baseCfg:=cfgFromModel(model.Base);var all []LearnedTrade;var folds []domain.OOSFold;positive:=0
    for n:=0;n<wcfg.Folds;n++ {sp:=wcfg.TrainPct+float64(n)*wcfg.StepPct;ep:=sp+wcfg.OOSPct;start:=int(math.Floor(float64(len(entry))*sp));end:=int(math.Floor(float64(len(entry))*ep));if start<1||end>len(entry)||end<=start{break};tr:=simulateMultiTimeframe(candlesThrough(main,entry[end-1].Start),entry[:end],confirmThrough(confirm,entry[end-1].Start),baseCfg,model.Timeframes,model.Base,model.AllowedSetups);var ft []LearnedTrade;for _,t:=range tr{if t.EntryBar>=start&&t.EntryBar<end{ft=append(ft,t)}};m:=tradeMetrics(ft,baseCfg.InitialBalance);folds=append(folds,domain.OOSFold{Trades:m.trades,WinRate:m.winRate,ProfitFactor:m.profitFactor,MaxDrawdown:m.maxDrawdown,TotalReturn:m.totalReturn,Bars:end-start});all=append(all,ft...);if m.trades>=wcfg.MinOOSTrades&&m.profitFactor>=wcfg.MinOOSProfitFactor&&m.maxDrawdown<=wcfg.MaxOOSDrawdown{positive++}}
    m:=tradeMetrics(all,baseCfg.InitialBalance);passed:=len(folds)>0&&positive>=wcfg.MinPositiveFolds&&m.trades>=wcfg.MinOOSTrades&&m.profitFactor>=wcfg.MinOOSProfitFactor&&m.maxDrawdown<=wcfg.MaxOOSDrawdown;now:=time.Now().UTC();bt:=domain.BacktestResult{StrategyID:strategyID,Trades:m.trades,WinRate:m.winRate,ProfitFactor:m.profitFactor,MaxDrawdown:m.maxDrawdown,TotalReturn:m.totalReturn,TestBars:len(entry),Passed:passed,Folds:len(folds),OOSFolds:folds,Status:"oos_fixed_candidate",MetricsAt:now};if err:=store.SaveBacktest(ctx,bt);err!=nil{return domain.BacktestResult{},err};st.UpdatedAt=now;if passed{st.Status=domain.StrategyActive;st.Error=""}else{st.Status=domain.StrategyRejected;st.Error=fmt.Sprintf("OOS fijo rechazado: %d/%d folds positivos",positive,len(folds))};if err:=store.UpsertStrategy(ctx,st);err!=nil{return domain.BacktestResult{},err};return bt,nil
}
func confirmThrough(ks []domain.Kline,ts time.Time) []domain.Kline{return candlesThrough(ks,ts)}
func cfgFromModel(m LearnedModel) LearnConfig{return LearnConfig{Symbol:m.Symbol,Timeframe:m.Timeframe,SwingLeft:m.SwingLeft,SwingRight:m.SwingRight,Fib:m.Fib,Confluence:m.Confluence,TradePlan:m.TradePlan,InitialBalance:10000,FeePct:0.001,SlippagePct:0.0002,MinTrades:8}}