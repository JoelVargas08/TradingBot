package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"tradingview-bot/models"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStorage(filepath.Join(dir, "data", "users.json"))

	users := map[int64]*models.UserState{
		42: {
			ChatID:      42,
			Active:      true,
			LastSignals: map[string]string{"BTCUSDT": "buy"},
		},
	}
	if err := fs.Save(users); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	loaded, err := fs.Load()
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded = %d usuarios, want 1", len(loaded))
	}
	u := loaded[42]
	if u == nil || !u.Active || u.GetLastSignal("BTCUSDT") != "buy" {
		t.Errorf("estado cargado incorrecto: %+v", u)
	}
}

func TestLoadMissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	fs := NewFileStorage(filepath.Join(dir, "nope.json"))

	loaded, err := fs.Load()
	if err != nil {
		t.Fatalf("Load error con archivo inexistente: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("loaded = %d, want 0", len(loaded))
	}
}

func TestLoadCorruptFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	if err := os.WriteFile(path, []byte("{not-valid-json"), 0644); err != nil {
		t.Fatal(err)
	}
	fs := NewFileStorage(path)

	if _, err := fs.Load(); err == nil {
		t.Error("Load con archivo corrupto devolvió nil error, want error")
	}
}

func TestSaveOverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.json")
	if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	fs := NewFileStorage(path)

	users := map[int64]*models.UserState{1: {ChatID: 1, Active: true, LastSignals: map[string]string{}}}
	if err := fs.Save(users); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted PersistedData
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("archivo resultante no es JSON válido: %v", err)
	}
	if len(persisted.Users) != 1 {
		t.Errorf("persisted = %d, want 1", len(persisted.Users))
	}
}
