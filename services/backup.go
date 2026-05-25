// Backup engine: dump databases to NDJSON .tar.gz, upload to storage.
package services

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"datavault/models"
	"datavault/store"
)

// resolveTmpDir returns the directory to use for backup/restore scratch space.
// Priority: server settings TmpDir → DV_TMP_DIR env var → OS default (usually /tmp).
func resolveTmpDir(st *store.Store) string {
	if st != nil {
		var s models.ServerSettings
		st.GetByID(store.TableSettings, "server", &s)
		if s.TmpDir != "" {
			return s.TmpDir
		}
	}
	if d := os.Getenv("DV_TMP_DIR"); d != "" {
		return d
	}
	return "" // os.MkdirTemp("", ...) uses os.TempDir() when dir is ""
}

// resolvePattern substitutes tokens in a pattern string.
func resolvePattern(pattern, sourceName, label string, ts time.Time) string {
	pad := func(n int) string { return fmt.Sprintf("%02d", n) }
	date := fmt.Sprintf("%d-%s-%s", ts.Year(), pad(int(ts.Month())), pad(ts.Day()))
	timestamp := fmt.Sprintf("%d-%s-%s_%s-%s-%s",
		ts.Year(), pad(int(ts.Month())), pad(ts.Day()),
		pad(ts.Hour()), pad(ts.Minute()), pad(ts.Second()))

	r := strings.NewReplacer(
		"{source}", sanitizeName(sourceName),
		"{label}", sanitizeName(label),
		"{date}", date,
		"{year}", fmt.Sprintf("%d", ts.Year()),
		"{month}", pad(int(ts.Month())),
		"{day}", pad(ts.Day()),
		"{hour}", pad(ts.Hour()),
		"{minute}", pad(ts.Minute()),
		"{timestamp}", timestamp,
	)
	return r.Replace(pattern)
}

func sanitizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			b.WriteRune(c)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

