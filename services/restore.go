// Restore engine: download backup archive and replay into database.
package services

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"datavault/models"
	"datavault/store"

	rdbcore "github.com/hdt3213/rdb/core"
	rdbmodel "github.com/hdt3213/rdb/model"
	goredis "github.com/redis/go-redis/v9"
)

// diskFree returns available bytes on the filesystem that contains dir.
func diskFree(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// RunRestore executes a restore job asynchronously.
func RunRestore(rec *models.RestoreRecord, backup *models.BackupRecord,
	dest *models.StorageDestination, src *models.DatabaseSource,
	profile *models.CredentialProfile, st *store.Store) {

	save := func() { st.Upsert(store.TableRestores, rec.ID, rec) }
	logf := func(format string, args ...any) {
		line := fmt.Sprintf("[%s] %s",
			time.Now().UTC().Format("2006-01-02 15:04:05"),
			fmt.Sprintf(format, args...))
		rec.Log = append(rec.Log, line)
		save()
	}

	rec.Status = "running"
	save()

	if err := runRestore(rec, backup, dest, src, profile, st, logf); err != nil {
		now := time.Now().UTC()
		rec.Status = "failed"
		rec.Error = err.Error()
		rec.FinishedAt = &now
		logf("ERROR: %s", err.Error())
		save()
		return
	}

	now := time.Now().UTC()
	rec.Status = "success"
	rec.FinishedAt = &now
	logf("Restore complete")
	save()
}

func runRestore(rec *models.RestoreRecord, backup *models.BackupRecord,
	dest *models.StorageDestination, src *models.DatabaseSource,
	profile *models.CredentialProfile, st *store.Store, logf func(string, ...any)) (retErr error) {

	logf("Starting restore — %s → %s (%s)", backup.FileName, src.Name, src.DBType)

	isRDB := backup.RedisMode == "rdb" && src.DBType == models.DBRedis
	mongoArchiveStreamed := false

	// Resolve URI and ping the target DB before downloading anything.
	// A failed connection aborts immediately with no wasted bandwidth.
	uri, err := ResolveURI(src, profile)
	if err != nil {
		return err
	}
	logf("Checking connection to %s (%s)...", src.Name, src.DBType)
	if msg, err := TestConnection(src, profile); err != nil {
		return fmt.Errorf("connection check failed — %w", err)
	} else {
		logf("Connection OK — %s", msg)
	}

	jobTmpBase := rec.TmpDir
	if jobTmpBase == "" {
		jobTmpBase = resolveTmpDir(st)
	}
	tmpDir, err := os.MkdirTemp(jobTmpBase, "dv-restore-*")
	if err != nil {
		return err
	}
	// Cleanup runs on every exit path (success and error).
	// Named return retErr lets the defer know whether to log "Cleanup complete".
	defer func() {
		logf("Cleaning up temp files...")
		os.RemoveAll(tmpDir)
		if retErr == nil {
			logf("Cleanup complete")
		}
	}()

	// Log available disk space as an informational note only — no hard block.
	if backup.SizeBytes != nil && *backup.SizeBytes > 0 {
		if avail, err := diskFree(tmpDir); err == nil {
			logf("Disk space: archive is %s, available %s on %s",
				fmtBytes(*backup.SizeBytes), fmtBytes(avail), filepath.Dir(tmpDir))
		}
	}

	// Stream directly from storage into the tar extractor.
	// The archive is never written to disk — saves archive_size bytes of scratch space.
	logf("Streaming %s from %s (download + extract simultaneously)...", backup.FileName, dest.StorageType)
	stream, _, err := DownloadStream(dest, backup.RemotePath, logf)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer stream.Close()

	// SHA-256 is computed on the raw compressed bytes (same as what was checksummed at backup time).
	hasher := sha256.New()
	var r io.Reader = io.TeeReader(stream, hasher)

	// New-format MongoDB backups: mongodump --archive --gzip.
	// Pipe the raw stream directly into mongorestore stdin — zero disk writes.
	if src.DBType == models.DBMongoDB && strings.HasSuffix(backup.FileName, ".mongodump.gz") {
		logf("Restoring into %s via mongorestore (streaming, zero disk)...", src.Name)
		if err := streamToMongorestore(r, uri, src.TargetDatabase, src.DockerContainer, logf); err != nil {
			return err
		}
		io.Copy(io.Discard, r) // drain remainder so hasher sees all bytes
		stream.Close()
		if backup.Checksum != "" {
			logf("Verifying checksum...")
			got := hex.EncodeToString(hasher.Sum(nil))
			if got != backup.Checksum {
				return fmt.Errorf("checksum mismatch: expected %s, got %s", backup.Checksum, got)
			}
			logf("Checksum OK")
		}
		return nil
	}

	// Layer gzip decompression on top if the archive is compressed.
	if backup.Compress || strings.HasSuffix(backup.FileName, ".gz") {
		gr, err := gzip.NewReader(r)
		if err != nil {
			return fmt.Errorf("gzip open: %w", err)
		}
		defer gr.Close()
		r = gr
	}
	tr := tar.NewReader(r)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	if isRDB {
		logf("Extracting dump.rdb...")
		rdbPath := filepath.Join(tmpDir, "dump.rdb")
		if err := extractEntryFromTar(tr, "dump.rdb", rdbPath); err != nil {
			return fmt.Errorf("extract failed: %w", err)
		}
	} else if src.DBType == models.DBMongoDB {
		// Peek at first tar entry to detect backup format:
		//   .archive → mongodump archive format (stream direct to mongorestore)
		//   mongodump/ → old DataVault directory format (extract to disk)
		firstHdr, err := tr.Next()
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}
		if strings.HasSuffix(firstHdr.Name, ".archive") {
			logf("Detected mongodump archive format, restoring via streaming...")
			if err := streamToMongorestore(tr, uri, src.TargetDatabase, src.DockerContainer, logf); err != nil {
				return err
			}
			mongoArchiveStreamed = true
		} else {
			dumpDir := filepath.Join(tmpDir, "mongodump")
			if err := os.MkdirAll(dumpDir, 0755); err != nil {
				return fmt.Errorf("mkdir: %w", err)
			}
			logf("Extracting mongodump archive...")
			if err := extractMongoDir(tr, firstHdr, dumpDir); err != nil {
				return fmt.Errorf("extract failed: %w", err)
			}
		}
	} else {
		logf("Extracting data.ndjson...")
		ndjsonPath := filepath.Join(tmpDir, "data.ndjson")
		if err := extractEntryFromTar(tr, "data.ndjson", ndjsonPath); err != nil {
			return fmt.Errorf("extract failed: %w", err)
		}
	}

	// Drain any remaining tar entries (e.g., metadata.json) so the hasher
	// receives all compressed bytes and the checksum is complete.
	for {
		if _, err := tr.Next(); err != nil {
			break
		}
		io.Copy(io.Discard, tr)
	}
	stream.Close()

	// Verify checksum now that the full stream has been consumed.
	if backup.Checksum != "" {
		logf("Verifying checksum...")
		got := hex.EncodeToString(hasher.Sum(nil))
		if got != backup.Checksum {
			return fmt.Errorf("checksum mismatch: expected %s, got %s", backup.Checksum, got)
		}
		logf("Checksum OK")
	}

	if isRDB {
		rdbPath := filepath.Join(tmpDir, "dump.rdb")
		logf("Restoring RDB snapshot into %s...", src.Name)
		return restoreRedisRDB(ctx, uri, rdbPath, logf)
	}

	if src.DBType == models.DBMongoDB {
		if mongoArchiveStreamed {
			return nil
		}
		dumpDir := filepath.Join(tmpDir, "mongodump")
		logf("Restoring into %s via mongorestore...", src.Name)
		return runMongorestore(uri, src.TargetDatabase, dumpDir, src.DockerContainer, logf)
	}

	ndjsonPath := filepath.Join(tmpDir, "data.ndjson")
	logf("Restoring into %s (%s)...", src.Name, src.DBType)
	switch src.DBType {
	case models.DBRedis:
		return restoreRedis(ctx, uri, ndjsonPath)
	case models.DBPostgres:
		return restorePostgres(ctx, uri, ndjsonPath)
	case models.DBMySQL:
		return restoreMySQL(uri, ndjsonPath)
	}
	return fmt.Errorf("unknown db_type: %s", src.DBType)
}

