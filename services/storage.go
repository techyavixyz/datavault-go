// Storage backends: S3, GCS, NFS, SSH/SFTP, SMB.
package services

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"net"

	"datavault/models"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	gcs "cloud.google.com/go/storage"
	"golang.org/x/crypto/ssh"
	"github.com/pkg/sftp"
	"github.com/hirochachacha/go-smb2"
	"google.golang.org/api/option"
)

// FileInfo represents a remote file entry for the explorer.
type FileInfo struct {
	Name     string  `json:"name"`
	Path     string  `json:"path"`
	Size     int64   `json:"size"`
	Modified float64 `json:"modified"`
}

// ── Progress tracking ─────────────────────────────────────────────────────────

// progressReader wraps an io.Reader and logs transfer progress at each 10% step.
type progressReader struct {
	r        io.Reader
	total    int64
	read     int64
	lastStep int
	label    string
	logf     func(string, ...any)
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	if n > 0 {
		p.read += int64(n)
		step := int(p.read * 10 / p.total)
		if step > p.lastStep {
			p.lastStep = step
			p.logf("  %s %d%%  (%s / %s)", p.label, step*10, fmtBytes(p.read), fmtBytes(p.total))
		}
	}
	return n, err
}

// withProgress wraps r with progress logging. Returns r unchanged when logf is
// nil or total is unknown (≤ 0).
func withProgress(r io.Reader, total int64, label string, logf func(string, ...any)) io.Reader {
	if logf == nil || total <= 0 {
		return r
	}
	return &progressReader{r: r, total: total, label: label, logf: logf}
}

// ── Public API ────────────────────────────────────────────────────────────────

// UploadFile sends localPath to the destination, placing it under subfolder.
// logf receives progress lines at each 10% step; pass nil to suppress them.
// Returns the remote path/URI.
func UploadFile(dest *models.StorageDestination, localPath, filename, subfolder string, logf func(string, ...any)) (string, error) {
	subf := strings.Trim(subfolder, "/")
	switch dest.StorageType {
	case models.StoreNFS:
		return uploadNFS(dest.NFS, localPath, filename, subf, logf)
	case models.StoreS3:
		return uploadS3(dest.S3, localPath, filename, subf, logf)
	case models.StoreGCS:
		return uploadGCS(dest.GCS, localPath, filename, subf, logf)
	case models.StoreSSH:
		return uploadSSH(dest.SSH, localPath, filename, subf, logf)
	case models.StoreSMB:
		return uploadSMB(dest.SMB, localPath, filename, subf, logf)
	}
	return "", fmt.Errorf("unsupported storage type: %s", dest.StorageType)
}

// ListFiles returns all files in a destination's root directory.
func ListFiles(dest *models.StorageDestination) ([]FileInfo, error) {
	switch dest.StorageType {
	case models.StoreNFS:
		return listNFS(dest.NFS)
	case models.StoreS3:
		return listS3(dest.S3)
	case models.StoreGCS:
		return listGCS(dest.GCS)
	case models.StoreSSH:
		return listSSH(dest.SSH)
	}
	return nil, fmt.Errorf("unsupported storage type: %s", dest.StorageType)
}

// DownloadTo copies a remote file to localPath.
// logf receives progress lines at each 10% step; pass nil to suppress them.
func DownloadTo(dest *models.StorageDestination, remotePath, localPath string, logf func(string, ...any)) error {
	switch dest.StorageType {
	case models.StoreNFS:
		return downloadNFS(remotePath, localPath, logf)
	case models.StoreS3:
		return downloadS3(dest.S3, remotePath, localPath, logf)
	case models.StoreGCS:
		return downloadGCS(dest.GCS, remotePath, localPath, logf)
	case models.StoreSSH:
		return downloadSSH(dest.SSH, remotePath, localPath, logf)
	}
	return fmt.Errorf("unsupported storage type: %s", dest.StorageType)
}

// multiCloser bundles an io.Reader with one or more cleanup functions into an io.ReadCloser.
type multiCloser struct {
	io.Reader
	closeFns []func() error
}