// RunBackup executes a backup job asynchronously, updating the record in store.
func RunBackup(rec *models.BackupRecord, src *models.DatabaseSource,
	dest *models.StorageDestination, profile *models.CredentialProfile,
	st *store.Store, folderPattern, filenamePattern string) {

	save := func() { st.Upsert(store.TableBackups, rec.ID, rec) }
	logf := func(format string, args ...any) {
		line := fmt.Sprintf("[%s] %s",
			time.Now().UTC().Format("2006-01-02 15:04:05"),
			fmt.Sprintf(format, args...))
		rec.Log = append(rec.Log, line)
		save()
	}

	rec.Status = "running"
	save()

	ts := time.Now().UTC()

	// resolve folder and filename
	subfolder := ""
	if folderPattern != "" {
		subfolder = resolvePattern(folderPattern, src.Name, rec.Label, ts)
	}
	baseName := ""
	if filenamePattern != "" {
		baseName = resolvePattern(filenamePattern, src.Name, rec.Label, ts)
	} else {
		pad := func(n int) string { return fmt.Sprintf("%02d", n) }
		baseName = fmt.Sprintf("%s_%d-%s-%s_%s-%s-%s",
			sanitizeName(src.Name),
			ts.Year(), pad(int(ts.Month())), pad(ts.Day()),
			pad(ts.Hour()), pad(ts.Minute()), pad(ts.Second()))
	}
	ext := ".tar"
	if rec.Compress {
		ext = ".tar.gz"
	}
	filename := baseName + ext

	logf("Starting backup — %s (%s) → %s (%s)", src.Name, src.DBType, dest.Name, dest.StorageType)

	// resolve connection URI
	uri, err := ResolveURI(src, profile)
	if err != nil {
		fail(rec, save, logf, "URI resolution failed: "+err.Error())
		return
	}

	tmpDir, err := os.MkdirTemp(resolveTmpDir(st), "dv-backup-*")
	if err != nil {
		fail(rec, save, logf, "tmp dir: "+err.Error())
		return
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, filename)

	var recordCount int
	backupMode := "ndjson"

	// ── mongodump path ────────────────────────────────────────────────────────
	if src.DBType == models.DBMongoDB {
		backupMode = "mongodump"
		// mongodump --archive --gzip: single self-contained file, no tar wrapping.
		// The .mongodump.gz extension lets restore stream it directly into
		// mongorestore stdin with zero disk writes.
		filename = baseName + ".mongodump.gz"
		archivePath = filepath.Join(tmpDir, filename)
		if err := runMongodumpArchive(uri, src.TargetDatabase, src.TargetCollection, archivePath, logf); err != nil {
			fail(rec, save, logf, "mongodump failed: "+err.Error())
			return
		}

	} else if src.DBType == models.DBRedis && rec.RedisMode == "rdb" {
		// ── RDB snapshot ──────────────────────────────────────────────────────
		backupMode = "rdb"
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		logRedisBackupInfo(ctx, uri, "all", logf)
		dataPath := filepath.Join(tmpDir, "dump.rdb")
		if err := dumpRedisRDB(ctx, uri, dataPath, logf); err != nil {
			fail(rec, save, logf, "RDB dump failed: "+err.Error())
			return
		}
		dataSum, _ := sha256File(dataPath)
		dataFI, _ := os.Stat(dataPath)
		var dataSizeBytes int64
		if dataFI != nil {
			dataSizeBytes = dataFI.Size()
		}
		metaObj := map[string]any{
			"backup_id":        rec.ID,
			"source_name":      src.Name,
			"db_type":          string(src.DBType),
			"db_version":       dbVersion(src, uri),
			"destination_name": dest.Name,
			"storage_type":     string(dest.StorageType),
			"backup_mode":      backupMode,
			"data_file":        "dump.rdb",
			"data_sha256":      dataSum,
			"data_size_bytes":  dataSizeBytes,
			"compress":         rec.Compress,
			"label":            rec.Label,
			"created_at":       ts.Format(time.RFC3339),
			"generated_by":     "DataVault v2.0.0-go",
		}
		metaBytes, _ := json.Marshal(metaObj)
		if err := packTarWithMeta(dataPath, "dump.rdb", metaBytes, archivePath, rec.Compress); err != nil {
			fail(rec, save, logf, "archive failed: "+err.Error())
			return
		}

	} else {
		// ── NDJSON path (Redis keys, Postgres, MySQL) ─────────────────────────
		dataPath := filepath.Join(tmpDir, "data.ndjson")
		n, err := dumpDB(src, uri, dataPath, logf)
		if err != nil {
			fail(rec, save, logf, "dump failed: "+err.Error())
			return
		}
		recordCount = n
		logf("Dumped %d records", n)

		dataSum, _ := sha256File(dataPath)
		dataFI, _ := os.Stat(dataPath)
		var dataSizeBytes int64
		if dataFI != nil {
			dataSizeBytes = dataFI.Size()
		}
		metaObj := map[string]any{
			"backup_id":        rec.ID,
			"source_name":      src.Name,
			"db_type":          string(src.DBType),
			"db_version":       dbVersion(src, uri),
			"destination_name": dest.Name,
			"storage_type":     string(dest.StorageType),
			"backup_mode":      backupMode,
			"data_file":        "data.ndjson",
			"data_sha256":      dataSum,
			"data_size_bytes":  dataSizeBytes,
			"compress":         rec.Compress,
			"label":            rec.Label,
			"created_at":       ts.Format(time.RFC3339),
			"generated_by":     "DataVault v2.0.0-go",
		}
		if recordCount > 0 {
			metaObj["record_count"] = recordCount
		}
		metaBytes, _ := json.Marshal(metaObj)
		if err := packTarWithMeta(dataPath, "data.ndjson", metaBytes, archivePath, rec.Compress); err != nil {
			fail(rec, save, logf, "archive failed: "+err.Error())
			return
		}
	}

	// Compute checksum of the final archive (used for download integrity verification)
	fi, _ := os.Stat(archivePath)
	if fi != nil {
		sz := fi.Size()
		rec.SizeBytes = &sz
	}
	if sum, err := sha256File(archivePath); err == nil {
		rec.Checksum = sum
		logf("SHA-256 (archive): %s", sum)
	}

	logf("Uploading %s...", filename)
	remotePath, err := UploadFile(dest, archivePath, filename, subfolder, logf)
	if err != nil {
		fail(rec, save, logf, "upload failed: "+err.Error())
		return
	}

	now := time.Now().UTC()
	rec.RemotePath = remotePath
	rec.FileName = filename
	rec.Status = "success"
	rec.FinishedAt = &now
	logf("Backup complete → %s", remotePath)
	save()
}

