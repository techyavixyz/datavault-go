package models

import "time"

type DatabaseType string

const (
	DBRedis      DatabaseType = "redis"
	DBMongoDB    DatabaseType = "mongodb"
	DBPostgres   DatabaseType = "postgresql"
	DBMySQL      DatabaseType = "mysql"
)

type StorageType string

const (
	StoreGCS StorageType = "gcs"
	StoreS3  StorageType = "s3"
	StoreNFS StorageType = "nfs"
	StoreSMB StorageType = "smb"
	StoreSSH StorageType = "ssh"
)

// ── Credential Profile ────────────────────────────────────────────────────────

type CredentialProfile struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	DBType          DatabaseType `json:"db_type"`
	Username        string       `json:"username,omitempty"`
	Password        string       `json:"password,omitempty"`
	AccessKeyID     string       `json:"access_key_id,omitempty"`
	SecretAccessKey string       `json:"secret_access_key,omitempty"`
	Host            string       `json:"host,omitempty"`
	Port            *int         `json:"port,omitempty"`
	Database        string       `json:"database,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
}

// ── Database Source ───────────────────────────────────────────────────────────

type DatabaseSource struct {
	ID                  string       `json:"id"`
	Name                string       `json:"name"`
	DBType              DatabaseType `json:"db_type"`
	ConnectionMode      string       `json:"connection_mode"` // "uri" | "profile"
	URI                 string       `json:"uri,omitempty"`
	CredentialProfileID string       `json:"credential_profile_id,omitempty"`
	Host                string       `json:"host,omitempty"`
	Port                *int         `json:"port,omitempty"`
	TargetDatabase      string       `json:"target_database,omitempty"`
	TargetCollection    string       `json:"target_collection,omitempty"`
	DockerContainer     string       `json:"docker_container,omitempty"` // MongoDB in Docker: container name/id for mongorestore via docker exec
	ConnStatus          string       `json:"conn_status,omitempty"` // "connected" | "disconnected"
	CreatedAt           time.Time    `json:"created_at"`
}

// ── Storage Destinations ──────────────────────────────────────────────────────

type GCSConfig struct {
	BucketName         string `json:"bucket_name"`
	Prefix             string `json:"prefix"`
	ServiceAccountJSON string `json:"service_account_json,omitempty"`
	ProjectID          string `json:"project_id,omitempty"`
}

type S3Config struct {
	BucketName      string `json:"bucket_name"`
	Prefix          string `json:"prefix"`
	Region          string `json:"region"`
	AccessKeyID     string `json:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	EndpointURL     string `json:"endpoint_url,omitempty"`
}

type NFSConfig struct {
	MountPath    string `json:"mount_path"`
	SubDirectory string `json:"sub_directory"`
}

type SMBConfig struct {
	Host         string `json:"host"`
	Share        string `json:"share"`
	Username     string `json:"username,omitempty"`
	Password     string `json:"password,omitempty"`
	Domain       string `json:"domain,omitempty"`
	SubDirectory string `json:"sub_directory"`
}

type SSHConfig struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	RemotePath string `json:"remote_path"`
}

type StorageDestination struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	StorageType StorageType `json:"storage_type"`
	GCS         *GCSConfig  `json:"gcs,omitempty"`
	S3          *S3Config   `json:"s3,omitempty"`
	NFS         *NFSConfig  `json:"nfs,omitempty"`
	SMB         *SMBConfig  `json:"smb,omitempty"`
	SSH         *SSHConfig  `json:"ssh,omitempty"`
	ConnStatus  string      `json:"conn_status,omitempty"` // "connected" | "disconnected"
	CreatedAt   time.Time   `json:"created_at"`
}

// ── Backup ────────────────────────────────────────────────────────────────────

type BackupRequest struct {
	SourceID        string `json:"source_id"`
	DestinationID   string `json:"destination_id"`
	Label           string `json:"label,omitempty"`
	Compress        bool   `json:"compress"`
	FolderPattern   string `json:"folder_pattern,omitempty"`
	FilenamePattern string `json:"filename_pattern,omitempty"`
	RedisMode               string   `json:"redis_mode,omitempty"`
	NotificationChannelIDs  []string `json:"notification_channel_ids,omitempty"`
}

type BackupRecord struct {
	ID              string       `json:"id"`
	SourceID        string       `json:"source_id"`
	SourceName      string       `json:"source_name"`
	DBType          DatabaseType `json:"db_type"`
	DestinationID   string       `json:"destination_id"`
	DestinationName string       `json:"destination_name"`
	StorageType     StorageType  `json:"storage_type"`
	RemotePath      string       `json:"remote_path"`
	FileName        string       `json:"file_name"`
	SizeBytes       *int64       `json:"size_bytes"`
	Checksum        string       `json:"checksum,omitempty"` // SHA-256 hex of the archive
	Compress        bool         `json:"compress"`
	RedisMode       string       `json:"redis_mode,omitempty"` // "" (NDJSON) | "rdb" (RDB snapshot)
	Status          string       `json:"status"`               // pending|running|success|failed
	Error           string       `json:"error,omitempty"`
	StartedAt       time.Time    `json:"started_at"`
	FinishedAt      *time.Time   `json:"finished_at,omitempty"`
	Label           string       `json:"label,omitempty"`
	Log             []string     `json:"log"`
}