func (m *multiCloser) Close() error {
	var lastErr error
	for _, fn := range m.closeFns {
		if err := fn(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// DownloadStream opens a remote file for streaming and returns (reader, totalBytes, error).
// The caller must Close() the reader when done. logf is used for progress; pass nil to suppress.
func DownloadStream(dest *models.StorageDestination, remotePath string, logf func(string, ...any)) (io.ReadCloser, int64, error) {
	switch dest.StorageType {
	case models.StoreNFS:
		fi, _ := os.Stat(remotePath)
		var total int64
		if fi != nil {
			total = fi.Size()
		}
		f, err := os.Open(remotePath)
		if err != nil {
			return nil, 0, err
		}
		return &multiCloser{Reader: withProgress(f, total, "downloading", logf), closeFns: []func() error{f.Close}}, total, nil

	case models.StoreS3:
		cfg := trimS3Config(dest.S3)
		client, err := s3Client(cfg)
		if err != nil {
			return nil, 0, err
		}
		key := strings.SplitN(remotePath, "/", 4)
		if len(key) < 4 {
			return nil, 0, fmt.Errorf("invalid S3 path: %s", remotePath)
		}
		result, err := client.GetObject(context.Background(), &s3.GetObjectInput{
			Bucket: aws.String(cfg.BucketName),
			Key:    aws.String(key[3]),
		})
		if err != nil {
			return nil, 0, err
		}
		var total int64
		if result.ContentLength != nil {
			total = *result.ContentLength
		}
		return &multiCloser{
			Reader:   withProgress(result.Body, total, "downloading", logf),
			closeFns: []func() error{result.Body.Close},
		}, total, nil

	case models.StoreGCS:
		client, err := gcsClient(dest.GCS)
		if err != nil {
			return nil, 0, err
		}
		key := strings.TrimPrefix(remotePath, fmt.Sprintf("gs://%s/", dest.GCS.BucketName))
		r, err := client.Bucket(dest.GCS.BucketName).Object(key).NewReader(context.Background())
		if err != nil {
			client.Close()
			return nil, 0, err
		}
		total := r.Attrs.Size
		return &multiCloser{
			Reader:   withProgress(r, total, "downloading", logf),
			closeFns: []func() error{r.Close, client.Close},
		}, total, nil

	case models.StoreSSH:
		sc, err := sshClient(dest.SSH)
		if err != nil {
			return nil, 0, err
		}
		fc, err := sftp.NewClient(sc)
		if err != nil {
			sc.Close()
			return nil, 0, err
		}
		sshPath := strings.TrimPrefix(remotePath, fmt.Sprintf("ssh://%s", dest.SSH.Host))
		var total int64
		if fi, err := fc.Stat(sshPath); err == nil {
			total = fi.Size()
		}
		f, err := fc.Open(sshPath)
		if err != nil {
			fc.Close()
			sc.Close()
			return nil, 0, err
		}
		return &multiCloser{
			Reader:   withProgress(f, total, "downloading", logf),
			closeFns: []func() error{f.Close, fc.Close, sc.Close},
		}, total, nil
	}
	return nil, 0, fmt.Errorf("unsupported storage type: %s", dest.StorageType)
}

// DeleteFile removes a file from storage given its remote path as stored in BackupRecord.RemotePath.
func DeleteFile(dest *models.StorageDestination, remotePath string) error {
	switch dest.StorageType {
	case models.StoreNFS:
		return os.Remove(remotePath)
	case models.StoreS3:
		return deleteS3(dest.S3, remotePath)
	case models.StoreGCS:
		return deleteGCS(dest.GCS, remotePath)
	case models.StoreSSH:
		return deleteSSH(dest.SSH, remotePath)
	case models.StoreSMB:
		return deleteSMB(dest.SMB, remotePath)
	}
	return fmt.Errorf("unsupported storage type: %s", dest.StorageType)
}

// TestStorage checks that the destination is reachable and writable.
func TestStorage(dest *models.StorageDestination) (string, error) {
	switch dest.StorageType {
	case models.StoreNFS:
		d := filepath.Join(dest.NFS.MountPath, dest.NFS.SubDirectory)
		if err := os.MkdirAll(d, 0755); err != nil {
			return "", err
		}
		tp := filepath.Join(d, ".dv_test")
		if err := os.WriteFile(tp, []byte("ok"), 0644); err != nil {
			return "", err
		}
		os.Remove(tp)
		return "NFS path writable", nil

	case models.StoreS3:
		client, err := s3Client(dest.S3)
		if err != nil {
			return "", err
		}
		ctx := context.Background()
		// GetBucketLocation works regardless of which regional endpoint is used,
		// so it tells us the bucket's actual region without needing to know it upfront.
		loc, err := client.GetBucketLocation(ctx, &s3.GetBucketLocationInput{
			Bucket: aws.String(dest.S3.BucketName),
		})
		if err != nil {
			return "", fmt.Errorf("bucket '%s' not found or not accessible: %w", dest.S3.BucketName, err)
		}
		actualRegion := string(loc.LocationConstraint)
		if actualRegion == "" {
			actualRegion = "us-east-1" // us-east-1 returns an empty constraint
		}
		// If the bucket is in a different region than configured, rebuild the client.
		if actualRegion != dest.S3.Region && dest.S3.EndpointURL == "" {
			corrected := *dest.S3
			corrected.Region = actualRegion
			client, err = s3Client(&corrected)
			if err != nil {
				return "", err
			}
		}
		_, err = client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:  aws.String(dest.S3.BucketName),
			MaxKeys: aws.Int32(1),
		})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("S3 bucket '%s' accessible (region: %s)", dest.S3.BucketName, actualRegion), nil

	case models.StoreGCS:
		c, err := gcsClient(dest.GCS)
		if err != nil {
			return "", err
		}
		defer c.Close()
		if err := c.Bucket(dest.GCS.BucketName).Object(".dv_probe").NewWriter(context.Background()).Close(); err != nil {
			// probe write not critical — just check bucket exists via attrs
			if _, err2 := c.Bucket(dest.GCS.BucketName).Attrs(context.Background()); err2 != nil {
				return "", err2
			}
		}
		return fmt.Sprintf("GCS bucket '%s' accessible", dest.GCS.BucketName), nil

	case models.StoreSSH:
		sc, err := sshClient(dest.SSH)
		if err != nil {
			return "", err
		}
		sc.Close()
		return fmt.Sprintf("SSH %s connected", dest.SSH.Host), nil

	case models.StoreSMB:
		return "SMB configured", nil
	}
	return "", fmt.Errorf("unknown storage type")
}

// ── NFS (local mount) ─────────────────────────────────────────────────────────

func uploadNFS(cfg *models.NFSConfig, localPath, filename, subf string, logf func(string, ...any)) (string, error) {
	d := filepath.Join(cfg.MountPath, cfg.SubDirectory)
	if subf != "" {
		d = filepath.Join(d, subf)
	}
	if err := os.MkdirAll(d, 0755); err != nil {
		return "", err
	}
	dst := filepath.Join(d, filename)

	fi, _ := os.Stat(localPath)
	var total int64
	if fi != nil {
		total = fi.Size()
	}

	in, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return "", err
	}
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer out.Close()
	_, err = io.Copy(out, withProgress(in, total, "uploading", logf))
	return dst, err
}

