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

type SourceHandler struct{ Store *store.Store }

func (h *SourceHandler) List(c *gin.Context) {
	var rows []models.DatabaseSource
	h.Store.GetAll(store.TableSources, &rows)
	if rows == nil {
		rows = []models.DatabaseSource{}
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].CreatedAt.Before(rows[j].CreatedAt)
	})
	c.JSON(http.StatusOK, rows)
}

func (h *SourceHandler) Create(c *gin.Context) {
	var s models.DatabaseSource
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	s.ID = uuid.New().String()
	s.CreatedAt = time.Now().UTC()
	h.Store.Upsert(store.TableSources, s.ID, s)
	c.JSON(http.StatusOK, s)
}

func (h *SourceHandler) Get(c *gin.Context) {
	var s models.DatabaseSource
	ok, _ := h.Store.GetByID(store.TableSources, c.Param("id"), &s)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	c.JSON(http.StatusOK, s)
}

func (h *SourceHandler) Update(c *gin.Context) {
	var existing models.DatabaseSource
	ok, _ := h.Store.GetByID(store.TableSources, c.Param("id"), &existing)
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
	h.Store.Upsert(store.TableSources, s.ID, s)
	c.JSON(http.StatusOK, s)
}

func (h *SourceHandler) Delete(c *gin.Context) {
	h.Store.Delete(store.TableSources, c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func (h *SourceHandler) Test(c *gin.Context) {
	var s models.DatabaseSource
	ok, _ := h.Store.GetByID(store.TableSources, c.Param("id"), &s)
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
		h.Store.Upsert(store.TableSources, s.ID, s)
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}
	s.ConnStatus = "connected"
	h.Store.Upsert(store.TableSources, s.ID, s)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": msg})
}

// TestLive tests a source config from the request body without saving it.
// Used when creating a new source before it has an ID.
func (h *SourceHandler) TestLive(c *gin.Context) {
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
