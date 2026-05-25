package handlers

import (
	"net/http"
	"sort"
	"time"

	"datavault/models"
	"datavault/services"
	"datavault/store"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type RestoreTargetHandler struct{ Store *store.Store }

func (h *RestoreTargetHandler) List(c *gin.Context) {
	var rows []models.DatabaseSource
	h.Store.GetAll(store.TableRestoreTargets, &rows)
	if rows == nil {
		rows = []models.DatabaseSource{}
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].CreatedAt.Before(rows[j].CreatedAt)
	})
	c.JSON(http.StatusOK, rows)
}

func (h *RestoreTargetHandler) Create(c *gin.Context) {
	var s models.DatabaseSource
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	s.ID = uuid.New().String()
	s.CreatedAt = time.Now().UTC()
	h.Store.Upsert(store.TableRestoreTargets, s.ID, s)
	c.JSON(http.StatusOK, s)
}

func (h *RestoreTargetHandler) Get(c *gin.Context) {
	var s models.DatabaseSource
	ok, _ := h.Store.GetByID(store.TableRestoreTargets, c.Param("id"), &s)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	c.JSON(http.StatusOK, s)
}

func (h *RestoreTargetHandler) Update(c *gin.Context) {
	var existing models.DatabaseSource
	ok, _ := h.Store.GetByID(store.TableRestoreTargets, c.Param("id"), &existing)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	var s models.DatabaseSource
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	s.ID = existing.ID
	s.CreatedAt = existing.CreatedAt
	h.Store.Upsert(store.TableRestoreTargets, s.ID, s)
	c.JSON(http.StatusOK, s)
}

func (h *RestoreTargetHandler) Delete(c *gin.Context) {
	h.Store.Delete(store.TableRestoreTargets, c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func (h *RestoreTargetHandler) Test(c *gin.Context) {
	var s models.DatabaseSource
	ok, _ := h.Store.GetByID(store.TableRestoreTargets, c.Param("id"), &s)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	var profile *models.CredentialProfile
	if s.CredentialProfileID != "" {
		var p models.CredentialProfile
		if found, _ := h.Store.GetByID(store.TableCredentials, s.CredentialProfileID, &p); found {
			profile = &p
		}
	}
	msg, err := services.TestConnection(&s, profile)
	if err != nil {
		s.ConnStatus = "disconnected"
		h.Store.Upsert(store.TableRestoreTargets, s.ID, s)
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}
	s.ConnStatus = "connected"
	h.Store.Upsert(store.TableRestoreTargets, s.ID, s)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": msg})
}

func (h *RestoreTargetHandler) TestLive(c *gin.Context) {
	var s models.DatabaseSource
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	var profile *models.CredentialProfile
	if s.CredentialProfileID != "" && s.CredentialProfileID != "none" {
		var p models.CredentialProfile
		if found, _ := h.Store.GetByID(store.TableCredentials, s.CredentialProfileID, &p); found {
			profile = &p
		}
	}
	msg, err := services.TestConnection(&s, profile)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": msg})
}
