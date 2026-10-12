package investingbulls

import (
	"encoding/json"
	"testing"
)

func TestLoadLearnedModelFillsLegacyComponents(t *testing.T) {
	m := LearnedModel{Confluence: ConfluenceConfig{Mode: ConfluenceAll}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := LoadLearnedModel(string(raw))
	if err != nil {
		t.Fatalf("LoadLearnedModel: %v", err)
	}
	if got.ConfluenceComponents != "fibonacci,order_block,imbalance" {
		t.Errorf("componentes migrados = %q, want %q", got.ConfluenceComponents, "fibonacci,order_block,imbalance")
	}
}

func TestLoadLearnedModelRejectsIncompatibleVersion(t *testing.T) {
	m := LearnedModel{CodeVersion: "investing-bulls/2.0.0"}
	raw, _ := json.Marshal(m)
	if _, err := LoadLearnedModel(string(raw)); err == nil {
		t.Fatal("se esperaba rechazo de versión incompatible")
	}
}

func TestLoadMultiTimeframeModelNormalizesTimeframes(t *testing.T) {
	m := MultiTimeframeModel{CodeVersion: CodeVersion}
	raw, _ := json.Marshal(m)
	got, err := LoadMultiTimeframeModel(string(raw))
	if err != nil {
		t.Fatalf("LoadMultiTimeframeModel: %v", err)
	}
	if got.Timeframes.MainTimeframe != "1h" || got.Timeframes.EntryTimeframe != "15m" || got.Timeframes.ConfirmTimeframe != "5m" {
		t.Errorf("timeframes normalizados = %+v", got.Timeframes)
	}
}

func TestLoadMultiTimeframeModelRejectsIncompatibleVersion(t *testing.T) {
	m := MultiTimeframeModel{CodeVersion: "investing-bulls/0.9.0"}
	raw, _ := json.Marshal(m)
	if _, err := LoadMultiTimeframeModel(string(raw)); err == nil {
		t.Fatal("se esperaba rechazo de versión incompatible")
	}
}
