package handlers

import (
	"log"
	"net/http"
	"strings"
	"time"

	"datavault/models"
	"datavault/services"
	"datavault/store"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type CronHandler struct{ Store *store.Store }

type cronJobResponse struct {
	models.CronJob
	NextRunAt *time.Time `json:"next_run_at,omitempty"`
}

func (h *CronHandler) enrich(j models.CronJob) cronJobResponse {
	r := cronJobResponse{CronJob: j}
	r.NextRunAt = services.NextRun(j.ID)
	return r
}

func (h *CronHandler) List(c *gin.Context) {
	var rows []models.CronJob
	h.Store.GetAll(store.TableCronJobs, &rows)
	if rows == nil {
		rows = []models.CronJob{}
	}
	out := make([]cronJobResponse, len(rows))
	for i, r := range rows {
		out[i] = h.enrich(r)
	}
	c.JSON(http.StatusOK, out)
}

func (h *CronHandler) Create(c *gin.Context) {
	var j models.CronJob
	if err := c.ShouldBindJSON(&j); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	if j.RedisMode != "" {
		var src models.DatabaseSource
		if ok, _ := h.Store.GetByID(store.TableSources, j.SourceID, &src); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "source not found"})
			return
		}
		if src.DBType != models.DBRedis {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "redis_mode is only valid for Redis sources"})
			return
		}
	}
	j.ID = uuid.New().String()
	j.CreatedAt = time.Now().UTC()
	h.Store.Upsert(store.TableCronJobs, j.ID, j)

	if j.Enabled {
		if err := scheduleJob(h.Store, j); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid cron: " + err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, h.enrich(j))
}

func (h *CronHandler) Get(c *gin.Context) {
	var j models.CronJob
	ok, _ := h.Store.GetByID(store.TableCronJobs, c.Param("id"), &j)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	c.JSON(http.StatusOK, h.enrich(j))
}

func (h *CronHandler) Update(c *gin.Context) {
	var existing models.CronJob
	ok, _ := h.Store.GetByID(store.TableCronJobs, c.Param("id"), &existing)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	var j models.CronJob
	if err := c.ShouldBindJSON(&j); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	if j.RedisMode != "" {
		var src models.DatabaseSource
		if ok, _ := h.Store.GetByID(store.TableSources, j.SourceID, &src); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "source not found"})
			return
		}
		if src.DBType != models.DBRedis {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "redis_mode is only valid for Redis sources"})
			return
		}
	}
	j.ID = existing.ID
	j.CreatedAt = existing.CreatedAt
	h.Store.Upsert(store.TableCronJobs, j.ID, j)

	services.UnscheduleJob(j.ID)
	if j.Enabled {
		if err := scheduleJob(h.Store, j); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid cron: " + err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, h.enrich(j))
}

func (h *CronHandler) Delete(c *gin.Context) {
	services.UnscheduleJob(c.Param("id"))
	h.Store.Delete(store.TableCronJobs, c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func (h *CronHandler) Run(c *gin.Context) {
	var j models.CronJob
	ok, _ := h.Store.GetByID(store.TableCronJobs, c.Param("id"), &j)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	go executeCronJob(h.Store, j)
	c.JSON(http.StatusOK, gin.H{"status": "triggered"})
}

func (h *CronHandler) Toggle(c *gin.Context) {
	var j models.CronJob
	ok, _ := h.Store.GetByID(store.TableCronJobs, c.Param("id"), &j)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "not found"})
		return
	}
	j.Enabled = !j.Enabled
	h.Store.Upsert(store.TableCronJobs, j.ID, j)
	if j.Enabled {
		scheduleJob(h.Store, j)
	} else {
		services.UnscheduleJob(j.ID)
	}
	c.JSON(http.StatusOK, h.enrich(j))
}

// scheduleJob wires up a cron job with the scheduler.
func scheduleJob(st *store.Store, j models.CronJob) error {
	return services.ScheduleJob(j.ID, j.CronExpression, func() {
		executeCronJob(st, j)
	})
}

func executeCronJob(st *store.Store, j models.CronJob) {
	var src models.DatabaseSource
	if ok, _ := st.GetByID(store.TableSources, j.SourceID, &src); !ok {
		return
	}
	var dest models.StorageDestination
	if ok, _ := st.GetByID(store.TableDestinations, j.DestinationID, &dest); !ok {
		return
	}
	var profile *models.CredentialProfile
	if src.CredentialProfileID != "" {
		var p models.CredentialProfile
		if found, _ := st.GetByID(store.TableCredentials, src.CredentialProfileID, &p); found {
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
		Compress:        j.Compress,
		Label:           j.Label,
		RedisMode:       j.RedisMode,
		Status:          "pending",
		StartedAt:       time.Now().UTC(),
		Log:             []string{},
	}
	st.Upsert(store.TableBackups, rec.ID, rec)

	pingHeartbeat(j.HeartbeatURL, "/start")
	services.RunBackup(&rec, &src, &dest, profile, st, j.FolderPattern, j.FilenamePattern)
	if rec.Status == "success" {
		pingHeartbeat(j.HeartbeatURL, "")
	} else {
		pingHeartbeat(j.HeartbeatURL, "/fail")
	}
	go services.SendNotifications(st, j.NotificationChannelIDs, &rec, j.Name)

	// update last_run_at
	var latest models.CronJob
	if ok, _ := st.GetByID(store.TableCronJobs, j.ID, &latest); ok {
		now := time.Now().UTC()
		latest.LastRunAt = &now
		st.Upsert(store.TableCronJobs, latest.ID, latest)
	}
}

// pingHeartbeat fires a GET to baseURL+suffix, ignoring errors.
// suffix is one of "", "/start", "/fail".
func pingHeartbeat(baseURL, suffix string) {
	if baseURL == "" {
		return
	}
	url := strings.TrimRight(baseURL, "/") + suffix
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		log.Printf("heartbeat ping %s: %v", url, err)
		return
	}
	resp.Body.Close()
}

// LoadScheduledJobs re-registers all enabled cron jobs from the store.
func LoadScheduledJobs(st *store.Store) {
	var rows []models.CronJob
	st.GetAll(store.TableCronJobs, &rows)
	for _, j := range rows {
		if j.Enabled {
			scheduleJob(st, j)
		}
	}
}
