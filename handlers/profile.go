package handlers

import (
	"net/http"
	"strings"

	"datavault/models"
	"datavault/store"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

type ProfileHandler struct {
	Store *store.Store
}

// Get returns the current user's safe profile.
func (h *ProfileHandler) Get(c *gin.Context) {
	userID, _ := c.Get("user_id")
	var user models.User
	ok, _ := h.Store.GetByID(store.TableUsers, userID.(string), &user)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "user not found"})
		return
	}
	c.JSON(http.StatusOK, models.UserSafe{
		ID: user.ID, Username: user.Username, Email: user.Email,
		Role: user.Role, CreatedAt: user.CreatedAt,
	})
}

// Update lets any authenticated user change their own email and/or password.
// Changing the password requires the current password to be provided.
func (h *ProfileHandler) Update(c *gin.Context) {
	userID, _ := c.Get("user_id")

	var req struct {
		Email           string `json:"email"`
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}

	var user models.User
	ok, _ := h.Store.GetByID(store.TableUsers, userID.(string), &user)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "user not found"})
		return
	}

	if req.Email != "" {
		user.Email = strings.TrimSpace(req.Email)
	}

	if req.NewPassword != "" {
		if req.CurrentPassword == "" {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "Current password is required to set a new password"})
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.CurrentPassword)); err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"detail": "Current password is incorrect"})
			return
		}
		if len(req.NewPassword) < 6 {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "New password must be at least 6 characters"})
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Failed to hash password"})
			return
		}
		user.PasswordHash = string(hash)
	}

	h.Store.Upsert(store.TableUsers, user.ID, user)
	c.JSON(http.StatusOK, models.UserSafe{
		ID: user.ID, Username: user.Username, Email: user.Email,
		Role: user.Role, CreatedAt: user.CreatedAt,
	})
}