func fail(rec *models.BackupRecord, save func(), logf func(string, ...any), msg string) {
	now := time.Now().UTC()
	rec.Status = "failed"
	rec.Error = msg
	rec.FinishedAt = &now
	logf("ERROR: %s", msg)
	save()
}

// dbVersion queries the server version string for the given source.
// Returns an empty string on any error (non-fatal — version is best-effort).
func dbVersion(src *models.DatabaseSource, uri string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	switch src.DBType {
	case models.DBRedis:
		rdb, err := newRedisClient(uri)
		if err != nil {
			return ""
		}
		defer rdb.Close()
		info, err := rdb.Info(ctx, "server").Result()
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(info, "\n") {
			if strings.HasPrefix(line, "redis_version:") {
				return strings.TrimSpace(strings.TrimPrefix(line, "redis_version:"))
			}
		}

	case models.DBMongoDB:
		client, err := newMongoClient(ctx, uri)
		if err != nil {
			return ""
		}
		defer client.Disconnect(ctx)
		var res struct {
			Version string `bson:"version"`
		}
		if err := client.Database("admin").RunCommand(ctx, map[string]any{"buildInfo": 1}).Decode(&res); err != nil {
			return ""
		}
		return res.Version

	case models.DBPostgres:
		conn, err := newPgConn(ctx, uri)
		if err != nil {
			return ""
		}
		defer conn.Close(ctx)
		var v string
		if err := conn.QueryRow(ctx, "SHOW server_version").Scan(&v); err != nil {
			return ""
		}
		return v

	case models.DBMySQL:
		db, err := newMySQLDB(uri)
		if err != nil {
			return ""
		}
		defer db.Close()
		var v string
		if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&v); err != nil {
			return ""
		}
		return v
	}
	return ""
}

// dumpDB writes all records as NDJSON to path, returns record count.
func dumpDB(src *models.DatabaseSource, uri, path string, logf func(string, ...any)) (int, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	enc := json.NewEncoder(f)

	switch src.DBType {
	case models.DBRedis:
		return dumpRedis(ctx, uri, src.TargetDatabase, enc, logf)
	case models.DBPostgres:
		return dumpPostgres(ctx, uri, src.TargetDatabase, enc, logf)
	case models.DBMySQL:
		return dumpMySQL(uri, src.TargetDatabase, enc, logf)
	}
	return 0, fmt.Errorf("unknown db_type: %s", src.DBType)
}

// redisDBInfo holds parsed keyspace info for one Redis logical database.
type redisDBInfo struct {
	index   int
	keys    int64
	expires int64
}

// logRedisBackupInfo queries INFO keyspace and INFO memory and logs a pre-backup summary.
// targetDB: empty or "all" = all databases; "0","1",... = specific db index.
func logRedisBackupInfo(ctx context.Context, uri, targetDB string, logf func(string, ...any)) {
	rdb, err := newRedisClient(uri)
	if err != nil {
		logf("Warning: could not connect for pre-backup info: %v", err)
		return
	}
	defer rdb.Close()

	// ── Keyspace info ─────────────────────────────────────────────────────────
	ks, err := rdb.Info(ctx, "keyspace").Result()
	var allDBs []redisDBInfo
	if err == nil {
		for _, line := range strings.Split(ks, "\n") {
			line = strings.TrimSpace(line)
			// Format: db0:keys=100,expires=5,avg_ttl=0
			if !strings.HasPrefix(line, "db") {
				continue
			}
			var idx int
			var keys, expires int64
			if _, err := fmt.Sscanf(line, "db%d:keys=%d,expires=%d", &idx, &keys, &expires); err == nil {
				allDBs = append(allDBs, redisDBInfo{index: idx, keys: keys, expires: expires})
			}
		}
	}

	// ── Memory info ───────────────────────────────────────────────────────────
	mem, _ := rdb.Info(ctx, "memory").Result()
	usedMemHuman := ""
	for _, line := range strings.Split(mem, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "used_memory_human:") {
			usedMemHuman = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
			break
		}
	}

	// ── Determine scope ───────────────────────────────────────────────────────
	backupAll := targetDB == "" || targetDB == "all"
	var targetIdx int
	if !backupAll {
		fmt.Sscanf(targetDB, "%d", &targetIdx)
	}

	logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	logf("Redis Backup Info")
	logf("  Total DBs with keys : %d", len(allDBs))
	logf("  Memory used         : %s", usedMemHuman)

	if backupAll {
		logf("  Scope               : all databases")
		var totalKeys int64
		for _, d := range allDBs {
			totalKeys += d.keys
		}
		logf("  Total keys          : %d", totalKeys)
		logf("")
		for _, d := range allDBs {
			logf("  db%-3d  keys=%-8d  expires=%d", d.index, d.keys, d.expires)
		}
	} else {
		logf("  Scope               : db%d", targetIdx)
		var found bool
		for _, d := range allDBs {
			if d.index == targetIdx {
				logf("  Keys in db%d         : %d  (expires: %d)", d.index, d.keys, d.expires)
				found = true
				break
			}
		}
		if !found {
			logf("  db%d                 : empty (0 keys)", targetIdx)
		}
	}
	logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
}