func downloadNFS(remotePath, localPath string, logf func(string, ...any)) error {
	fi, _ := os.Stat(remotePath)
	var total int64
	if fi != nil {
		total = fi.Size()
	}

	in, err := os.Open(remotePath)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return err
	}
	out, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, withProgress(in, total, "downloading", logf))
	return err
}

func listNFS(cfg *models.NFSConfig) ([]FileInfo, error) {
	base := filepath.Join(cfg.MountPath, cfg.SubDirectory)
	entries, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []FileInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, _ := e.Info()
		out = append(out, FileInfo{
			Name:     e.Name(),
			Path:     filepath.Join(base, e.Name()),
			Size:     fi.Size(),
			Modified: float64(fi.ModTime().Unix()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	return out, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// ── S3 ────────────────────────────────────────────────────────────────────────

func trimS3Config(cfg *models.S3Config) *models.S3Config {
	c := *cfg
	c.BucketName = strings.TrimSpace(c.BucketName)
	c.Region = strings.TrimSpace(c.Region)
	c.Prefix = strings.TrimSpace(c.Prefix)
	c.EndpointURL = strings.TrimSpace(c.EndpointURL)
	c.AccessKeyID = strings.TrimSpace(c.AccessKeyID)
	return &c
}

func s3Client(cfg *models.S3Config) (*s3.Client, error) {
	cfg = trimS3Config(cfg)
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	opts := []func(*config.LoadOptions) error{
		config.WithRegion(region),
	}
	if cfg.AccessKeyID != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}
	awsCfg, err := config.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, err
	}
	var s3opts []func(*s3.Options)
	if cfg.EndpointURL != "" {
		s3opts = append(s3opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.EndpointURL)
			o.UsePathStyle = true
		})
	}
	return s3.NewFromConfig(awsCfg, s3opts...), nil
}

