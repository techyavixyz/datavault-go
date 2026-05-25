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

type NotificationHandler struct{ Store *store.Store }

func (h *NotificationHandler) List(c *gin.Context) {
	var rows []models.NotificationChannel
	h.Store.GetAll(store.TableNotifications, &rows)
	if rows == nil {
		rows = []models.NotificationChannel{}
	}
	c.JSON(http.StatusOK, rows)
}

func (h *NotificationHandler) Create(c *gin.Context) {
	var ch models.NotificationChannel
	if err := c.ShouldBindJSON(&ch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	ch.ID = uuid.New().String()
	ch.CreatedAt = time.Now().UTC()
	if ch.Config == nil {
		ch.Config = map[string]string{}
	}
	h.Store.Upsert(store.TableNotifications, ch.ID, ch)
	c.JSON(http.StatusOK, ch)
}

func (h *NotificationHandler) Get(c *gin.Context) {
	var ch models.NotificationChannel
	ok, _ := h.Store.GetByID(store.TableNotifications, c.Param("id"), &ch)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	c.JSON(http.StatusOK, ch)
}

func (h *NotificationHandler) Update(c *gin.Context) {
	var existing models.NotificationChannel
	ok, _ := h.Store.GetByID(store.TableNotifications, c.Param("id"), &existing)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	var ch models.NotificationChannel
	if err := c.ShouldBindJSON(&ch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	ch.ID = existing.ID
	ch.CreatedAt = existing.CreatedAt
	if ch.Config == nil {
		ch.Config = map[string]string{}
	}
	h.Store.Upsert(store.TableNotifications, ch.ID, ch)
	c.JSON(http.StatusOK, ch)
}

func (h *NotificationHandler) Delete(c *gin.Context) {
	h.Store.Delete(store.TableNotifications, c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func (h *NotificationHandler) Test(c *gin.Context) {
	var ch models.NotificationChannel
	if err := c.ShouldBindJSON(&ch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	if ch.Config == nil {
		ch.Config = map[string]string{}
	}
	if err := services.TestNotification(ch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