func dumpRedis(ctx context.Context, uri, targetDB string, enc *json.Encoder, logf func(string, ...any)) (int, error) {
	logRedisBackupInfo(ctx, uri, targetDB, logf)

	rdb, err := newRedisClient(uri)
	if err != nil {
		return 0, err
	}
	defer rdb.Close()

	logf("Scanning Redis keys...")
	var cursor uint64
	count := 0
	for {
		keys, next, err := rdb.Scan(ctx, cursor, "*", 200).Result()
		if err != nil {
			return count, err
		}
		for _, key := range keys {
			typ, _ := rdb.Type(ctx, key).Result()
			var val any
			switch typ {
			case "string":
				val, _ = rdb.Get(ctx, key).Result()
			case "list":
				val, _ = rdb.LRange(ctx, key, 0, -1).Result()
			case "set":
				val, _ = rdb.SMembers(ctx, key).Result()
			case "zset":
				val, _ = rdb.ZRangeWithScores(ctx, key, 0, -1).Result()
			case "hash":
				val, _ = rdb.HGetAll(ctx, key).Result()
			default:
				val = nil
			}
			ttl, _ := rdb.TTL(ctx, key).Result()
			row := map[string]any{"key": key, "type": typ, "value": val, "ttl": ttl.Seconds()}
			if err := enc.Encode(row); err != nil {
				return count, err
			}
			count++
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return count, nil
}

var mongoSystemDBs = map[string]bool{"admin": true, "local": true, "config": true}

type mongoDBInfo struct {
	name        string
	dataSize    int64
	storageSize int64
	collections int64
	objects     int64
}

// mongoDBStats queries sizes and collection counts for user databases.
// If dbName is non-empty only that database is queried.
func mongoDBStats(ctx context.Context, uri, dbName string) ([]mongoDBInfo, error) {
	client, err := newMongoClient(ctx, uri)
	if err != nil {
		return nil, err
	}
	defer client.Disconnect(ctx)

	var names []string
	if dbName != "" {
		names = []string{dbName}
	} else {
		all, err := client.ListDatabaseNames(ctx, map[string]any{})
		if err != nil {
			return nil, err
		}
		for _, n := range all {
			if !mongoSystemDBs[n] {
				names = append(names, n)
			}
		}
	}

	infos := make([]mongoDBInfo, 0, len(names))
	for _, n := range names {
		// Use float64 — MongoDB returns dbStats numbers as BSON double in many versions.
		var result struct {
			DataSize    float64 `bson:"dataSize"`
			StorageSize float64 `bson:"storageSize"`
			Collections float64 `bson:"collections"`
			Objects     float64 `bson:"objects"`
		}
		if err := client.Database(n).RunCommand(ctx, map[string]any{"dbStats": 1, "scale": 1}).Decode(&result); err != nil {
			// Non-fatal: include with zero sizes
			infos = append(infos, mongoDBInfo{name: n})
			continue
		}
		infos = append(infos, mongoDBInfo{
			name:        n,
			dataSize:    int64(result.DataSize),
			storageSize: int64(result.StorageSize),
			collections: int64(result.Collections),
			objects:     int64(result.Objects),
		})
	}
	return infos, nil
}

// runMongodump shells out to mongodump per-database so we can log progress.
// If dbName is empty all user databases are dumped one by one.
func runMongodump(uri, dbName, collName, outDir string, logf func(string, ...any)) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dbs, err := mongoDBStats(ctx, uri, dbName)
	if err != nil {
		logf("Warning: could not fetch database stats: %v", err)
		// Fall back to a single mongodump call without per-db stats
		return execMongodump(uri, dbName, collName, outDir, 0, 0, logf)
	}

	// ── Summary header ────────────────────────────────────────────────────────
	var totalData, totalStorage, totalObjects int64
	for _, d := range dbs {
		totalData += d.dataSize
		totalStorage += d.storageSize
		totalObjects += d.objects
	}
	logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	logf("MongoDB Backup Summary")
	logf("  Databases : %d", len(dbs))
	logf("  Data size : %s", fmtBytes(totalData))
	logf("  On disk   : %s", fmtBytes(totalStorage))
	logf("  Documents : %d", totalObjects)
	logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	if dbName != "" {
		// Single database
		d := dbs[0]
		logf("[%s] data=%s  on-disk=%s  collections=%d  documents=%d",
			d.name, fmtBytes(d.dataSize), fmtBytes(d.storageSize), d.collections, d.objects)
		return execMongodump(uri, dbName, collName, outDir, d.dataSize, d.storageSize, logf)
	}

	// All databases — dump one by one so we can log per-db progress
	for i, d := range dbs {
		logf("[%d/%d] Dumping %s  data=%s  on-disk=%s  collections=%d  documents=%d",
			i+1, len(dbs), d.name, fmtBytes(d.dataSize), fmtBytes(d.storageSize), d.collections, d.objects)
		if err := execMongodump(uri, d.name, "", outDir, d.dataSize, d.storageSize, logf); err != nil {
			return err
		}
	}
	return nil
}

// execMongodump runs a single mongodump command for one database (or all if dbName="").
func execMongodump(uri, dbName, collName, outDir string, _, _ int64, logf func(string, ...any)) error {
	args := []string{
		"--uri=" + uri,
		"--out=" + outDir,
	}
	if dbName != "" {
		args = append(args, "--db="+dbName)
	}
	if collName != "" {
		args = append(args, "--collection="+collName)
	}
	cmd := exec.Command("mongodump", args...)
	out, err := cmd.CombinedOutput()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			logf("  %s", line)
		}
	}
	if err != nil {
		return fmt.Errorf("mongodump [%s] failed: %w", dbName, err)
	}
	return nil
}


