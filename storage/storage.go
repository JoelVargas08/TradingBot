package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"tradingview-bot/models"
)

type FileStorage struct {
	filePath string
	mu       sync.RWMutex
}

func NewFileStorage(filePath string) *FileStorage {
	return &FileStorage{filePath: filePath}
}

type PersistedData struct {
	Users map[int64]*UserStateData `json:"users"`
}
type UserStateData struct {
	ChatID int64 `json:"chat_id"`
	Active bool  `json:"active"`
}

func (fs *FileStorage) Load() (map[int64]*models.UserState, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	data, err := os.ReadFile(fs.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[int64]*models.UserState), nil
		}
		return nil, err
	}
	var persisted PersistedData
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil, fmt.Errorf("estado guardado corrupto: %w", err)
	}
	users := make(map[int64]*models.UserState, len(persisted.Users))
	for chatID, userData := range persisted.Users {
		users[chatID] = &models.UserState{
			ChatID: userData.ChatID,
			Active: userData.Active,
		}
	}
	return users, nil
}

func (fs *FileStorage) Save(users []models.UserState) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	persisted := PersistedData{
		Users: make(map[int64]*UserStateData, len(users)),
	}
	for _, user := range users {
		persisted.Users[user.ChatID] = &UserStateData{
			ChatID: user.ChatID,
			Active: user.Active,
		}
	}
	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fs.filePath), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(fs.filePath), ".users-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, fs.filePath)
}
