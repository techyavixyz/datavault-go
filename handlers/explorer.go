package handlers

import (
	"net/http"
	"os"

	"datavault/models"
	"datavault/services"
	"datavault/store"

	"github.com/gin-gonic/gin"
)

type ExplorerHandler struct{ Store *store.Store }

func (h *ExplorerHandler) ListFiles(c *gin.Context) {
	var dest models.StorageDestination
	ok, _ := h.Store.GetByID(store.TableDestinations, c.Param("dest_id"), &dest)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "destination not found"})
		return
	}
	files, err := services.ListFiles(&dest)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
		return
	}
	if files == nil {
		files = []services.FileInfo{}
	}
	c.JSON(http.StatusOK, files)
}

func (h *ExplorerHandler) Download(c *gin.Context) {
	var dest models.StorageDestination
	ok, _ := h.Store.GetByID(store.TableDestinations, c.Param("dest_id"), &dest)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "destination not found"})
		return
	}

	remotePath := c.Query("path")
	if remotePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "path required"})
		return
	}

	tmp, err := os.CreateTemp("", "dv-dl-*")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
		return
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	if err := services.DownloadTo(&dest, remotePath, tmp.Name(), nil); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
		return
	}

	// derive filename from path
	filename := remotePath
	for i := len(remotePath) - 1; i >= 0; i-- {
		if remotePath[i] == '/' {
			filename = remotePath[i+1:]
			break
		}
	}
	c.FileAttachment(tmp.Name(), filename)
}