// extractEntryFromTar extracts a single named entry from an already-open *tar.Reader.
func extractEntryFromTar(tr *tar.Reader, entryName, destPath string) error {
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s not found in archive", entryName)
		}
		if err != nil {
			return err
		}
		if hdr.Name == entryName {
			out, err := os.Create(destPath)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			out.Close()
			return err
		}
	}
}

// extractDirFromTar extracts all tar entries whose path starts with prefix/ into destDir.
func extractDirFromTar(tr *tar.Reader, prefix, destDir string) error {
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if !strings.HasPrefix(hdr.Name, prefix+"/") {
			continue
		}
		rel := strings.TrimPrefix(hdr.Name, prefix+"/")
		if rel == "" {
			continue
		}
		target := filepath.Join(destDir, rel)
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		out.Close()
	}
	return nil
}

// extractTarEntry extracts a single named entry from a tar (optionally gzipped) archive.
func extractTarEntry(archivePath, entryName, destPath string, compressed bool) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	var tr *tar.Reader
	if compressed || strings.HasSuffix(archivePath, ".gz") {
		gr, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gr.Close()
		tr = tar.NewReader(gr)
	} else {
		tr = tar.NewReader(f)
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Name == entryName {
			out, err := os.Create(destPath)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			out.Close()
			return err
		}
	}
	return fmt.Errorf("%s not found in archive", entryName)
}