// runMongodumpArchive runs mongodump with --archive --gzip, writing a single
// compressed file to outFile. Restore can stream it directly to mongorestore
// stdin with no disk writes.
func runMongodumpArchive(uri, dbName, collName, outFile string, logf func(string, ...any)) error {
	statsCtx, statsCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer statsCancel()

	dbs, err := mongoDBStats(statsCtx, uri, dbName)
	if err != nil {
		logf("Warning: could not fetch database stats: %v", err)
	} else {
		var totalData, totalStorage, totalObjects int64
		for _, d := range dbs {
			totalData += d.dataSize
			totalStorage += d.storageSize
			totalObjects += d.objects
		}
		logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		logf("MongoDB Backup Summary")
		logf("  Databases : %d", len(dbs))
		logf("  Data size : %s", fmtBytes(totalData))
		logf("  On disk   : %s", fmtBytes(totalStorage))
		logf("  Documents : %d", totalObjects)
		logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	}

	args := []string{
		"--uri=" + uri,
		"--archive=" + outFile,
		"--gzip",
	}
	if dbName != "" {
		args = append(args, "--db="+dbName)
	}
	if collName != "" {
		args = append(args, "--collection="+collName)
	}
	logf("Running: mongodump --archive --gzip ...")
	cmd := exec.Command("mongodump", args...)
	pipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("mongodump pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("mongodump start: %w", err)
	}
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		logf("  %s", scanner.Text())
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("mongodump failed: %w", err)
	}
	return nil
}

