package services

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const sessionTTL = 7 * 24 * time.Hour

type sessionEntry struct {
	UserID    string
	ExpiresAt time.Time
}

var sessions sync.Map

func CreateSession(userID string) string {
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)
	sessions.Store(token, sessionEntry{UserID: userID, ExpiresAt: time.Now().Add(sessionTTL)})
	return token
}

func ValidateSession(token string) (string, bool) {
	v, ok := sessions.Load(token)
	if !ok {
		return "", false
	}
	entry := v.(sessionEntry)
	if time.Now().After(entry.ExpiresAt) {
		sessions.Delete(token)
		return "", false
	}
	return entry.UserID, true
}

func DeleteSession(token string) {
	sessions.Delete(token)
}