// restoreRedisRDB parses an RDB binary file and replays all keys into Redis.
func restoreRedisRDB(ctx context.Context, uri, rdbPath string, logf func(string, ...any)) error {
	rdb, err := newRedisClient(uri)
	if err != nil {
		return err
	}
	defer rdb.Close()

	f, err := os.Open(rdbPath)
	if err != nil {
		return err
	}
	defer f.Close()

	currentDB := -1
	count := 0
	var parseErr error

	dec := rdbcore.NewDecoder(f)
	err = dec.Parse(func(obj rdbmodel.RedisObject) bool {
		db := obj.GetDBIndex()
		if db != currentDB {
			if e := rdb.Do(ctx, "SELECT", db).Err(); e != nil {
				parseErr = fmt.Errorf("SELECT %d: %w", db, e)
				return false
			}
			currentDB = db
		}

		key := obj.GetKey()
		exp := obj.GetExpiration()

		switch o := obj.(type) {
		case *rdbmodel.StringObject:
			rdb.Set(ctx, key, o.Value, 0)
		case *rdbmodel.ListObject:
			rdb.Del(ctx, key)
			if len(o.Values) > 0 {
				vals := make([]any, len(o.Values))
				for i, v := range o.Values {
					vals[i] = v
				}
				rdb.RPush(ctx, key, vals...)
			}
		case *rdbmodel.SetObject:
			rdb.Del(ctx, key)
			if len(o.Members) > 0 {
				vals := make([]any, len(o.Members))
				for i, v := range o.Members {
					vals[i] = v
				}
				rdb.SAdd(ctx, key, vals...)
			}
		case *rdbmodel.HashObject:
			rdb.Del(ctx, key)
			if len(o.Hash) > 0 {
				pairs := make([]any, 0, len(o.Hash)*2)
				for k, v := range o.Hash {
					pairs = append(pairs, k, v)
				}
				rdb.HSet(ctx, key, pairs...)
			}
		case *rdbmodel.ZSetObject:
			rdb.Del(ctx, key)
			if len(o.Entries) > 0 {
				members := make([]goredis.Z, len(o.Entries))
				for i, e := range o.Entries {
					members[i] = goredis.Z{Score: e.Score, Member: e.Member}
				}
				rdb.ZAdd(ctx, key, members...)
			}
		}

		if exp != nil && exp.After(time.Now()) {
			rdb.ExpireAt(ctx, key, *exp)
		}

		count++
		if count%500 == 0 {
			logf("  %d keys restored...", count)
		}
		return true
	})

	if parseErr != nil {
		return parseErr
	}
	if err != nil {
		return fmt.Errorf("rdb parse: %w", err)
	}

	logf("Restored %d keys from RDB snapshot", count)
	return nil
}

