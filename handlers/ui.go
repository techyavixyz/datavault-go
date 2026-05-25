package handlers

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"time"

	"datavault/models"
	"datavault/services"
	"datavault/store"

	"github.com/gin-gonic/gin"
)

// ── Template setup ─────────────────────────────────────────────────────────────

var funcMap = template.FuncMap{
	"fmtBytes": func(b *int64) string {
		if b == nil {
			return "—"
		}
		switch {
		case *b < 1024:
			return fmt.Sprintf("%d B", *b)
		case *b < 1048576:
			return fmt.Sprintf("%.1f KB", float64(*b)/1024)
		default:
			return fmt.Sprintf("%.2f MB", float64(*b)/1048576)
		}
	},
	"fmtBytesI": func(b int64) string {
		switch {
		case b < 1024:
			return fmt.Sprintf("%d Bytes", b)
		case b < 1048576:
			return fmt.Sprintf("%.1f KB", float64(b)/1024)
		default:
			return fmt.Sprintf("%.2f MB", float64(b)/1048576)
		}
	},
	"fmtTime": func(t time.Time) string {
		s := int(time.Since(t).Seconds())
		switch {
		case s < 60:
			return "just now"
		case s < 3600:
			return fmt.Sprintf("%dm ago", s/60)
		case s < 86400:
			return fmt.Sprintf("%dh ago", s/3600)
		default:
			return fmt.Sprintf("%dd ago", s/86400)
		}
	},
	"fmtTimePtr": func(t *time.Time) string {
		if t == nil {
			return "—"
		}
		s := int(time.Since(*t).Seconds())
		switch {
		case s < 60:
			return "just now"
		case s < 3600:
			return fmt.Sprintf("%dm ago", s/60)
		case s < 86400:
			return fmt.Sprintf("%dh ago", s/3600)
		default:
			return fmt.Sprintf("%dd ago", s/86400)
		}
	},
	"retNextRun": func(id string) *time.Time {
		return services.NextRun("ret-" + id)
	},
	"dotClass": func(status string) string {
		switch status {
		case "success":
			return "ok"
		case "failed":
			return "er"
		default:
			return status
		}
	},
	"truncate": func(s string, n int) string {
		if len(s) <= n {
			return s
		}
		return s[:n]
	},
	"jsonStr": func(v any) (template.JS, error) {
		b, err := json.Marshal(v)
		return template.JS(b), err
	},
	"initial": func(s string) string {
		if s == "" {
			return "?"
		}
		return strings.ToUpper(string([]rune(s)[0]))
	},
	"navItem": func(data any, page, href, icon, label string) (template.HTML, error) {
		// data is *PageData
		pd, ok := data.(*PageData)
		if !ok {
			return "", nil
		}
		active := ""
		if pd.Page == page {
			active = " active"
		}
		svgPaths := map[string]string{
			"grid":    `<rect x="2" y="2" width="5" height="5" rx="1"/><rect x="9" y="2" width="5" height="5" rx="1"/><rect x="2" y="9" width="5" height="5" rx="1"/><rect x="9" y="9" width="5" height="5" rx="1"/>`,
			"lock":    `<rect x="3" y="7" width="10" height="8" rx="1.5"/><path d="M5 7V5a3 3 0 0 1 6 0v2"/>`,
			"db":      `<ellipse cx="8" cy="5" rx="5" ry="2.5"/><path d="M3 5v3c0 1.38 2.24 2.5 5 2.5s5-1.12 5-2.5V5"/><path d="M3 8v3c0 1.38 2.24 2.5 5 2.5s5-1.12 5-2.5V8"/>`,
			"storage": `<rect x="2" y="3" width="12" height="4" rx="1"/><rect x="2" y="9" width="12" height="4" rx="1"/>`,
			"target":  `<circle cx="8" cy="8" r="6"/><circle cx="8" cy="8" r="3"/><circle cx="8" cy="8" r="1" fill="currentColor"/>`,
			"backup":  `<path d="M8 3v9M4 9l4 4 4-4"/><path d="M2 13h12"/>`,
			"refresh": `<path d="M2.5 8a5.5 5.5 0 1 0 .75-2.75"/><path d="M2 3.5l.75 2.75 2.75-.75"/>`,
			"folder":  `<path d="M2 4h4.5l1.5 2H14v8H2z"/>`,
			"clock":   `<circle cx="8" cy="8" r="6"/><path d="M8 5v3l2 2"/>`,
			"trash":   `<path d="M3 5h10M8 5V3M5.5 5l.5 8h4l.5-8"/>`,
			"bell":    `<path d="M8 2a4 4 0 0 1 4 4v3l1.5 2.5H2.5L4 9V6a4 4 0 0 1 4-4z"/><path d="M6.5 13a1.5 1.5 0 0 0 3 0"/>`,
			"users":    `<circle cx="6" cy="6" r="3"/><path d="M2 14c0-2.76 1.79-5 4-5h4c2.21 0 4 2.24 4 5"/><circle cx="14" cy="6" r="2"/><path d="M14 9c1.66 0 3 1.57 3 3.5"/>`,
			"settings": `<circle cx="8" cy="8" r="2.5"/><path d="M8 2v1.5M8 12.5V14M2 8h1.5M12.5 8H14M3.5 3.5l1 1M11.5 11.5l1 1M11.5 3.5l-1 1M4.5 11.5l-1 1"/>`,
		}
		svg := fmt.Sprintf(`<svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">%s</svg>`, svgPaths[icon])
		html := fmt.Sprintf(`<a class="nav-item%s" hx-get="%s" hx-target="#content" hx-push-url="true" hx-swap="innerHTML" href="%s">%s %s</a>`,
			active, href, href, svg, label)
		return template.HTML(html), nil
	},
}

