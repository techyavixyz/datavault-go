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

type DestHandler struct{ Store *store.Store }

func (h *DestHandler) List(c *gin.Context) {
	var rows []models.StorageDestination
	h.Store.GetAll(store.TableDestinations, &rows)
	if rows == nil {
		rows = []models.StorageDestination{}
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].CreatedAt.Before(rows[j].CreatedAt)
	})
	c.JSON(http.StatusOK, rows)
}

func (h *DestHandler) Create(c *gin.Context) {
	var d models.StorageDestination
	if err := c.ShouldBindJSON(&d); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	d.ID = uuid.New().String()
	d.CreatedAt = time.Now().UTC()
	h.Store.Upsert(store.TableDestinations, d.ID, d)
	c.JSON(http.StatusOK, d)
}

func (h *DestHandler) Get(c *gin.Context) {
	var d models.StorageDestination
	ok, _ := h.Store.GetByID(store.TableDestinations, c.Param("id"), &d)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	c.JSON(http.StatusOK, d)
}

func (h *DestHandler) Update(c *gin.Context) {
	var existing models.StorageDestination
	ok, _ := h.Store.GetByID(store.TableDestinations, c.Param("id"), &existing)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	var d models.StorageDestination
	if err := c.ShouldBindJSON(&d); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	d.ID = existing.ID
	d.CreatedAt = existing.CreatedAt
	h.Store.Upsert(store.TableDestinations, d.ID, d)
	c.JSON(http.StatusOK, d)
}

func (h *DestHandler) Delete(c *gin.Context) {
	h.Store.Delete(store.TableDestinations, c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func (h *DestHandler) Test(c *gin.Context) {
	var d models.StorageDestination
	ok, _ := h.Store.GetByID(store.TableDestinations, c.Param("id"), &d)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	msg, err := services.TestStorage(&d)
	if err != nil {
		d.ConnStatus = "disconnected"
		h.Store.Upsert(store.TableDestinations, d.ID, d)
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}
	d.ConnStatus = "connected"
	h.Store.Upsert(store.TableDestinations, d.ID, d)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": msg})
}

// TestLive tests a destination config from the request body without saving it.
func (h *DestHandler) TestLive(c *gin.Context) {
	var d models.StorageDestination
	if err := c.ShouldBindJSON(&d); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	msg, err := services.TestStorage(&d)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": msg})
}