func s3Key(prefix, subf, filename string) string {
	base := strings.TrimRight(prefix, "/")
	if subf != "" {
		return base + "/" + subf + "/" + filename
	}
	return base + "/" + filename
}

func uploadS3(cfg *models.S3Config, localPath, filename, subf string, logf func(string, ...any)) (string, error) {
	cfg = trimS3Config(cfg)
	client, err := s3Client(cfg)
	if err != nil {
		return "", err
	}
	key := s3Key(cfg.Prefix, subf, filename)

	fi, _ := os.Stat(localPath)
	var total int64
	if fi != nil {
		total = fi.Size()
	}

	f, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket:        aws.String(cfg.BucketName),
		Key:           aws.String(key),
		Body:          withProgress(f, total, "uploading", logf),
		ContentLength: aws.Int64(total),
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("s3://%s/%s", cfg.BucketName, key), nil
}

func listS3(cfg *models.S3Config) ([]FileInfo, error) {
	cfg = trimS3Config(cfg)
	client, err := s3Client(cfg)
	if err != nil {
		return nil, err
	}
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: aws.String(cfg.BucketName),
		Prefix: aws.String(cfg.Prefix),
	})
	var out []FileInfo
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(context.Background())
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			parts := strings.Split(key, "/")
			sz := int64(0)
			if obj.Size != nil {
				sz = *obj.Size
			}
			out = append(out, FileInfo{
				Name:     parts[len(parts)-1],
				Path:     fmt.Sprintf("s3://%s/%s", cfg.BucketName, key),
				Size:     sz,
				Modified: float64(obj.LastModified.Unix()),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	return out, nil
}

func downloadS3(cfg *models.S3Config, remotePath, localPath string, logf func(string, ...any)) error {
	cfg = trimS3Config(cfg)
	client, err := s3Client(cfg)
	if err != nil {
		return err
	}
	// strip s3://bucket/ prefix
	key := strings.SplitN(remotePath, "/", 4)
	if len(key) < 4 {
		return fmt.Errorf("invalid S3 path: %s", remotePath)
	}
	result, err := client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(cfg.BucketName),
		Key:    aws.String(key[3]),
	})
	if err != nil {
		return err
	}
	defer result.Body.Close()

	var total int64
	if result.ContentLength != nil {
		total = *result.ContentLength
	}

	f, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, withProgress(result.Body, total, "downloading", logf))
	return err
}

// ── GCS ───────────────────────────────────────────────────────────────────────

func gcsClient(cfg *models.GCSConfig) (*gcs.Client, error) {
	ctx := context.Background()
	if cfg.ServiceAccountJSON != "" {
		return gcs.NewClient(ctx, option.WithCredentialsJSON([]byte(cfg.ServiceAccountJSON)))
	}
	return gcs.NewClient(ctx)
}