type UIHandler struct {
	Store *store.Store
	pages map[string]*template.Template
}

// PageData is passed to every template.
type PageData struct {
	Page     string
	Title    string
	Sub      string
	Data     any
	UserRole string
	Username string
}

func NewUIHandler(st *store.Store, tplFS fs.FS) *UIHandler {
	names := []string{"dashboard", "credentials", "sources", "destinations",
		"backups", "cronjobs", "restores", "restore_targets", "explorer", "retention", "notifications", "users", "profile", "settings"}
	pages := make(map[string]*template.Template, len(names))
	for _, name := range names {
		t := template.New("base.html").Funcs(funcMap)
		template.Must(t.ParseFS(tplFS, "templates/base.html", "templates/"+name+".html"))
		pages[name] = t
	}
	return &UIHandler{Store: st, pages: pages}
}

func (h *UIHandler) render(c *gin.Context, page, title, sub string, bodyData any) {
	role, _ := c.Get("user_role")
	uname, _ := c.Get("username")
	roleStr, _ := role.(string)
	unameStr, _ := uname.(string)
	pd := &PageData{Page: page, Title: title, Sub: sub, Data: bodyData, UserRole: roleStr, Username: unameStr}
	c.Header("Content-Type", "text/html; charset=utf-8")
	if c.GetHeader("HX-Request") == "true" {
		// HTMX swap — render only the body block
		h.pages[page].ExecuteTemplate(c.Writer, "body", pd)
	} else {
		// Full page — execute base which calls {{template "body" .}}
		h.pages[page].ExecuteTemplate(c.Writer, "base.html", pd)
	}
}

// ── Page handlers ─────────────────────────────────────────────────────────────

type DashActivityDay struct {
	Date      string `json:"date"`
	Completed int    `json:"completed"`
	Failed    int    `json:"failed"`
	Running   int    `json:"running"`
	Pending   int    `json:"pending"`
	Cancelled int    `json:"cancelled"`
}

type DashStorageDest struct {
	Name  string
	Count int
	Bytes int64
}

type DashJob struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	SourceName  string `json:"source_name"`
	DestName    string `json:"dest_name"`
	Status      string `json:"status"`
	StartedAt   string `json:"started_at"`
}

type DashData struct {
	TotalJobs       int
	ActiveSchedules int
	TotalStorage    int64
	SuccessRate     int
	Success24h      int
	Failed24h       int
	Activity        []DashActivityDay
	Recent          []DashJob
	StatusCompleted int
	StatusFailed    int
	StatusCancelled int
	Storage         []DashStorageDest
	StorageTotal    int64
	StorageUpdated  string
}

