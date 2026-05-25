package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"datavault/store"

	"github.com/gin-gonic/gin"
)

type ConfigHandler struct{ Store *store.Store }

var configTables = []string{
	store.TableCredentials,
	store.TableSources,
	store.TableRestoreTargets,
	store.TableDestinations,
	store.TableBackups,
	store.TableRestores,
	store.TableCronJobs,
	store.TableRetentionPolicies,
	store.TableNotifications,
}

type ConfigBundle struct {
	ExportedAt   string                                `json:"exported_at"`
	Version      string                                `json:"version"`
	IncludeUsers bool                                  `json:"include_users"`
	Tables       map[string]map[string]json.RawMessage `json:"tables"`
}

func (h *ConfigHandler) Export(c *gin.Context) {
	includeUsers := c.Query("include_users") == "true"
	tables := make([]string, len(configTables))
	copy(tables, configTables)
	if includeUsers {
		tables = append(tables, store.TableUsers)
	}
	bundle := ConfigBundle{
		ExportedAt:   time.Now().UTC().Format(time.RFC3339),
		Version:      "2.0.0-go",
		IncludeUsers: includeUsers,
		Tables:       h.Store.RawExport(tables),
	}
	filename := fmt.Sprintf("datavault-config-%s.json", time.Now().UTC().Format("20060102-150405"))
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.JSON(http.StatusOK, bundle)
}

func (h *ConfigHandler) Import(c *gin.Context) {
	var bundle ConfigBundle
	if err := c.ShouldBindJSON(&bundle); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid JSON: " + err.Error()})
		return
	}
	if bundle.Tables == nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "no tables found in export file"})
		return
	}
	allowed := make(map[string]bool)
	for _, t := range append(configTables, store.TableUsers) {
		allowed[t] = true
	}
	imported := 0
	for table, data := range bundle.Tables {
		if !allowed[table] {
			continue
		}
		if err := h.Store.ReplaceTable(table, data); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": fmt.Sprintf("restore table %s: %v", table, err)})
			return
		}
		imported++
	}
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"tables":  imported,
		"message": fmt.Sprintf("Restored %d tables successfully.", imported),
	})
}
