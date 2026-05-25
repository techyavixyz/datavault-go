package handlers

import (
	"net/http"
	"time"

	"datavault/models"
	"datavault/services"
	"datavault/store"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type BackupHandler struct{ Store *store.Store }

func (h *BackupHandler) List(c *gin.Context) {
	var rows []models.BackupRecord
	h.Store.GetAll(store.TableBackups, &rows)
	if rows == nil {
		rows = []models.BackupRecord{}
	}
	c.JSON(http.StatusOK, rows)
}

func (h *BackupHandler) Create(c *gin.Context) {
	var req models.BackupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}

	var src models.DatabaseSource
	if ok, _ := h.Store.GetByID(store.TableSources, req.SourceID, &src); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "source not found"})
		return
	}
	var dest models.StorageDestination
	if ok, _ := h.Store.GetByID(store.TableDestinations, req.DestinationID, &dest); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "destination not found"})
		return
	}
	if req.RedisMode != "" && src.DBType != models.DBRedis {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "redis_mode is only valid for Redis sources"})
		return
	}

	var profile *models.CredentialProfile
	if src.CredentialProfileID != "" {
		var p models.CredentialProfile
		if found, _ := h.Store.GetByID(store.TableCredentials, src.CredentialProfileID, &p); found {
			profile = &p
		}
	}

	rec := models.BackupRecord{
		ID:              uuid.New().String(),
		SourceID:        src.ID,
		SourceName:      src.Name,
		DBType:          src.DBType,
		DestinationID:   dest.ID,
		DestinationName: dest.Name,
		StorageType:     dest.StorageType,
		Compress:        req.Compress,
		Label:           req.Label,
		RedisMode:       req.RedisMode,
		Status:          "pending",
		StartedAt:       time.Now().UTC(),
		Log:             []string{},
	}
	h.Store.Upsert(store.TableBackups, rec.ID, rec)

	channelIDs := req.NotificationChannelIDs
	go func() {
		services.RunBackup(&rec, &src, &dest, profile, h.Store,
			req.FolderPattern, req.FilenamePattern)
		services.SendNotifications(h.Store, channelIDs, &rec, rec.SourceName+" manual backup")
	}()

	c.JSON(http.StatusOK, rec)
}

func (h *BackupHandler) Get(c *gin.Context) {
	var rec models.BackupRecord
	ok, _ := h.Store.GetByID(store.TableBackups, c.Param("id"), &rec)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	c.JSON(http.StatusOK, rec)
}

func (h *BackupHandler) Delete(c *gin.Context) {
	h.Store.Delete(store.TableBackups, c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}