// ── Cron Job ──────────────────────────────────────────────────────────────────

type CronJob struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	SourceID        string     `json:"source_id"`
	DestinationID   string     `json:"destination_id"`
	Label           string     `json:"label,omitempty"`
	Compress        bool       `json:"compress"`
	FolderPattern   string     `json:"folder_pattern,omitempty"`
	FilenamePattern string     `json:"filename_pattern,omitempty"`
	CronExpression  string     `json:"cron_expression"`
	Enabled         bool       `json:"enabled"`
	RedisMode               string     `json:"redis_mode,omitempty"`
	HeartbeatURL            string     `json:"heartbeat_url,omitempty"`
	NotificationChannelIDs  []string   `json:"notification_channel_ids,omitempty"`
	LastRunAt               *time.Time `json:"last_run_at,omitempty"`
	NextRunAt       *time.Time `json:"next_run_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

// ── Retention Policy ──────────────────────────────────────────────────────────

type RetentionPolicy struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	SourceID      string     `json:"source_id"`
	DestinationID string     `json:"destination_id"`
	KeepAll       bool       `json:"keep_all"`
	KeepLast      int        `json:"keep_last,omitempty"`    // keep last N successful backups
	KeepDaily     int        `json:"keep_daily,omitempty"`   // keep one/day for N days
	KeepWeekly    int        `json:"keep_weekly,omitempty"`  // keep one/week for N weeks
	KeepMonthly   int        `json:"keep_monthly,omitempty"` // keep one/month for N months
	KeepYearly    int        `json:"keep_yearly,omitempty"`  // keep one/year for N years
	Schedule      string     `json:"schedule,omitempty"` // "" | "hourly" | "daily" | "weekly"
	LastRunAt     *time.Time `json:"last_run_at,omitempty"`
	LastDeleted   int        `json:"last_deleted,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

type RetentionResult struct {
	Kept    int      `json:"kept"`
	Deleted int      `json:"deleted"`
	Errors  []string `json:"errors,omitempty"`
}

// ── Server Settings ───────────────────────────────────────────────────────────

type ServerSettings struct {
	Timezone string `json:"timezone"` // IANA timezone name, e.g. "America/New_York"
	TmpDir   string `json:"tmp_dir,omitempty"` // directory for backup/restore scratch space; defaults to OS temp dir
}

// ── Auth ──────────────────────────────────────────────────────────────────────

const (
	RoleSuperAdmin = "super_admin"
	RoleAdmin      = "admin"
	RoleNormal     = "normal"
)

type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	Email        string    `json:"email,omitempty"`
	Role         string    `json:"role"` // super_admin | admin | normal
	CreatedAt    time.Time `json:"created_at"`
}

// UserSafe is User without the password hash, safe to return over API.
type UserSafe struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email,omitempty"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// ── Notification Channels ─────────────────────────────────────────────────────

// NotificationChannel defines where and when to send backup event alerts.
// Type is one of: smtp | slack | googlechat | webhook
// Config keys per type:
//   smtp:       host, port, tls (starttls|tls|none), username, password, from, to
//   slack:      webhook_url
//   googlechat: webhook_url
//   webhook:    url, method (GET|POST)
type NotificationChannel struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Config    map[string]string `json:"config"`
	OnSuccess bool              `json:"on_success"`
	OnFailure bool              `json:"on_failure"`
	CreatedAt time.Time         `json:"created_at"`
}

// ── Restore ───────────────────────────────────────────────────────────────────

type RestoreRequest struct {
	BackupID       string `json:"backup_id"`
	TargetSourceID string `json:"target_source_id"`
	TmpDir         string `json:"tmp_dir,omitempty"` // override scratch directory for this job
}

type RestoreRecord struct {
	ID               string       `json:"id"`
	BackupID         string       `json:"backup_id"`
	BackupFileName   string       `json:"backup_file_name,omitempty"`
	DBType           DatabaseType `json:"db_type,omitempty"`
	TargetSourceID   string       `json:"target_source_id"`
	TargetSourceName string       `json:"target_source_name"`
	TmpDir           string       `json:"tmp_dir,omitempty"`
	Status           string       `json:"status"`
	Error            string       `json:"error,omitempty"`
	Log              []string     `json:"log"`
	StartedAt        time.Time    `json:"started_at"`
	FinishedAt       *time.Time   `json:"finished_at,omitempty"`
}
