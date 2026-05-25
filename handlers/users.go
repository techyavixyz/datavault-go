package handlers

import (
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"datavault/models"
	"datavault/store"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type UserHandler struct {
	Store *store.Store
}

// CleanupUsers runs once at startup: removes duplicate-username records and
// backfills any empty Role to super_admin (pre-role-support legacy accounts).
func CleanupUsers(st *store.Store) {
	var users []models.User
	st.GetAll(store.TableUsers, &users)

	keep := make(map[string]models.User) // lower(username) → winner
	var toDelete []string

	for _, u := range users {
		key := strings.ToLower(u.Username)
		existing, exists := keep[key]
		if !exists {
			keep[key] = u
		} else {
			// Prefer the record that already has a role set.
			if u.Role != "" && existing.Role == "" {
				toDelete = append(toDelete, existing.ID)
				keep[key] = u
			} else {
				toDelete = append(toDelete, u.ID)
			}
		}
	}
	for _, id := range toDelete {
		st.Delete(store.TableUsers, id)
	}

	// Backfill empty roles — all accounts pre-role-support are treated as super_admin.
	for _, u := range keep {
		if u.Role == "" {
			u.Role = models.RoleSuperAdmin
			st.Upsert(store.TableUsers, u.ID, u)
		}
	}
}

func (h *UserHandler) List(c *gin.Context) {
	var users []models.User
	h.Store.GetAll(store.TableUsers, &users)
	c.JSON(http.StatusOK, deduplicateUsers(users))
}

// deduplicateUsers collapses same-username records (left from pre-role DB state),
// backfills empty roles to super_admin, and keeps the entry with a role set.
func deduplicateUsers(users []models.User) []models.UserSafe {
	type entry struct {
		safe     models.UserSafe
		hasRole  bool
	}
	seen := make(map[string]*entry, len(users))
	for _, u := range users {
		role := u.Role
		if role == "" {
			role = models.RoleSuperAdmin
		}
		key := strings.ToLower(u.Username)
		e, exists := seen[key]
		if !exists || (!e.hasRole && u.Role != "") {
			seen[key] = &entry{
				safe:    models.UserSafe{ID: u.ID, Username: u.Username, Email: u.Email, Role: role, CreatedAt: u.CreatedAt},
				hasRole: u.Role != "",
			}
		}
	}
	out := make([]models.UserSafe, 0, len(seen))
	for _, e := range seen {
		out = append(out, e.safe)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func (h *UserHandler) Create(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
		Email    string `json:"email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	if len(strings.TrimSpace(req.Username)) < 3 {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "Username must be at least 3 characters"})
		return
	}
	if len(req.Password) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "Password must be at least 6 characters"})
		return
	}
	if req.Role != models.RoleSuperAdmin && req.Role != models.RoleAdmin && req.Role != models.RoleNormal {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "Invalid role"})
		return
	}

	var users []models.User
	h.Store.GetAll(store.TableUsers, &users)
	for _, u := range users {
		if strings.EqualFold(u.Username, req.Username) {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "Username already taken"})
			return
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Failed to hash password"})
		return
	}

	user := models.User{
		ID:           uuid.New().String(),
		Username:     strings.TrimSpace(req.Username),
		PasswordHash: string(hash),
		Email:        strings.TrimSpace(req.Email),
		Role:         req.Role,
		CreatedAt:    time.Now().UTC(),
	}
	h.Store.Upsert(store.TableUsers, user.ID, user)
	c.JSON(http.StatusOK, models.UserSafe{ID: user.ID, Username: user.Username, Email: user.Email, Role: user.Role, CreatedAt: user.CreatedAt})
}

// Update handles partial updates: role, email, and/or password in one request.
func (h *UserHandler) Update(c *gin.Context) {
	id := c.Param("id")
	callerID, _ := c.Get("user_id")

	var req struct {
		Role     string `json:"role"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}

	var user models.User
	ok, _ := h.Store.GetByID(store.TableUsers, id, &user)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "User not found"})
		return
	}

	if req.Role != "" {
		if req.Role != models.RoleSuperAdmin && req.Role != models.RoleAdmin && req.Role != models.RoleNormal {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "Invalid role"})
			return
		}
		if id == callerID.(string) {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "Cannot change your own role"})
			return
		}
		user.Role = req.Role
	}

	if req.Email != "" {
		user.Email = strings.TrimSpace(req.Email)
	}

	if req.Password != "" {
		if len(req.Password) < 6 {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "Password must be at least 6 characters"})
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"detail": "Failed to hash password"})
			return
		}
		user.PasswordHash = string(hash)
	}

	h.Store.Upsert(store.TableUsers, user.ID, user)
	c.JSON(http.StatusOK, models.UserSafe{ID: user.ID, Username: user.Username, Email: user.Email, Role: user.Role, CreatedAt: user.CreatedAt})
}

