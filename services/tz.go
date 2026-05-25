package services

import (
	"log"
	"sync"
	"time"
)

var (
	tzMu       sync.RWMutex
	serverLoc  = time.UTC
)

// SetTimezone updates the package-level location used for all timestamp formatting.
// Call this at startup and whenever the timezone setting changes.
func SetTimezone(tz string) {
	if tz == "" {
		tzMu.Lock()
		serverLoc = time.UTC
		tzMu.Unlock()
		return
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		log.Printf("SetTimezone: invalid timezone %q: %v", tz, err)
		return
	}
	tzMu.Lock()
	serverLoc = loc
	tzMu.Unlock()
}

// ServerLocation returns the currently configured server timezone.
func ServerLocation() *time.Location {
	tzMu.RLock()
	defer tzMu.RUnlock()
	return serverLoc
}

// FormatTime formats t in the server timezone.
func FormatTime(t time.Time) string {
	return t.In(ServerLocation()).Format("2006-01-02 15:04:05")
}
