package handlers

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"datavault/models"
	"datavault/services"
	"datavault/store"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type RestoreHandler struct{ Store *store.Store }

func (h *RestoreHandler) List(c *gin.Context) {
	var rows []models.RestoreRecord
	h.Store.GetAll(store.TableRestores, &rows)
	if rows == nil {
		rows = []models.RestoreRecord{}
	}
	c.JSON(http.StatusOK, rows)
}

func (h *RestoreHandler) Create(c *gin.Context) {
	var req models.RestoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}

	var backup models.BackupRecord
	if ok, _ := h.Store.GetByID(store.TableBackups, req.BackupID, &backup); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "backup not found"})
		return
	}
	var dest models.StorageDestination
	if ok, _ := h.Store.GetByID(store.TableDestinations, backup.DestinationID, &dest); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "destination not found"})
		return
	}
	var src models.DatabaseSource
	if ok, _ := h.Store.GetByID(store.TableRestoreTargets, req.TargetSourceID, &src); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "restore target not found"})
		return
	}
	if backup.DBType != src.DBType {
		c.JSON(http.StatusBadRequest, gin.H{
			"detail": fmt.Sprintf("type mismatch: backup is %s but restore target is %s — cannot restore across database types", backup.DBType, src.DBType),
		})
		return
	}

	// Validate custom tmp_dir if provided — must be creatable and writable.
	if req.TmpDir != "" {
		if err := os.MkdirAll(req.TmpDir, 0755); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "tmp_dir: cannot create directory: " + err.Error()})
			return
		}
		probe := req.TmpDir + "/.dv_probe"
		if err := os.WriteFile(probe, []byte("ok"), 0644); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "tmp_dir: directory is not writable: " + err.Error()})
			return
		}
		os.Remove(probe)
	}

	var profile *models.CredentialProfile
	if src.CredentialProfileID != "" {
		var p models.CredentialProfile
		if found, _ := h.Store.GetByID(store.TableCredentials, src.CredentialProfileID, &p); found {
			profile = &p
		}
	}

	rec := models.RestoreRecord{
		ID:               uuid.New().String(),
		BackupID:         backup.ID,
		BackupFileName:   backup.FileName,
		DBType:           backup.DBType,
		TargetSourceID:   src.ID,
		TargetSourceName: src.Name,
		TmpDir:           req.TmpDir,
		Status:           "pending",
		Log:              []string{},
		StartedAt:        time.Now().UTC(),
	}
	h.Store.Upsert(store.TableRestores, rec.ID, rec)

	go services.RunRestore(&rec, &backup, &dest, &src, profile, h.Store)

	c.JSON(http.StatusOK, rec)
}

func (h *RestoreHandler) Get(c *gin.Context) {
	var rec models.RestoreRecord
	ok, _ := h.Store.GetByID(store.TableRestores, c.Param("id"), &rec)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	c.JSON(http.StatusOK, rec)
}
