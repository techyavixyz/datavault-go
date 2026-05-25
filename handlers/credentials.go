package handlers

import (
	"net/http"
	"time"

	"datavault/models"
	"datavault/store"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type CredHandler struct{ Store *store.Store }

func (h *CredHandler) List(c *gin.Context) {
	var rows []models.CredentialProfile
	h.Store.GetAll(store.TableCredentials, &rows)
	if rows == nil {
		rows = []models.CredentialProfile{}
	}
	c.JSON(http.StatusOK, rows)
}

func (h *CredHandler) Create(c *gin.Context) {
	var p models.CredentialProfile
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	p.ID = uuid.New().String()
	p.CreatedAt = time.Now().UTC()
	if err := h.Store.Upsert(store.TableCredentials, p.ID, p); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
		return
	}
	c.JSON(http.StatusOK, p)
}

func (h *CredHandler) Get(c *gin.Context) {
	var p models.CredentialProfile
	ok, err := h.Store.GetByID(store.TableCredentials, c.Param("id"), &p)
	if err != nil || !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	c.JSON(http.StatusOK, p)
}

func (h *CredHandler) Update(c *gin.Context) {
	var existing models.CredentialProfile
	ok, _ := h.Store.GetByID(store.TableCredentials, c.Param("id"), &existing)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	var p models.CredentialProfile
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	p.ID = existing.ID
	p.CreatedAt = existing.CreatedAt
	h.Store.Upsert(store.TableCredentials, p.ID, p)
	c.JSON(http.StatusOK, p)
}

func (h *CredHandler) Delete(c *gin.Context) {
	h.Store.Delete(store.TableCredentials, c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}