// packTarDir writes an entire directory tree plus metadata.json into a tar archive.
func packTarDir(srcDir, entryPrefix string, metaBytes []byte, archivePath string, compress bool) error {
	out, err := os.Create(archivePath)
	if err != nil {
		return err
	}
	defer out.Close()

	var tw *tar.Writer
	if compress {
		gw := gzip.NewWriter(out)
		defer gw.Close()
		tw = tar.NewWriter(gw)
	} else {
		tw = tar.NewWriter(out)
	}
	defer tw.Close()

	if err := filepath.Walk(srcDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		entryName := filepath.Join(entryPrefix, rel)
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		hdr.Name = entryName
		if fi.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	}); err != nil {
		return err
	}

	// Append metadata.json
	now := time.Now().UTC()
	if err := tw.WriteHeader(&tar.Header{
		Name:    "metadata.json",
		Mode:    0644,
		Size:    int64(len(metaBytes)),
		ModTime: now,
	}); err != nil {
		return err
	}
	_, err = tw.Write(metaBytes)
	return err
}

func dumpPostgres(ctx context.Context, uri, dbName string, enc *json.Encoder, logf func(string, ...any)) (int, error) {
	conn, err := newPgConn(ctx, uri)
	if err != nil {
		return 0, err
	}
	defer conn.Close(ctx)

	rows, err := conn.Query(ctx,
		"SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'")
	if err != nil {
		return 0, err
	}
	var tables []string
	for rows.Next() {
		var t string
		rows.Scan(&t)
		tables = append(tables, t)
	}
	rows.Close()

	count := 0
	for _, tbl := range tables {
		logf("Dumping table: %s", tbl)
		trows, err := conn.Query(ctx, fmt.Sprintf("SELECT * FROM %q", tbl))
		if err != nil {
			return count, err
		}
		cols := trows.FieldDescriptions()
		tableCount := 0
		for trows.Next() {
			vals, err := trows.Values()
			if err != nil {
				trows.Close()
				return count, err
			}
			doc := make(map[string]any, len(cols))
			for i, col := range cols {
				doc[string(col.Name)] = vals[i]
			}
			row := map[string]any{"_table": tbl, "row": doc}
			if err := enc.Encode(row); err != nil {
				trows.Close()
				return count, err
			}
			count++
			tableCount++
		}
		trows.Close()
		logf("  → %d rows", tableCount)
	}
	return count, nil
}

func dumpMySQL(uri, dbName string, enc *json.Encoder, logf func(string, ...any)) (int, error) {
	db, err := newMySQLDB(uri)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	rows, err := db.Query("SHOW TABLES")
	if err != nil {
		return 0, err
	}
	var tables []string
	for rows.Next() {
		var t string
		rows.Scan(&t)
		tables = append(tables, t)
	}
	rows.Close()

	count := 0
	for _, tbl := range tables {
		logf("Dumping table: %s", tbl)
		trows, err := db.Query(fmt.Sprintf("SELECT * FROM `%s`", tbl))
		if err != nil {
			return count, err
		}
		cols, _ := trows.Columns()
		for trows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := trows.Scan(ptrs...); err != nil {
				trows.Close()
				return count, err
			}
			doc := make(map[string]any, len(cols))
			for i, col := range cols {
				b, ok := vals[i].([]byte)
				if ok {
					doc[col] = string(b)
				} else {
					doc[col] = vals[i]
				}
			}
			row := map[string]any{"_table": tbl, "row": doc}
			if err := enc.Encode(row); err != nil {
				trows.Close()
				return count, err
			}
			count++
		}
		trows.Close()
	}
	return count, nil
}