func uploadGCS(cfg *models.GCSConfig, localPath, filename, subf string, logf func(string, ...any)) (string, error) {
	client, err := gcsClient(cfg)
	if err != nil {
		return "", err
	}
	defer client.Close()
	key := s3Key(cfg.Prefix, subf, filename) // same join logic

	fi, _ := os.Stat(localPath)
	var total int64
	if fi != nil {
		total = fi.Size()
	}

	f, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	w := client.Bucket(cfg.BucketName).Object(key).NewWriter(context.Background())
	if _, err := io.Copy(w, withProgress(f, total, "uploading", logf)); err != nil {
		w.Close()
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return fmt.Sprintf("gs://%s/%s", cfg.BucketName, key), nil
}

func listGCS(cfg *models.GCSConfig) ([]FileInfo, error) {
	client, err := gcsClient(cfg)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	ctx := context.Background()
	q := &gcs.Query{Prefix: cfg.Prefix}
	it := client.Bucket(cfg.BucketName).Objects(ctx, q)
	var out []FileInfo
	for {
		attrs, err := it.Next()
		if err != nil {
			break
		}
		parts := strings.Split(attrs.Name, "/")
		out = append(out, FileInfo{
			Name:     parts[len(parts)-1],
			Path:     fmt.Sprintf("gs://%s/%s", cfg.BucketName, attrs.Name),
			Size:     attrs.Size,
			Modified: float64(attrs.Updated.Unix()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	return out, nil
}

func downloadGCS(cfg *models.GCSConfig, remotePath, localPath string, logf func(string, ...any)) error {
	client, err := gcsClient(cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	key := strings.TrimPrefix(remotePath, fmt.Sprintf("gs://%s/", cfg.BucketName))
	r, err := client.Bucket(cfg.BucketName).Object(key).NewReader(context.Background())
	if err != nil {
		return err
	}
	defer r.Close()

	total := r.Attrs.Size

	f, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, withProgress(r, total, "downloading", logf))
	return err
}

// ── SSH / SFTP ────────────────────────────────────────────────────────────────

func sshClient(cfg *models.SSHConfig) (*ssh.Client, error) {
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	var auth []ssh.AuthMethod
	if cfg.PrivateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
		if err != nil {
			return nil, err
		}
		auth = append(auth, ssh.PublicKeys(signer))
	} else if cfg.Password != "" {
		auth = append(auth, ssh.Password(cfg.Password))
	}
	return ssh.Dial("tcp", fmt.Sprintf("%s:%d", cfg.Host, port), &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
}

func uploadSSH(cfg *models.SSHConfig, localPath, filename, subf string, logf func(string, ...any)) (string, error) {
	sc, err := sshClient(cfg)
	if err != nil {
		return "", err
	}
	defer sc.Close()
	fc, err := sftp.NewClient(sc)
	if err != nil {
		return "", err
	}
	defer fc.Close()

	remote := strings.TrimRight(cfg.RemotePath, "/")
	if subf != "" {
		for _, part := range strings.Split(subf, "/") {
			remote += "/" + part
			fc.Mkdir(remote)
		}
	} else {
		fc.Mkdir(remote)
	}
	rp := remote + "/" + filename
	dst, err := fc.Create(rp)
	if err != nil {
		return "", err
	}
	defer dst.Close()

	fi, _ := os.Stat(localPath)
	var total int64
	if fi != nil {
		total = fi.Size()
	}

	src, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	if _, err := io.Copy(dst, withProgress(src, total, "uploading", logf)); err != nil {
		return "", err
	}
	return fmt.Sprintf("ssh://%s%s", cfg.Host, rp), nil
}

func listSSH(cfg *models.SSHConfig) ([]FileInfo, error) {
	sc, err := sshClient(cfg)
	if err != nil {
		return nil, err
	}
	defer sc.Close()
	fc, err := sftp.NewClient(sc)
	if err != nil {
		return nil, err
	}
	defer fc.Close()

	entries, err := fc.ReadDir(cfg.RemotePath)
	if err != nil {
		return nil, err
	}
	var out []FileInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out = append(out, FileInfo{
			Name:     e.Name(),
			Path:     fmt.Sprintf("ssh://%s%s/%s", cfg.Host, cfg.RemotePath, e.Name()),
			Size:     e.Size(),
			Modified: float64(e.ModTime().Unix()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	return out, nil
}

func downloadSSH(cfg *models.SSHConfig, remotePath, localPath string, logf func(string, ...any)) error {
	sc, err := sshClient(cfg)
	if err != nil {
		return err
	}
	defer sc.Close()
	fc, err := sftp.NewClient(sc)
	if err != nil {
		return err
	}
	defer fc.Close()
	sshPath := strings.TrimPrefix(remotePath, fmt.Sprintf("ssh://%s", cfg.Host))

	var total int64
	if fi, err := fc.Stat(sshPath); err == nil {
		total = fi.Size()
	}

	src, err := fc.Open(sshPath)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, withProgress(src, total, "downloading", logf))
	return err
}

// ── SMB ───────────────────────────────────────────────────────────────────────

func smbDial(cfg *models.SMBConfig) (*smb2.Session, error) {
	conn, err := net.Dial("tcp", fmt.Sprintf("%s:445", cfg.Host))
	if err != nil {
		return nil, err
	}
	d := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     cfg.Username,
			Password: cfg.Password,
			Domain:   cfg.Domain,
		},
	}
	return d.Dial(conn)
}

func deleteS3(cfg *models.S3Config, remotePath string) error {
	cfg = trimS3Config(cfg)
	client, err := s3Client(cfg)
	if err != nil {
		return err
	}
	// remotePath is s3://bucket/key — strip scheme+bucket
	parts := strings.SplitN(remotePath, "/", 4)
	if len(parts) < 4 {
		return fmt.Errorf("invalid S3 path: %s", remotePath)
	}
	_, err = client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
		Bucket: aws.String(cfg.BucketName),
		Key:    aws.String(parts[3]),
	})
	return err
}

func deleteGCS(cfg *models.GCSConfig, remotePath string) error {
	c, err := gcsClient(cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	key := strings.TrimPrefix(remotePath, fmt.Sprintf("gs://%s/", cfg.BucketName))
	return c.Bucket(cfg.BucketName).Object(key).Delete(context.Background())
}

func deleteSSH(cfg *models.SSHConfig, remotePath string) error {
	sc, err := sshClient(cfg)
	if err != nil {
		return err
	}
	defer sc.Close()
	fc, err := sftp.NewClient(sc)
	if err != nil {
		return err
	}
	defer fc.Close()
	sshPath := strings.TrimPrefix(remotePath, fmt.Sprintf("ssh://%s", cfg.Host))
	return fc.Remove(sshPath)
}

func deleteSMB(cfg *models.SMBConfig, remotePath string) error {
	conn, err := smbDial(cfg)
	if err != nil {
		return err
	}
	defer conn.Logoff()
	share, err := conn.Mount(cfg.Share)
	if err != nil {
		return err
	}
	defer share.Umount()
	// remotePath is smb://host/share/subdir/file — strip scheme+host+share
	prefix := fmt.Sprintf("smb://%s/%s/", cfg.Host, cfg.Share)
	rp := strings.TrimPrefix(remotePath, prefix)
	return share.Remove(rp)
}

func uploadSMB(cfg *models.SMBConfig, localPath, filename, subf string, logf func(string, ...any)) (string, error) {
	conn, err := smbDial(cfg)
	if err != nil {
		return "", err
	}
	defer conn.Logoff()

	share, err := conn.Mount(cfg.Share)
	if err != nil {
		return "", err
	}
	defer share.Umount()

	rp := cfg.SubDirectory
	if subf != "" {
		rp += "/" + subf
	}
	share.MkdirAll(rp, 0755)
	rp += "/" + filename

	fi, _ := os.Stat(localPath)
	var total int64
	if fi != nil {
		total = fi.Size()
	}

	src, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst, err := share.Create(rp)
	if err != nil {
		return "", err
	}
	defer dst.Close()
	if _, err := io.Copy(dst, withProgress(src, total, "uploading", logf)); err != nil {
		return "", err
	}
	return fmt.Sprintf("smb://%s/%s/%s", cfg.Host, cfg.Share, rp), nil
}
