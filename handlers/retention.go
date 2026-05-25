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

type RetentionHandler struct{ Store *store.Store }

var retentionCronExprs = map[string]string{
	"hourly": "0 * * * *",
	"daily":  "0 0 * * *",
	"weekly": "0 0 * * 0",
}

func retJobID(policyID string) string { return "ret-" + policyID }

func scheduleRetentionPolicy(st *store.Store, p models.RetentionPolicy) {
	expr, ok := retentionCronExprs[p.Schedule]
	if !ok {
		return
	}
	services.ScheduleJob(retJobID(p.ID), expr, func() {
		var policy models.RetentionPolicy
		if found, _ := st.GetByID(store.TableRetentionPolicies, p.ID, &policy); !found {
			return
		}
		var dest models.StorageDestination
		if found, _ := st.GetByID(store.TableDestinations, policy.DestinationID, &dest); !found {
			return
		}
		result, err := services.ApplyRetentionPolicy(&policy, &dest, st)
		if err != nil {
			return
		}
		now := time.Now().UTC()
		policy.LastRunAt = &now
		policy.LastDeleted = result.Deleted
		st.Upsert(store.TableRetentionPolicies, policy.ID, policy)
	})
}

// LoadRetentionSchedules re-registers all scheduled retention policies on startup.
func LoadRetentionSchedules(st *store.Store) {
	var rows []models.RetentionPolicy
	st.GetAll(store.TableRetentionPolicies, &rows)
	for _, p := range rows {
		if p.Schedule != "" {
			scheduleRetentionPolicy(st, p)
		}
	}
}

func (h *RetentionHandler) List(c *gin.Context) {
	var rows []models.RetentionPolicy
	h.Store.GetAll(store.TableRetentionPolicies, &rows)
	if rows == nil {
		rows = []models.RetentionPolicy{}
	}
	c.JSON(http.StatusOK, rows)
}

func (h *RetentionHandler) Create(c *gin.Context) {
	var p models.RetentionPolicy
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	p.ID = uuid.New().String()
	p.CreatedAt = time.Now().UTC()
	h.Store.Upsert(store.TableRetentionPolicies, p.ID, p)
	if p.Schedule != "" {
		scheduleRetentionPolicy(h.Store, p)
	}
	c.JSON(http.StatusOK, p)
}

func (h *RetentionHandler) Get(c *gin.Context) {
	var p models.RetentionPolicy
	ok, _ := h.Store.GetByID(store.TableRetentionPolicies, c.Param("id"), &p)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	c.JSON(http.StatusOK, p)
}

func (h *RetentionHandler) Update(c *gin.Context) {
	var existing models.RetentionPolicy
	ok, _ := h.Store.GetByID(store.TableRetentionPolicies, c.Param("id"), &existing)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	var p models.RetentionPolicy
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	p.ID = existing.ID
	p.CreatedAt = existing.CreatedAt
	p.LastRunAt = existing.LastRunAt
	p.LastDeleted = existing.LastDeleted
	h.Store.Upsert(store.TableRetentionPolicies, p.ID, p)
	services.UnscheduleJob(retJobID(p.ID))
	if p.Schedule != "" {
		scheduleRetentionPolicy(h.Store, p)
	}
	c.JSON(http.StatusOK, p)
}

func (h *RetentionHandler) Delete(c *gin.Context) {
	services.UnscheduleJob(retJobID(c.Param("id")))
	h.Store.Delete(store.TableRetentionPolicies, c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func (h *RetentionHandler) Apply(c *gin.Context) {
	var p models.RetentionPolicy
	ok, _ := h.Store.GetByID(store.TableRetentionPolicies, c.Param("id"), &p)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	var dest models.StorageDestination
	ok, _ = h.Store.GetByID(store.TableDestinations, p.DestinationID, &dest)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "destination not found"})
		return
	}
	result, err := services.ApplyRetentionPolicy(&p, &dest, h.Store)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
		return
	}
	now := time.Now().UTC()
	p.LastRunAt = &now
	p.LastDeleted = result.Deleted
	h.Store.Upsert(store.TableRetentionPolicies, p.ID, p)
	c.JSON(http.StatusOK, result)
}