// dumpRedisRDB uses the Redis replication PSYNC protocol to obtain a full RDB snapshot
// covering ALL databases and writes it to rdbPath.
//
// This requires a self-hosted Redis that permits replica connections. Managed/cloud
// Redis services (Redis Cloud, AWS ElastiCache, Upstash, etc.) typically reject PSYNC
// — use NDJSON mode for those.
func dumpRedisRDB(ctx context.Context, uri, rdbPath string, logf func(string, ...any)) error {
	addr, username, password, err := parseRedisURI(uri)
	if err != nil {
		return err
	}

	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}

	reader := bufio.NewReaderSize(conn, 1<<20) // 1 MiB read buffer

	// writeCmd sends a command using the RESP multi-bulk protocol for maximum compatibility.
	writeCmd := func(args ...string) error {
		var b strings.Builder
		fmt.Fprintf(&b, "*%d\r\n", len(args))
		for _, a := range args {
			fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
		}
		_, err := io.WriteString(conn, b.String())
		return err
	}
	readLine := func() (string, error) {
		line, err := reader.ReadString('\n')
		return strings.TrimRight(line, "\r\n"), err
	}

	// PING
	if err := writeCmd("PING"); err != nil {
		return fmt.Errorf("PING write: %w", err)
	}
	resp, err := readLine()
	if err != nil {
		return fmt.Errorf("PING read: %w", err)
	}
	logf("RDB> PING → %q", resp)
	if !strings.Contains(resp, "PONG") {
		return fmt.Errorf("unexpected PING response: %q", resp)
	}

	// AUTH — Redis 6+ supports AUTH <username> <password>; older versions use AUTH <password>
	if password != "" {
		var authErr error
		if username != "" && username != "default" {
			authErr = writeCmd("AUTH", username, password)
		} else {
			authErr = writeCmd("AUTH", password)
		}
		if authErr != nil {
			return fmt.Errorf("AUTH write: %w", authErr)
		}
		resp, err = readLine()
		if err != nil {
			return fmt.Errorf("AUTH read: %w", err)
		}
		logf("RDB> AUTH → %q", resp)
		if !strings.HasPrefix(resp, "+OK") {
			return fmt.Errorf("AUTH failed: %s", resp)
		}
	}

	// REPLCONF listening-port 0 — soft fail, some servers reject it
	_ = writeCmd("REPLCONF", "listening-port", "0")
	r1, _ := readLine()
	logf("RDB> REPLCONF listening-port → %q", r1)

	// REPLCONF capa eof capa psync2 — soft fail
	_ = writeCmd("REPLCONF", "capa", "eof", "capa", "psync2")
	r2, _ := readLine()
	logf("RDB> REPLCONF capa → %q", r2)

	// PSYNC ? -1 — request full resync; server replies with +FULLRESYNC <replid> <offset>.
	// Skip leading blank lines — some servers flush a stray \r\n after REPLCONF before responding.
	if err := writeCmd("PSYNC", "?", "-1"); err != nil {
		return fmt.Errorf("PSYNC write: %w", err)
	}
	resp = ""
	for i := 0; i < 10 && resp == ""; i++ {
		resp, err = readLine()
		if err != nil {
			return fmt.Errorf("PSYNC read: %w", err)
		}
	}
	logf("RDB> PSYNC → %q", resp)
	if !strings.HasPrefix(resp, "+FULLRESYNC") {
		if strings.HasPrefix(resp, "-") {
			return fmt.Errorf("server rejected PSYNC (managed/cloud Redis blocks replication — use NDJSON mode instead): %s", resp)
		}
		if resp == "" {
			return fmt.Errorf("PSYNC returned empty response — this Redis does not support replication (proxy, Sentinel front-end, or managed service). Use NDJSON mode instead")
		}
		return fmt.Errorf("unexpected PSYNC response: %q", resp)
	}
	logf("PSYNC handshake: %s", resp)

	// Read RDB bulk header: $<size> — skip any empty lines before it
	sizeLine := ""
	for i := 0; i < 5 && sizeLine == ""; i++ {
		sizeLine, err = readLine()
		if err != nil {
			return fmt.Errorf("RDB header read: %w", err)
		}
	}
	if !strings.HasPrefix(sizeLine, "$") {
		return fmt.Errorf("expected RDB bulk header, got: %q", sizeLine)
	}

	f, err := os.Create(rdbPath)
	if err != nil {
		return err
	}
	defer f.Close()

	payload := sizeLine[1:] // strip leading '$'
	if strings.HasPrefix(payload, "EOF:") {
		// EOF-marker mode: server streams RDB then appends the 40-byte hex marker.
		// Sent when the client advertised "capa eof" in REPLCONF.
		marker := []byte(strings.TrimPrefix(payload, "EOF:"))
		logf("Downloading RDB snapshot (EOF marker mode, all databases)...")
		n, err := copyUntilEOFMarker(f, reader, marker)
		if err != nil {
			return fmt.Errorf("read RDB data (EOF mode): %w", err)
		}
		logf("RDB snapshot saved (%d bytes)", n)
	} else {
		// Size-prefixed mode
		size, err := strconv.ParseInt(payload, 10, 64)
		if err != nil {
			return fmt.Errorf("parse RDB size %q: %w", payload, err)
		}
		logf("Downloading RDB snapshot (%d bytes, all databases)...", size)
		if _, err := io.CopyN(f, reader, size); err != nil {
			return fmt.Errorf("read RDB data: %w", err)
		}
		logf("RDB snapshot saved (%d bytes)", size)
	}
	return nil
}

