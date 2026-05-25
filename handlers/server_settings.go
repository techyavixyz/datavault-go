package handlers

import (
	"net/http"
	"os"

	"datavault/models"
	"datavault/services"
	"datavault/store"

	"github.com/gin-gonic/gin"
)

const settingsID = "server"

type ServerSettingsHandler struct{ Store *store.Store }

func (h *ServerSettingsHandler) Get(c *gin.Context) {
	var s models.ServerSettings
	h.Store.GetByID(store.TableSettings, settingsID, &s)
	if s.Timezone == "" {
		s.Timezone = "UTC"
	}
	c.JSON(http.StatusOK, s)
}

func (h *ServerSettingsHandler) Update(c *gin.Context) {
	var req models.ServerSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	if req.Timezone == "" {
		req.Timezone = "UTC"
	}
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
	h.Store.Upsert(store.TableSettings, settingsID, req)
	services.SetTimezone(req.Timezone)
	c.JSON(http.StatusOK, req)
}

// LoadSettings reads saved settings from the store and applies them at startup.
func LoadSettings(st *store.Store) {
	var s models.ServerSettings
	st.GetByID(store.TableSettings, settingsID, &s)
	if s.Timezone != "" {
		services.SetTimezone(s.Timezone)
	}
}
