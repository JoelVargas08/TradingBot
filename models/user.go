package models

import "sync"

type UserState struct {
	ChatID      int64
	Active      bool
	LastSignals map[string]string // symbol -> "buy" o "sell"
	mu          sync.RWMutex
}
type UserManager struct {
	users map[int64]*UserState
	mu    sync.RWMutex
}

func NewUserManager() *UserManager {
	return &UserManager{
		users: make(map[int64]*UserState),
	}
}
func (um *UserManager) Activate(chatID int64) {
	um.mu.Lock()
	defer um.mu.Unlock()
	if user, exists := um.users[chatID]; exists {
		user.Active = true
	} else {
		um.users[chatID] = &UserState{
			ChatID:      chatID,
			Active:      true,
			LastSignals: make(map[string]string),
		}
	}
}
func (um *UserManager) Deactivate(chatID int64) {
	um.mu.Lock()
	defer um.mu.Unlock()
	if user, exists := um.users[chatID]; exists {
		user.Active = false
	}
}
func (um *UserManager) GetActiveUsers() []*UserState {
	um.mu.RLock()
	defer um.mu.RUnlock()
	var active []*UserState
	for _, user := range um.users {
		if user.Active {
			active = append(active, user)
		}
	}
	return active
}
func (um *UserManager) Load(users map[int64]*UserState) {
	um.mu.Lock()
	defer um.mu.Unlock()
	for chatID, user := range users {
		um.users[chatID] = user
	}
}
func (um *UserManager) GetAllUsers() map[int64]*UserState {
	um.mu.RLock()
	defer um.mu.RUnlock()
	copied := make(map[int64]*UserState, len(um.users))
	for chatID, user := range um.users {
		copied[chatID] = user
	}
	return copied
}
func (u *UserState) GetLastSignal(symbol string) string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.LastSignals[symbol]
}
func (u *UserState) SetLastSignal(symbol, signal string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.LastSignals[symbol] = signal
}
func (u *UserState) Snapshot() (chatID int64, active bool, signals map[string]string) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	signals = make(map[string]string, len(u.LastSignals))
	for symbol, signal := range u.LastSignals {
		signals[symbol] = signal
	}
	return u.ChatID, u.Active, signals
}
