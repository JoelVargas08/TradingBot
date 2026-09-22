package investingbulls

import "testing"

func TestDefaultMultiTimeframeConfig(t *testing.T) {
 c:=DefaultMultiTimeframeConfig()
 if c.MainTimeframe!="1h"||c.EntryTimeframe!="15m"||c.ConfirmTimeframe!="5m"{t.Fatalf("unexpected timeframes: %+v",c)}
 if c.RequireConfirm{t.Fatal("5m confirmation must be optional by default")}
}

func TestNormalizeMultiTimeframeConfig(t *testing.T) {
 c:=normalizeMTF(MultiTimeframeConfig{})
 if c.MainTimeframe==""||c.EntryTimeframe==""||c.MainSwingLeft<=0||c.EntrySwingRight<=0||c.ConfirmSwingLeft<=0{t.Fatalf("config not normalized: %+v",c)}
}