func restoreRedis(ctx context.Context, uri, ndjsonPath string) error {
	rdb, err := newRedisClient(uri)
	if err != nil {
		return err
	}
	defer rdb.Close()

	f, err := os.Open(ndjsonPath)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 10*1024*1024), 10*1024*1024)
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			continue
		}
		key, _ := row["key"].(string)
		if key == "" {
			continue
		}
		typ, _ := row["type"].(string)
		val := row["value"]
		ttlSec, _ := row["ttl"].(float64)
		ttl := time.Duration(-1)
		if ttlSec > 0 {
			ttl = time.Duration(ttlSec) * time.Second
		}

		switch typ {
		case "string":
			rdb.Set(ctx, key, fmt.Sprint(val), ttl)
		case "list":
			rdb.Del(ctx, key)
			if items, ok := val.([]any); ok {
				for _, item := range items {
					rdb.RPush(ctx, key, fmt.Sprint(item))
				}
			}
		case "set":
			rdb.Del(ctx, key)
			if items, ok := val.([]any); ok {
				for _, item := range items {
					rdb.SAdd(ctx, key, fmt.Sprint(item))
				}
			}
		case "hash":
			rdb.Del(ctx, key)
			if m, ok := val.(map[string]any); ok {
				for k, v := range m {
					rdb.HSet(ctx, key, k, fmt.Sprint(v))
				}
			}
		}
	}
	return scanner.Err()
}

// runCmdStream starts cmd, streams its stderr line-by-line via logf, and waits.
// Set cmd.Stdin before calling if needed.
func runCmdStream(cmd *exec.Cmd, logf func(string, ...any)) error {
	pipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		logf("%s", scanner.Text())
	}
	return cmd.Wait()
}

// runMongorestore shells out to mongorestore.
// dumpDir is the directory containing the mongodump output (the "mongodump/" subtree).
// If dbName is non-empty it overrides the target database for all collections.
func runMongorestore(uri, dbName, dumpDir, dockerContainer string, logf func(string, ...any)) error {
	if dockerContainer != "" {
		return runMongorestoreInDocker(uri, dbName, dumpDir, dockerContainer, logf)
	}

	args := []string{
		"--uri=" + uri,
		"--drop",
		dumpDir,
	}
	if dbName != "" {
		args = append(args, "--db="+dbName)
	}
	dbSuffix := ""
	if dbName != "" {
		dbSuffix = " --db=" + dbName
	}
	logf("Running: mongorestore --uri=<hidden>%s %s", dbSuffix, dumpDir)
	if err := runCmdStream(exec.Command("mongorestore", args...), logf); err != nil {
		return fmt.Errorf("mongorestore exited with error: %w", err)
	}
	return nil
}

// runMongorestoreInDocker copies the dump into the container and runs mongorestore
// via docker exec — no need for mongorestore on the host.
func runMongorestoreInDocker(uri, dbName, dumpDir, container string, logf func(string, ...any)) error {
	containerPath := fmt.Sprintf("/tmp/dv-mongodump-%d", time.Now().UnixNano())

	// Copy extracted dump directory into the container.
	logf("Copying dump into container %s...", container)
	cpOut, err := exec.Command("docker", "cp", dumpDir+"/.", container+":"+containerPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker cp failed: %w — %s", err, strings.TrimSpace(string(cpOut)))
	}

	// Always clean up the dump inside the container when we're done.
	defer func() {
		exec.Command("docker", "exec", container, "rm", "-rf", containerPath).Run()
	}()

	// Build mongorestore args for docker exec.
	args := []string{"exec", container, "mongorestore",
		"--uri=" + uri,
		"--drop",
		containerPath,
	}
	if dbName != "" {
		args = append(args, "--db="+dbName)
	}
	dbSuffix := ""
	if dbName != "" {
		dbSuffix = " --db=" + dbName
	}
	logf("Running: docker exec %s mongorestore --uri=<hidden>%s %s", container, dbSuffix, containerPath)
	if err := runCmdStream(exec.Command("docker", args...), logf); err != nil {
		return fmt.Errorf("mongorestore in container %s exited with error: %w", container, err)
	}
	return nil
}

// extractMongoDir extracts the mongodump/ directory from an already-open tar.
// firstHdr is the entry that was already read by the caller (format detection).
func extractMongoDir(tr *tar.Reader, firstHdr *tar.Header, destDir string) error {
	hdr := firstHdr
	for {
		if strings.HasPrefix(hdr.Name, "mongodump/") && hdr.Typeflag != tar.TypeDir {
			rel := strings.TrimPrefix(hdr.Name, "mongodump/")
			if rel != "" {
				outPath := filepath.Join(destDir, rel)
				if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
					return err
				}
				f, err := os.Create(outPath)
				if err != nil {
					return err
				}
				_, err = io.Copy(f, tr)
				f.Close()
				if err != nil {
					return err
				}
			}
		}
		var nextErr error
		hdr, nextErr = tr.Next()
		if nextErr == io.EOF {
			return nil
		}
		if nextErr != nil {
			return nextErr
		}
	}
}