// copyUntilEOFMarker copies src → dst until the marker bytes appear,
// writing everything before the marker and discarding the marker itself.
func copyUntilEOFMarker(dst io.Writer, src io.Reader, marker []byte) (int64, error) {
	ml := len(marker)
	chunk := make([]byte, 32*1024)
	hold := make([]byte, 0, ml) // tail bytes held for split-marker detection
	var written int64

	for {
		n, readErr := src.Read(chunk)
		if n > 0 {
			data := append(hold, chunk[:n]...)
			hold = hold[:0]

			if idx := bytes.Index(data, marker); idx >= 0 {
				if idx > 0 {
					wn, werr := dst.Write(data[:idx])
					written += int64(wn)
					if werr != nil {
						return written, werr
					}
				}
				return written, nil
			}

			// No marker yet — flush all but the last (ml-1) bytes which
			// might be the beginning of a marker split across reads.
			safeLen := len(data) - (ml - 1)
			if safeLen < 0 {
				safeLen = 0
			}
			if safeLen > 0 {
				wn, werr := dst.Write(data[:safeLen])
				written += int64(wn)
				if werr != nil {
					return written, werr
				}
			}
			hold = append(hold[:0], data[safeLen:]...)
		}
		if readErr != nil {
			if readErr == io.EOF {
				return written, fmt.Errorf("stream ended before EOF marker was found")
			}
			return written, readErr
		}
	}
}

// packTarWithMeta writes dataPath (as dataEntry) plus an inline metadata.json
// into a tar archive (optionally gzip-compressed) at archivePath.
func packTarWithMeta(dataPath, dataEntry string, metaBytes []byte, archivePath string, compress bool) error {
	out, err := os.Create(archivePath)
	if err != nil {
		return err
	}
	defer out.Close()

	var tw *tar.Writer
	if compress {
		gw := gzip.NewWriter(out)
		defer gw.Close()
		tw = tar.NewWriter(gw)
	} else {
		tw = tar.NewWriter(out)
	}
	defer tw.Close()

	// Write data file
	fi, err := os.Stat(dataPath)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name:    dataEntry,
		Mode:    0644,
		Size:    fi.Size(),
		ModTime: fi.ModTime(),
	}); err != nil {
		return err
	}
	f, err := os.Open(dataPath)
	if err != nil {
		return err
	}
	if _, err := copyBuf(tw, f); err != nil {
		f.Close()
		return err
	}
	f.Close()

	// Write metadata.json
	now := time.Now().UTC()
	if err := tw.WriteHeader(&tar.Header{
		Name:    "metadata.json",
		Mode:    0644,
		Size:    int64(len(metaBytes)),
		ModTime: now,
	}); err != nil {
		return err
	}
	_, err = tw.Write(metaBytes)
	return err
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyBuf(dst interface{ Write([]byte) (int, error) }, src interface{ Read([]byte) (int, error) }) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			wn, werr := dst.Write(buf[:n])
			total += int64(wn)
			if werr != nil {
				return total, werr
			}
		}
		if err != nil {
			if err.Error() == "EOF" {
				return total, nil
			}
			return total, err
		}
	}
}