func (h *UIHandler) Dashboard(c *gin.Context) {
	var srcs []models.DatabaseSource
	var dsts []models.StorageDestination
	var bks []models.BackupRecord
	var crons []models.CronJob
	h.Store.GetAll(store.TableSources, &srcs)
	h.Store.GetAll(store.TableDestinations, &dsts)
	h.Store.GetAll(store.TableBackups, &bks)
	h.Store.GetAll(store.TableCronJobs, &crons)

	now := time.Now().UTC()

	// Active schedules
	activeSchedules := 0
	for _, cj := range crons {
		if cj.Enabled {
			activeSchedules++
		}
	}

	// Activity chart — last 14 days
	numDays := 14
	activity := make([]DashActivityDay, numDays)
	dayIdx := make(map[string]int, numDays)
	for i := 0; i < numDays; i++ {
		d := now.AddDate(0, 0, -(numDays - 1 - i))
		key := d.Format("Jan 2")
		activity[i] = DashActivityDay{Date: key}
		dayIdx[key] = i
	}

	// Per-destination storage map
	destMap := make(map[string]*DashStorageDest, len(dsts))
	for _, d := range dsts {
		dd := DashStorageDest{Name: d.Name}
		destMap[d.ID] = &dd
	}

	h30 := now.Add(-30 * 24 * time.Hour)
	h24 := now.Add(-24 * time.Hour)
	var totalStorage int64
	var completed30, failed30, cancelled30, success24, failed24 int

	sort.Slice(bks, func(i, j int) bool { return bks[i].StartedAt.After(bks[j].StartedAt) })

	for _, b := range bks {
		// Activity
		key := b.StartedAt.UTC().Format("Jan 2")
		if idx, ok := dayIdx[key]; ok {
			switch b.Status {
			case "success":
				activity[idx].Completed++
			case "failed":
				activity[idx].Failed++
			case "running":
				activity[idx].Running++
			case "pending":
				activity[idx].Pending++
			case "cancelled":
				activity[idx].Cancelled++
			}
		}
		// Storage
		if b.SizeBytes != nil {
			totalStorage += *b.SizeBytes
			if ds, ok := destMap[b.DestinationID]; ok {
				ds.Count++
				ds.Bytes += *b.SizeBytes
			}
		}
		// 30-day status
		if b.StartedAt.After(h30) {
			switch b.Status {
			case "success":
				completed30++
			case "failed":
				failed30++
			case "cancelled":
				cancelled30++
			}
		}
		// 24h stats
		if b.StartedAt.After(h24) {
			if b.Status == "success" {
				success24++
			} else if b.Status == "failed" {
				failed24++
			}
		}
	}

	// Success rate (last 30 days)
	successRate := 0
	if total30 := completed30 + failed30 + cancelled30; total30 > 0 {
		successRate = completed30 * 100 / total30
	}

	// Storage list sorted by bytes desc
	storageList := make([]DashStorageDest, 0, len(destMap))
	var storageTotal int64
	for _, ds := range destMap {
		storageList = append(storageList, *ds)
		storageTotal += ds.Bytes
	}
	sort.Slice(storageList, func(i, j int) bool {
		return storageList[i].Bytes > storageList[j].Bytes
	})

	// Build unified recent jobs (backups + restores), sorted newest first
	var restores []models.RestoreRecord
	h.Store.GetAll(store.TableRestores, &restores)

	allJobs := make([]DashJob, 0, len(bks)+len(restores))
	for _, b := range bks {
		allJobs = append(allJobs, DashJob{
			ID:         b.ID,
			Type:       "backup",
			SourceName: b.SourceName,
			DestName:   b.DestinationName,
			Status:     b.Status,
			StartedAt:  b.StartedAt.UTC().Format(time.RFC3339),
		})
	}
	for _, r := range restores {
		allJobs = append(allJobs, DashJob{
			ID:         r.ID,
			Type:       "restore",
			SourceName: r.TargetSourceName,
			DestName:   r.BackupFileName,
			Status:     r.Status,
			StartedAt:  r.StartedAt.UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(allJobs, func(i, j int) bool { return allJobs[i].StartedAt > allJobs[j].StartedAt })
	if len(allJobs) > 15 {
		allJobs = allJobs[:15]
	}

	_ = srcs

	h.render(c, "dashboard", "Dashboard", "Overview of your backup operations", DashData{
		TotalJobs:       len(bks),
		ActiveSchedules: activeSchedules,
		TotalStorage:    totalStorage,
		SuccessRate:     successRate,
		Success24h:      success24,
		Failed24h:       failed24,
		Activity:        activity,
		Recent:          allJobs,
		StatusCompleted: completed30,
		StatusFailed:    failed30,
		StatusCancelled: cancelled30,
		Storage:         storageList,
		StorageTotal:    storageTotal,
		StorageUpdated:  now.Format("01/02/2006 3:04 PM"),
	})
}

func (h *UIHandler) Credentials(c *gin.Context) {
	var rows []models.CredentialProfile
	h.Store.GetAll(store.TableCredentials, &rows)
	if rows == nil {
		rows = []models.CredentialProfile{}
	}
	h.render(c, "credentials", "Credentials", "Manage database credential profiles", rows)
}

type SourcesData struct {
	Sources []models.DatabaseSource
	Creds   []models.CredentialProfile
}

func (h *UIHandler) Sources(c *gin.Context) {
	var rows []models.DatabaseSource
	var creds []models.CredentialProfile
	h.Store.GetAll(store.TableSources, &rows)
	h.Store.GetAll(store.TableCredentials, &creds)
	h.render(c, "sources", "Database Sources", "Manage database connection sources", SourcesData{rows, creds})
}

func (h *UIHandler) Destinations(c *gin.Context) {
	var rows []models.StorageDestination
	h.Store.GetAll(store.TableDestinations, &rows)
	h.render(c, "destinations", "Storage Destinations", "Manage backup storage destinations", rows)
}

type BackupsData struct {
	Backups  []models.BackupRecord
	Sources  []models.DatabaseSource
	Dests    []models.StorageDestination
	Channels []models.NotificationChannel
}

func (h *UIHandler) Backups(c *gin.Context) {
	var bks []models.BackupRecord
	var srcs []models.DatabaseSource
	var dsts []models.StorageDestination
	var channels []models.NotificationChannel
	h.Store.GetAll(store.TableBackups, &bks)
	h.Store.GetAll(store.TableSources, &srcs)
	h.Store.GetAll(store.TableDestinations, &dsts)
	h.Store.GetAll(store.TableNotifications, &channels)
	sort.Slice(bks, func(i, j int) bool { return bks[i].StartedAt.After(bks[j].StartedAt) })
	h.render(c, "backups", "Backup Jobs", "Create and monitor database backup operations", BackupsData{bks, srcs, dsts, channels})
}

type CronData struct {
	Jobs     []models.CronJob
	Sources  []models.DatabaseSource
	Dests    []models.StorageDestination
	Channels []models.NotificationChannel
}

func (h *UIHandler) CronJobs(c *gin.Context) {
	var jobs []models.CronJob
	var srcs []models.DatabaseSource
	var dsts []models.StorageDestination
	var channels []models.NotificationChannel
	h.Store.GetAll(store.TableCronJobs, &jobs)
	h.Store.GetAll(store.TableSources, &srcs)
	h.Store.GetAll(store.TableDestinations, &dsts)
	h.Store.GetAll(store.TableNotifications, &channels)
	for i, j := range jobs {
		jobs[i].NextRunAt = NextRun(j.ID)
	}
	h.render(c, "cronjobs", "Cron Jobs", "Scheduled automatic backup operations", CronData{jobs, srcs, dsts, channels})
}

// NextRun is the scheduler's next-run lookup; reassigned in main to break init ordering.
var NextRun = func(id string) *time.Time { return services.NextRun(id) }

type RestorePageData struct {
	Restores []models.RestoreRecord
	Backups  []models.BackupRecord
	Targets  []models.DatabaseSource
}

func (h *UIHandler) Restores(c *gin.Context) {
	var rs []models.RestoreRecord
	var bks []models.BackupRecord
	var targets []models.DatabaseSource
	h.Store.GetAll(store.TableRestores, &rs)
	h.Store.GetAll(store.TableBackups, &bks)
	h.Store.GetAll(store.TableRestoreTargets, &targets)
	sort.Slice(rs, func(i, j int) bool { return rs[i].StartedAt.After(rs[j].StartedAt) })
	h.render(c, "restores", "Restore Jobs", "Restore a database from a backup", RestorePageData{rs, bks, targets})
}

type RestoreTargetsPageData struct {
	Sources []models.DatabaseSource
	Creds   []models.CredentialProfile
}

func (h *UIHandler) RestoreTargets(c *gin.Context) {
	var rows []models.DatabaseSource
	var creds []models.CredentialProfile
	h.Store.GetAll(store.TableRestoreTargets, &rows)
	h.Store.GetAll(store.TableCredentials, &creds)
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
	h.render(c, "restore_targets", "Restore Targets", "Databases to restore backups into", RestoreTargetsPageData{rows, creds})
}

func (h *UIHandler) PartialRestoreTargets(c *gin.Context) {
	var rows []models.DatabaseSource
	var creds []models.CredentialProfile
	h.Store.GetAll(store.TableRestoreTargets, &rows)
	h.Store.GetAll(store.TableCredentials, &creds)
	c.Header("Content-Type", "text/html; charset=utf-8")
	h.pages["restore_targets"].ExecuteTemplate(c.Writer, "rt-cards", RestoreTargetsPageData{rows, creds})
}

type RetentionPageData struct {
	Policies []models.RetentionPolicy
	Sources  []models.DatabaseSource
	Dests    []models.StorageDestination
}

func (h *UIHandler) Retention(c *gin.Context) {
	var policies []models.RetentionPolicy
	var srcs []models.DatabaseSource
	var dsts []models.StorageDestination
	h.Store.GetAll(store.TableRetentionPolicies, &policies)
	h.Store.GetAll(store.TableSources, &srcs)
	h.Store.GetAll(store.TableDestinations, &dsts)
	h.render(c, "retention", "Retention Policies", "Automatically prune old backup files", RetentionPageData{policies, srcs, dsts})
}

func (h *UIHandler) Profile(c *gin.Context) {
	userID, _ := c.Get("user_id")
	var user models.User
	h.Store.GetByID(store.TableUsers, userID.(string), &user)
	safe := models.UserSafe{ID: user.ID, Username: user.Username, Email: user.Email, Role: user.Role, CreatedAt: user.CreatedAt}
	h.render(c, "profile", "My Profile", "Manage your account details and password", safe)
}

func (h *UIHandler) Users(c *gin.Context) {
	var users []models.User
	h.Store.GetAll(store.TableUsers, &users)
	safe := deduplicateUsers(users)
	callerID, _ := c.Get("user_id")
	h.render(c, "users", "User Management", "Manage accounts and roles", gin.H{
		"Users":    safe,
		"CallerID": callerID,
	})
}

func (h *UIHandler) Settings(c *gin.Context) {
	var s models.ServerSettings
	h.Store.GetByID(store.TableSettings, "server", &s)
	if s.Timezone == "" {
		s.Timezone = "UTC"
	}
	h.render(c, "settings", "Settings", "Server configuration & backup", s)
}

func (h *UIHandler) Notifications(c *gin.Context) {
	var channels []models.NotificationChannel
	h.Store.GetAll(store.TableNotifications, &channels)
	if channels == nil {
		channels = []models.NotificationChannel{}
	}
	h.render(c, "notifications", "Notification Channels", "Send alerts on backup success or failure", channels)
}

func (h *UIHandler) PartialRetention(c *gin.Context) {
	var policies []models.RetentionPolicy
	var srcs []models.DatabaseSource
	var dsts []models.StorageDestination
	h.Store.GetAll(store.TableRetentionPolicies, &policies)
	h.Store.GetAll(store.TableSources, &srcs)
	h.Store.GetAll(store.TableDestinations, &dsts)
	c.Header("Content-Type", "text/html; charset=utf-8")
	h.pages["retention"].ExecuteTemplate(c.Writer, "ret-cards", RetentionPageData{policies, srcs, dsts})
}

type ExplorerData struct{ Dests []models.StorageDestination }

func (h *UIHandler) Explorer(c *gin.Context) {
	var dsts []models.StorageDestination
	h.Store.GetAll(store.TableDestinations, &dsts)
	h.render(c, "explorer", "File Explorer", "Browse and download backup files", ExplorerData{dsts})
}

// ── Partial handlers (HTMX list refreshes) ───────────────────────────────────

func (h *UIHandler) PartialCredentials(c *gin.Context) {
	var rows []models.CredentialProfile
	h.Store.GetAll(store.TableCredentials, &rows)
	if rows == nil {
		rows = []models.CredentialProfile{}
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	h.pages["credentials"].ExecuteTemplate(c.Writer, "cred-cards", rows)
}

func (h *UIHandler) PartialSources(c *gin.Context) {
	var rows []models.DatabaseSource
	var creds []models.CredentialProfile
	h.Store.GetAll(store.TableSources, &rows)
	h.Store.GetAll(store.TableCredentials, &creds)
	c.Header("Content-Type", "text/html; charset=utf-8")
	h.pages["sources"].ExecuteTemplate(c.Writer, "src-cards", SourcesData{rows, creds})
}

func (h *UIHandler) PartialDestinations(c *gin.Context) {
	var rows []models.StorageDestination
	h.Store.GetAll(store.TableDestinations, &rows)
	c.Header("Content-Type", "text/html; charset=utf-8")
	h.pages["destinations"].ExecuteTemplate(c.Writer, "dest-cards", rows)
}

func (h *UIHandler) PartialBackups(c *gin.Context) {
	var bks []models.BackupRecord
	h.Store.GetAll(store.TableBackups, &bks)
	sort.Slice(bks, func(i, j int) bool { return bks[i].StartedAt.After(bks[j].StartedAt) })
	c.Header("Content-Type", "text/html; charset=utf-8")
	h.pages["backups"].ExecuteTemplate(c.Writer, "bk-table", bks)
}

func (h *UIHandler) PartialCronJobs(c *gin.Context) {
	var jobs []models.CronJob
	var srcs []models.DatabaseSource
	var dsts []models.StorageDestination
	h.Store.GetAll(store.TableCronJobs, &jobs)
	h.Store.GetAll(store.TableSources, &srcs)
	h.Store.GetAll(store.TableDestinations, &dsts)
	for i, j := range jobs {
		jobs[i].NextRunAt = NextRun(j.ID)
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	var ch []models.NotificationChannel
	h.Store.GetAll(store.TableNotifications, &ch)
	h.pages["cronjobs"].ExecuteTemplate(c.Writer, "cron-table", CronData{jobs, srcs, dsts, ch})
}

// ── SSE log streaming ─────────────────────────────────────────────────────────

func (h *UIHandler) RestoreLogStream(c *gin.Context) {
	id := c.Param("id")
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	ticker := time.NewTicker(800 * time.Millisecond)
	defer ticker.Stop()
	lastIdx := 0

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-ticker.C:
			var rec models.RestoreRecord
			ok, _ := h.Store.GetByID(store.TableRestores, id, &rec)
			if !ok {
				return
			}
			for i := lastIdx; i < len(rec.Log); i++ {
				line := strings.ReplaceAll(rec.Log[i], "\n", " ")
				fmt.Fprintf(c.Writer, "data: %s\n\n", line)
			}
			lastIdx = len(rec.Log)
			c.Writer.(http.Flusher).Flush()

			if rec.Status != "running" && rec.Status != "pending" {
				fmt.Fprintf(c.Writer, "event: done\ndata: %s\n\n", rec.Status)
				c.Writer.(http.Flusher).Flush()
				return
			}
		}
	}
}

func (h *UIHandler) BackupLogStream(c *gin.Context) {
	id := c.Param("id")
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	ticker := time.NewTicker(800 * time.Millisecond)
	defer ticker.Stop()
	lastIdx := 0

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-ticker.C:
			var rec models.BackupRecord
			ok, _ := h.Store.GetByID(store.TableBackups, id, &rec)
			if !ok {
				return
			}
			for i := lastIdx; i < len(rec.Log); i++ {
				line := strings.ReplaceAll(rec.Log[i], "\n", " ")
				fmt.Fprintf(c.Writer, "data: %s\n\n", line)
			}
			lastIdx = len(rec.Log)
			c.Writer.(http.Flusher).Flush()

			if rec.Status != "running" && rec.Status != "pending" {
				fmt.Fprintf(c.Writer, "event: done\ndata: %s\n\n", rec.Status)
				c.Writer.(http.Flusher).Flush()
				return
			}
		}
	}
}
