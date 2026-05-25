package services

import (
	"fmt"
	"sort"
	"time"

	"datavault/models"
	"datavault/store"
)

// ApplyRetentionPolicy evaluates the policy against all successful backups for
// the configured source+destination pair, deletes files from storage, and
// removes the corresponding records from the store.
func ApplyRetentionPolicy(policy *models.RetentionPolicy, dest *models.StorageDestination, st *store.Store) (models.RetentionResult, error) {
	var all []models.BackupRecord
	st.GetAll(store.TableBackups, &all)

	// Filter to this source+destination pair only
	var matched []models.BackupRecord
	for _, b := range all {
		if b.SourceID == policy.SourceID && b.DestinationID == policy.DestinationID {
			matched = append(matched, b)
		}
	}
	if len(matched) == 0 {
		return models.RetentionResult{}, nil
	}

	if policy.KeepAll {
		return models.RetentionResult{Kept: len(matched)}, nil
	}

	// Sort newest first
	sort.Slice(matched, func(i, j int) bool {
		return matched[i].StartedAt.After(matched[j].StartedAt)
	})

	// Split: only successful backups are subject to retention rules;
	// pending/running/failed records are always kept.
	var succeeded, others []models.BackupRecord
	for _, b := range matched {
		if b.Status == "success" {
			succeeded = append(succeeded, b)
		} else {
			others = append(others, b)
		}
	}

	keepIDs := map[string]bool{}
	now := time.Now().UTC()

	// Keep last N
	if policy.KeepLast > 0 {
		for i, b := range succeeded {
			if i < policy.KeepLast {
				keepIDs[b.ID] = true
			}
		}
	}

	// Keep one per calendar day for the last N days
	if policy.KeepDaily > 0 {
		seen := map[string]bool{}
		cutoff := now.AddDate(0, 0, -policy.KeepDaily)
		for _, b := range succeeded {
			if b.StartedAt.Before(cutoff) {
				continue
			}
			key := b.StartedAt.Format("2006-01-02")
			if !seen[key] {
				seen[key] = true
				keepIDs[b.ID] = true
			}
		}
	}

	// Keep one per ISO week for the last N weeks
	if policy.KeepWeekly > 0 {
		seen := map[string]bool{}
		cutoff := now.AddDate(0, 0, -policy.KeepWeekly*7)
		for _, b := range succeeded {
			if b.StartedAt.Before(cutoff) {
				continue
			}
			y, w := b.StartedAt.ISOWeek()
			key := fmt.Sprintf("%d-W%02d", y, w)
			if !seen[key] {
				seen[key] = true
				keepIDs[b.ID] = true
			}
		}
	}

	// Keep one per calendar month for the last N months
	if policy.KeepMonthly > 0 {
		seen := map[string]bool{}
		cutoff := now.AddDate(0, -policy.KeepMonthly, 0)
		for _, b := range succeeded {
			if b.StartedAt.Before(cutoff) {
				continue
			}
			key := b.StartedAt.Format("2006-01")
			
			if !seen[key] {
				seen[key] = true
				keepIDs[b.ID] = true
			}
		}
	}

	// Keep one per calendar year for the last N years
	if policy.KeepYearly > 0 {
		seen := map[string]bool{}
		cutoff := now.AddDate(-policy.KeepYearly, 0, 0)
		for _, b := range succeeded {
			if b.StartedAt.Before(cutoff) {
				continue
			}
			key := b.StartedAt.Format("2006")
			if !seen[key] {
				seen[key] = true
				keepIDs[b.ID] = true
			}
		}
	}

	result := models.RetentionResult{Kept: len(others)}
	for _, b := range succeeded {
		if keepIDs[b.ID] {
			result.Kept++
			continue
		}
		if b.RemotePath != "" {
			if err := DeleteFile(dest, b.RemotePath); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", b.FileName, err))
			}
		}
		st.Delete(store.TableBackups, b.ID)
		result.Deleted++
	}
	return result, nil
}
