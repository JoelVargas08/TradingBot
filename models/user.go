package models

import "sync"

type UserState struct {
	ChatID int64
	Active bool
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
		return
	}
	um.users[chatID] = &UserState{ChatID: chatID, Active: true}
}

func (um *UserManager) Deactivate(chatID int64) {
	um.mu.Lock()
	defer um.mu.Unlock()
	if user, exists := um.users[chatID]; exists {
		user.Active = false
	}
}

func (um *UserManager) ActiveUserIDs() []int64 {
	um.mu.RLock()
	defer um.mu.RUnlock()
	ids := make([]int64, 0, len(um.users))
	for id, user := range um.users {
		if user.Active {
			ids = append(ids, id)
		}
	}
	return ids
}

func (um *UserManager) SnapshotUsers() []UserState {
	um.mu.RLock()
	defer um.mu.RUnlock()
	out := make([]UserState, 0, len(um.users))
	for _, user := range um.users {
		out = append(out, UserState{ChatID: user.ChatID, Active: user.Active})
	}
	return out
}

func (um *UserManager) Load(users map[int64]*UserState) {
	um.mu.Lock()
	defer um.mu.Unlock()
	for chatID, user := range users {
		um.users[chatID] = user
	}
}