func (h *UserHandler) UpdateRole(c *gin.Context) {
	id := c.Param("id")
	callerID, _ := c.Get("user_id")

	var req struct {
		Role string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	if req.Role != models.RoleSuperAdmin && req.Role != models.RoleAdmin && req.Role != models.RoleNormal {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "Invalid role"})
		return
	}
	if id == callerID.(string) {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "Cannot change your own role"})
		return
	}

	var user models.User
	ok, _ := h.Store.GetByID(store.TableUsers, id, &user)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "User not found"})
		return
	}
	user.Role = req.Role
	h.Store.Upsert(store.TableUsers, user.ID, user)
	c.JSON(http.StatusOK, models.UserSafe{ID: user.ID, Username: user.Username, Role: user.Role, CreatedAt: user.CreatedAt})
}

func (h *UserHandler) ResetPassword(c *gin.Context) {
	id := c.Param("id")

	var req struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	if len(req.Password) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "Password must be at least 6 characters"})
		return
	}

	var user models.User
	ok, _ := h.Store.GetByID(store.TableUsers, id, &user)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "User not found"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Failed to hash password"})
		return
	}
	user.PasswordHash = string(hash)
	h.Store.Upsert(store.TableUsers, user.ID, user)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *UserHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	callerID, _ := c.Get("user_id")
	if id == callerID.(string) {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "Cannot delete your own account"})
		return
	}

	var users []models.User
	h.Store.GetAll(store.TableUsers, &users)
	superCount := 0
	for _, u := range users {
		if u.Role == models.RoleSuperAdmin {
			superCount++
		}
	}
	var target models.User
	ok, _ := h.Store.GetByID(store.TableUsers, id, &target)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"detail": "User not found"})
		return
	}
	if target.Role == models.RoleSuperAdmin && superCount <= 1 {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "Cannot delete the last super admin"})
		return
	}

	h.Store.Delete(store.TableUsers, id)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// SeedAdminUser creates a super_admin from ADMIN_USERNAME+ADMIN_PASSWORD env vars,
// but only when no users exist yet.
func SeedAdminUser(st *store.Store) {
	username := os.Getenv("ADMIN_USERNAME")
	password := os.Getenv("ADMIN_PASSWORD")
	if username == "" || password == "" {
		return
	}
	var users []models.User
	st.GetAll(store.TableUsers, &users)
	if len(users) > 0 {
		log.Println("SeedAdminUser: users already exist, skipping")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		log.Printf("SeedAdminUser: bcrypt: %v", err)
		return
	}
	u := models.User{
		ID:           uuid.New().String(),
		Username:     username,
		PasswordHash: string(hash),
		Role:         models.RoleSuperAdmin,
		CreatedAt:    time.Now().UTC(),
	}
	st.Upsert(store.TableUsers, u.ID, u)
	log.Printf("SeedAdminUser: created super_admin '%s'", username)
}

// ResetAdminPassword resets the first super_admin's password using the
// RESET_ADMIN_PASSWORD env var. Logs a warning to remove the var after use.
func ResetAdminPassword(st *store.Store) {
	newPass := os.Getenv("RESET_ADMIN_PASSWORD")
	if newPass == "" {
		return
	}
	var users []models.User
	st.GetAll(store.TableUsers, &users)
	for _, u := range users {
		if u.Role == models.RoleSuperAdmin {
			hash, err := bcrypt.GenerateFromPassword([]byte(newPass), bcrypt.DefaultCost)
			if err != nil {
				log.Printf("ResetAdminPassword: bcrypt: %v", err)
				return
			}
			u.PasswordHash = string(hash)
			st.Upsert(store.TableUsers, u.ID, u)
			log.Printf("⚠️  RESET_ADMIN_PASSWORD applied — password reset for '%s'. Remove RESET_ADMIN_PASSWORD env var now.", u.Username)
			return
		}
	}
	log.Println("ResetAdminPassword: no super_admin found")
}