// streamToMongorestore pipes r (a mongodump --archive --gzip stream) directly
// into mongorestore stdin. No files are written to disk.
func streamToMongorestore(r io.Reader, uri, dbName, dockerContainer string, logf func(string, ...any)) error {
	args := []string{"--uri=" + uri, "--archive", "--gzip", "--drop"}
	if dbName != "" {
		args = append(args, "--db="+dbName)
	}
	dbSuffix := ""
	if dbName != "" {
		dbSuffix = " --db=" + dbName
	}

	var cmd *exec.Cmd
	if dockerContainer != "" {
		dockerArgs := append([]string{"exec", "-i", dockerContainer, "mongorestore"}, args...)
		cmd = exec.Command("docker", dockerArgs...)
		logf("Running: docker exec -i %s mongorestore --uri=<hidden>%s --archive --gzip --drop", dockerContainer, dbSuffix)
	} else {
		cmd = exec.Command("mongorestore", args...)
		logf("Running: mongorestore --uri=<hidden>%s --archive --gzip --drop", dbSuffix)
	}

	cmd.Stdin = r
	if err := runCmdStream(cmd, logf); err != nil {
		return fmt.Errorf("mongorestore exited with error: %w", err)
	}
	return nil
}

// extractTarDir extracts all entries whose path starts with prefix into destDir.
func extractTarDir(archivePath, prefix, destDir string, compressed bool) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	var tr *tar.Reader
	if compressed || strings.HasSuffix(archivePath, ".gz") {
		gr, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gr.Close()
		tr = tar.NewReader(gr)
	} else {
		tr = tar.NewReader(f)
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		// Only extract entries under the prefix directory
		if !strings.HasPrefix(hdr.Name, prefix+"/") {
			continue
		}
		rel := strings.TrimPrefix(hdr.Name, prefix+"/")
		if rel == "" {
			continue
		}
		target := filepath.Join(destDir, rel)
		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		out.Close()
	}
	return nil
}

func restorePostgres(ctx context.Context, uri, ndjsonPath string) error {
	conn, err := newPgConn(ctx, uri)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	f, err := os.Open(ndjsonPath)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 10*1024*1024), 10*1024*1024)
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			continue
		}
		tbl, _ := row["_table"].(string)
		doc, _ := row["row"].(map[string]any)
		if tbl == "" || doc == nil {
			continue
		}

		cols := make([]string, 0, len(doc))
		vals := make([]any, 0, len(doc))
		placeholders := make([]string, 0, len(doc))
		i := 1
		for k, v := range doc {
			cols = append(cols, fmt.Sprintf("%q", k))
			vals = append(vals, v)
			placeholders = append(placeholders, fmt.Sprintf("$%d", i))
			i++
		}
		q := fmt.Sprintf("INSERT INTO %q (%s) VALUES (%s) ON CONFLICT DO NOTHING",
			tbl, strings.Join(cols, ","), strings.Join(placeholders, ","))
		conn.Exec(ctx, q, vals...)
	}
	return scanner.Err()
}

func restoreMySQL(uri, ndjsonPath string) error {
	db, err := newMySQLDB(uri)
	if err != nil {
		return err
	}
	defer db.Close()

	f, err := os.Open(ndjsonPath)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 10*1024*1024), 10*1024*1024)
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			continue
		}
		tbl, _ := row["_table"].(string)
		doc, _ := row["row"].(map[string]any)
		if tbl == "" || doc == nil {
			continue
		}

		cols := make([]string, 0, len(doc))
		vals := make([]any, 0, len(doc))
		placeholders := make([]string, 0, len(doc))
		for k, v := range doc {
			cols = append(cols, fmt.Sprintf("`%s`", k))
			vals = append(vals, v)
			placeholders = append(placeholders, "?")
		}
		q := fmt.Sprintf("INSERT IGNORE INTO `%s` (%s) VALUES (%s)",
			tbl, strings.Join(cols, ","), strings.Join(placeholders, ","))
		db.Exec(q, vals...)
	}
	return scanner.Err()
}
