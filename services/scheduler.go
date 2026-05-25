// Cron scheduler wrapper around robfig/cron.
package services

import (
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

var (
	cronInst *cron.Cron
	cronMu   sync.Mutex
	entryMap = map[string]cron.EntryID{}
)

func StartScheduler() {
	cronInst = cron.New(cron.WithSeconds())
	cronInst.Start()
}

func StopScheduler() {
	if cronInst != nil {
		cronInst.Stop()
	}
}

// ScheduleJob adds or replaces a cron job. expr is a 5-field cron expression.
func ScheduleJob(id, expr string, fn func()) error {
	cronMu.Lock()
	defer cronMu.Unlock()

	// remove existing
	if eid, ok := entryMap[id]; ok {
		cronInst.Remove(eid)
		delete(entryMap, id)
	}

	eid, err := cronInst.AddFunc("0 "+expr, fn) // prepend seconds=0
	if err != nil {
		return err
	}
	entryMap[id] = eid
	return nil
}

func UnscheduleJob(id string) {
	cronMu.Lock()
	defer cronMu.Unlock()
	if eid, ok := entryMap[id]; ok {
		cronInst.Remove(eid)
		delete(entryMap, id)
	}
}

// NextRun returns the next scheduled time for a job, or zero if not found.
func NextRun(id string) *time.Time {
	cronMu.Lock()
	defer cronMu.Unlock()
	eid, ok := entryMap[id]
	if !ok {
		return nil
	}
	entry := cronInst.Entry(eid)
	t := entry.Next
	if t.IsZero() {
		return nil
	}
	return &t
}
