package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"

	"datavault/handlers"
	"datavault/models"
	"datavault/services"
	"datavault/store"

	"github.com/gin-gonic/gin"
)

//go:embed templates/*
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

func main() {
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "/data/db.json"
	}

	st, err := store.New(dbPath)
	if err != nil {
		log.Fatalf("store init: %v", err)
	}

	handlers.CleanupUsers(st)
	handlers.SeedAdminUser(st)
	handlers.ResetAdminPassword(st)
	handlers.LoadSettings(st)

	services.StartScheduler()
	defer services.StopScheduler()
	handlers.LoadScheduledJobs(st)
	handlers.LoadRetentionSchedules(st)

	r := gin.Default()

	// ── Static assets ─────────────────────────────────────────────────────────
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("static fs: %v", err)
	}
	r.StaticFS("/static", http.FS(staticSub))

	// ── Template FS ───────────────────────────────────────────────────────────
	tplSub, err := fs.Sub(templateFS, ".")
	if err != nil {
		log.Fatalf("template fs: %v", err)
	}

	// ── Auth (public routes) ──────────────────────────────────────────────────
	auth := handlers.NewAuthHandler(st, tplSub)
	r.GET("/signin", auth.SignInPage)
	r.GET("/signup", auth.SignUpPage)
	r.GET("/forgot", auth.ForgotPage)
	r.POST("/api/auth/signin", auth.SignIn)
	r.POST("/api/auth/signup", auth.SignUp)
	r.POST("/api/auth/signout", auth.SignOut)
	r.POST("/api/auth/forgot", auth.ForgotRequest)

	// Convenience role sets
	adminPlus := auth.RequireRole(models.RoleAdmin, models.RoleSuperAdmin)
	superOnly := auth.RequireRole(models.RoleSuperAdmin)

	// ── Protected UI pages ────────────────────────────────────────────────────
	ui := handlers.NewUIHandler(st, tplSub)

	web := r.Group("/")
	web.Use(auth.AuthRequired())
	{
		web.GET("/", func(c *gin.Context) { c.Redirect(http.StatusFound, "/dashboard") })
		web.GET("/dashboard", ui.Dashboard)
		web.GET("/credentials", ui.Credentials)
		web.GET("/sources", ui.Sources)
		web.GET("/destinations", ui.Destinations)
		web.GET("/backups", ui.Backups)
		web.GET("/cronjobs", ui.CronJobs)
		web.GET("/restores", ui.Restores)
		web.GET("/restore_targets", ui.RestoreTargets)
		web.GET("/explorer", ui.Explorer)
		web.GET("/retention", ui.Retention)
		web.GET("/notifications", ui.Notifications)
		web.GET("/users", superOnly, ui.Users)
		web.GET("/settings", superOnly, ui.Settings)
		web.GET("/profile", ui.Profile)

		// HTMX partials
		web.GET("/partials/retention", ui.PartialRetention)
		web.GET("/partials/credentials", ui.PartialCredentials)
		web.GET("/partials/sources", ui.PartialSources)
		web.GET("/partials/restore_targets", ui.PartialRestoreTargets)
		web.GET("/partials/destinations", ui.PartialDestinations)
		web.GET("/partials/backups", ui.PartialBackups)
		web.GET("/partials/cronjobs", ui.PartialCronJobs)

		// SSE log streams
		web.GET("/api/backups/:id/stream", ui.BackupLogStream)
		web.GET("/api/restores/:id/stream", ui.RestoreLogStream)
	}

	// ── Protected JSON API ────────────────────────────────────────────────────
	api := r.Group("/api")
	api.Use(auth.AuthRequired())
	{
		// ── Credentials (admin+) ──────────────────────────────────────────────
		cred := &handlers.CredHandler{Store: st}
		api.GET("/credentials", cred.List)
		api.GET("/credentials/:id", cred.Get)
		api.POST("/credentials", adminPlus, cred.Create)
		api.PUT("/credentials/:id", adminPlus, cred.Update)
		api.DELETE("/credentials/:id", adminPlus, cred.Delete)

		// ── Sources (read: all; write: admin+) ───────────────────────────────
		src := &handlers.SourceHandler{Store: st}
		api.GET("/sources", src.List)
		api.GET("/sources/:id", src.Get)
		api.POST("/sources", adminPlus, src.Create)
		api.PUT("/sources/:id", adminPlus, src.Update)
		api.DELETE("/sources/:id", adminPlus, src.Delete)
		api.POST("/sources/test", adminPlus, src.TestLive)
		api.POST("/sources/:id/test", adminPlus, src.Test)

		// ── Destinations (read: all; write: admin+) ──────────────────────────
		dest := &handlers.DestHandler{Store: st}
		api.GET("/destinations", dest.List)
		api.GET("/destinations/:id", dest.Get)
		api.POST("/destinations", adminPlus, dest.Create)
		api.PUT("/destinations/:id", adminPlus, dest.Update)
		api.DELETE("/destinations/:id", adminPlus, dest.Delete)
		api.POST("/destinations/test", adminPlus, dest.TestLive)
		api.POST("/destinations/:id/test", adminPlus, dest.Test)

		// ── Backups (read: all; create/delete: admin+) ────────────────────────
		bk := &handlers.BackupHandler{Store: st}
		api.GET("/backups", bk.List)
		api.GET("/backups/:id", bk.Get)
		api.POST("/backups", adminPlus, bk.Create)
		api.DELETE("/backups/:id", adminPlus, bk.Delete)

		// ── Restores (all roles can create/view) ──────────────────────────────
		rs := &handlers.RestoreHandler{Store: st}
		api.GET("/restores", rs.List)
		api.GET("/restores/:id", rs.Get)
		api.POST("/restores", rs.Create)

		// ── Restore targets (read: all; write: admin+) ────────────────────────
		rt := &handlers.RestoreTargetHandler{Store: st}
		api.GET("/restore_targets", rt.List)
		api.GET("/restore_targets/:id", rt.Get)
		api.POST("/restore_targets", adminPlus, rt.Create)
		api.PUT("/restore_targets/:id", adminPlus, rt.Update)
		api.DELETE("/restore_targets/:id", adminPlus, rt.Delete)
		api.POST("/restore_targets/test", adminPlus, rt.TestLive)
		api.POST("/restore_targets/:id/test", adminPlus, rt.Test)

		// ── Explorer (all roles can list and download) ────────────────────────
		ex := &handlers.ExplorerHandler{Store: st}
		api.GET("/explorer/:dest_id/files", ex.ListFiles)
		api.GET("/explorer/:dest_id/download", ex.Download)

		// ── Notifications (read: all; write: admin+) ──────────────────────────
		notif := &handlers.NotificationHandler{Store: st}
		api.GET("/notifications", notif.List)
		api.GET("/notifications/:id", notif.Get)
		api.POST("/notifications", adminPlus, notif.Create)
		api.POST("/notifications/test", adminPlus, notif.Test)
		api.PUT("/notifications/:id", adminPlus, notif.Update)
		api.DELETE("/notifications/:id", adminPlus, notif.Delete)

		// ── Retention (read: all; write: admin+) ──────────────────────────────
		ret := &handlers.RetentionHandler{Store: st}
		api.GET("/retention", ret.List)
		api.GET("/retention/:id", ret.Get)
		api.POST("/retention", adminPlus, ret.Create)
		api.PUT("/retention/:id", adminPlus, ret.Update)
		api.DELETE("/retention/:id", adminPlus, ret.Delete)
		api.POST("/retention/:id/apply", adminPlus, ret.Apply)

		// ── Cron jobs (read: all; write: admin+) ──────────────────────────────
		cj := &handlers.CronHandler{Store: st}
		api.GET("/cronjobs", cj.List)
		api.GET("/cronjobs/:id", cj.Get)
		api.POST("/cronjobs", adminPlus, cj.Create)
		api.PUT("/cronjobs/:id", adminPlus, cj.Update)
		api.DELETE("/cronjobs/:id", adminPlus, cj.Delete)
		api.POST("/cronjobs/:id/run", adminPlus, cj.Run)
		api.POST("/cronjobs/:id/toggle", adminPlus, cj.Toggle)

		// ── Self-service profile (all roles) ─────────────────────────────────
		prof := &handlers.ProfileHandler{Store: st}
		api.GET("/profile", prof.Get)
		api.PUT("/profile", prof.Update)

		// ── User management (super_admin only) ───────────────────────────────
		users := &handlers.UserHandler{Store: st}
		api.GET("/users", superOnly, users.List)
		api.POST("/users", superOnly, users.Create)
		api.PUT("/users/:id", superOnly, users.Update)
		api.DELETE("/users/:id", superOnly, users.Delete)

		// ── Config export/import (super_admin only) ───────────────────────────
		cfg := &handlers.ConfigHandler{Store: st}
		api.GET("/config/export", superOnly, cfg.Export)
		api.POST("/config/import", superOnly, cfg.Import)

		// ── Server settings (super_admin only) ───────────────────────────────
		ss := &handlers.ServerSettingsHandler{Store: st}
		api.GET("/settings", superOnly, ss.Get)
		api.PUT("/settings", superOnly, ss.Update)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}
	log.Printf("DataVault listening on :%s", port)
	r.Run(":" + port)
}
