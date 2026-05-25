package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"datavault/models"
	"datavault/services"
	"datavault/store"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const sessionCookie = "dv_session"

type AuthHandler struct {
	Store     *store.Store
	signinTpl *template.Template
	signupTpl *template.Template
	forgotTpl *template.Template
}

func NewAuthHandler(st *store.Store, tplFS fs.FS) *AuthHandler {
	signin := template.Must(template.New("signin.html").ParseFS(tplFS, "templates/signin.html"))
	signup := template.Must(template.New("signup.html").ParseFS(tplFS, "templates/signup.html"))
	forgot := template.Must(template.New("forgot.html").ParseFS(tplFS, "templates/forgot.html"))
	return &AuthHandler{Store: st, signinTpl: signin, signupTpl: signup, forgotTpl: forgot}
}

func (h *AuthHandler) SignInPage(c *gin.Context) {
	var users []models.User
	h.Store.GetAll(store.TableUsers, &users)
	if len(users) == 0 {
		c.Redirect(http.StatusFound, "/signup")
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	h.signinTpl.Execute(c.Writer, nil)
}

// SignUpPage only allows access when no users exist (first-time setup).
func (h *AuthHandler) SignUpPage(c *gin.Context) {
	var users []models.User
	h.Store.GetAll(store.TableUsers, &users)
	if len(users) > 0 {
		c.Redirect(http.StatusFound, "/signin")
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	h.signupTpl.Execute(c.Writer, nil)
}

func (h *AuthHandler) ForgotPage(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	h.forgotTpl.Execute(c.Writer, nil)
}

func (h *AuthHandler) SignIn(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}

	var users []models.User
	h.Store.GetAll(store.TableUsers, &users)
	var found *models.User
	for i := range users {
		if strings.EqualFold(users[i].Username, req.Username) {
			found = &users[i]
			break
		}
	}
	if found == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"detail": "Invalid username or password"})
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(found.PasswordHash), []byte(req.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"detail": "Invalid username or password"})
		return
	}

	token := services.CreateSession(found.ID)
	c.SetCookie(sessionCookie, token, int(7*24*time.Hour/time.Second), "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// SignUp only works when no users exist yet (first-time setup).
func (h *AuthHandler) SignUp(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Email    string `json:"email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}

	var users []models.User
	h.Store.GetAll(store.TableUsers, &users)
	if len(users) > 0 {
		c.JSON(http.StatusForbidden, gin.H{"detail": "Account creation is disabled after initial setup"})
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
		Role:         models.RoleSuperAdmin, // first user is always super_admin
		CreatedAt:    time.Now().UTC(),
	}
	h.Store.Upsert(store.TableUsers, user.ID, user)

	token := services.CreateSession(user.ID)
	c.SetCookie(sessionCookie, token, int(7*24*time.Hour/time.Second), "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ForgotRequest generates a temporary password and emails it to the user.
func (h *AuthHandler) ForgotRequest(c *gin.Context) {
	var req struct {
		Email string `json:"email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
		return
	}
	email := strings.TrimSpace(req.Email)
	if email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"detail": "Email is required"})
		return
	}

	var users []models.User
	h.Store.GetAll(store.TableUsers, &users)

	var found *models.User
	for i := range users {
		if strings.EqualFold(users[i].Email, email) {
			found = &users[i]
			break
		}
	}

	// Always return success — don't reveal whether the email is registered.
	if found == nil {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}

	tempPass := generateTempPassword()
	hash, err := bcrypt.GenerateFromPassword([]byte(tempPass), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Internal error"})
		return
	}
	found.PasswordHash = string(hash)
	h.Store.Upsert(store.TableUsers, found.ID, *found)

	subject := "[DataVault] Your login credentials"
	body := fmt.Sprintf(
		"Hello %s,\n\nYour DataVault login credentials:\n\n  Username:           %s\n  Temporary Password: %s\n\nSign in at /signin and change your password once you're in.\n\n— DataVault",
		found.Username, found.Username, tempPass,
	)
	if err := services.SendPlainEmail(h.Store, email, subject, body); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"detail": "Failed to send email: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *AuthHandler) SignOut(c *gin.Context) {
	token, _ := c.Cookie(sessionCookie)
	if token != "" {
		services.DeleteSession(token)
	}
	c.SetCookie(sessionCookie, "", -1, "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// AuthRequired enforces session auth and loads user info into the context.
func (h *AuthHandler) AuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(sessionCookie)
		if err != nil || token == "" {
			h.redirectOrAbort(c)
			return
		}
		userID, ok := services.ValidateSession(token)
		if !ok {
			c.SetCookie(sessionCookie, "", -1, "/", "", false, true)
			h.redirectOrAbort(c)
			return
		}
		var user models.User
		found, _ := h.Store.GetByID(store.TableUsers, userID, &user)
		if !found {
			c.SetCookie(sessionCookie, "", -1, "/", "", false, true)
			h.redirectOrAbort(c)
			return
		}
		if user.Role == "" {
			user.Role = models.RoleSuperAdmin
		}
		c.Set("user_id", userID)
		c.Set("user_role", user.Role)
		c.Set("username", user.Username)
		c.Next()
	}
}

// RequireRole returns a middleware that allows only the listed roles.
func (h *AuthHandler) RequireRole(roles ...string) gin.HandlerFunc {
	allowed := make(map[string]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}
	return func(c *gin.Context) {
		role, _ := c.Get("user_role")
		if !allowed[role.(string)] {
			if strings.HasPrefix(c.Request.URL.Path, "/api/") {
				c.JSON(http.StatusForbidden, gin.H{"detail": "insufficient permissions"})
			} else {
				c.Redirect(http.StatusFound, "/dashboard")
			}
			c.Abort()
			return
		}
		c.Next()
	}
}

func (h *AuthHandler) redirectOrAbort(c *gin.Context) {
	if strings.HasPrefix(c.Request.URL.Path, "/api/") {
		c.JSON(http.StatusUnauthorized, gin.H{"detail": "session expired"})
	} else {
		c.Redirect(http.StatusFound, "/signin")
	}
	c.Abort()
}

func generateTempPassword() string {
	const chars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789!@#"
	b := make([]byte, 32)
	rand.Read(b)
	pwd := make([]byte, 12)
	for i := range pwd {
		pwd[i] = chars[int(b[i])%len(chars)]
	}
	// Append a short hex suffix for entropy
	extra := make([]byte, 2)
	rand.Read(extra)
	return string(pwd) + hex.EncodeToString(extra)[:2]
}
