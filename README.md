# DataVault — Go Edition

A self-hosted database backup manager with a web UI. Supports PostgreSQL, MySQL, MongoDB, and Redis as backup sources, and S3-compatible storage, Google Cloud Storage, and SFTP/SMB as destinations.

## Features

- Backup and restore for PostgreSQL, MySQL, MongoDB, Redis
- Storage destinations: S3 / S3-compatible (MinIO, Backblaze B2, etc.), Google Cloud Storage, SFTP, SMB/CIFS
- Cron-scheduled backup jobs
- File explorer with download support
- Retention policies
- Webhook / notification hooks
- Role-based access: Super Admin, Admin, Normal
- Config export / import (JSON bundle)
- Single self-contained binary — no external database required (JSON file store)

---

## Prerequisites

### Without Docker

| Requirement | Version |
|-------------|---------|
| Go | 1.22 or later |
| Git | any |

### With Docker

| Requirement | Notes |
|-------------|-------|
| Docker | 20.10+ |
| Docker Compose | v2 (`docker compose`) or v1 (`docker-compose`) |

---

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8000` | HTTP listen port |
| `DB_PATH` | `./db.json` | Path to the JSON data file |
| `JWT_SECRET` | _(auto-generated)_ | Secret used to sign session tokens. Set explicitly in production so sessions survive restarts. |
| `ADMIN_USERNAME` | — | **First-run only.** Creates a `super_admin` account with this username when no users exist. |
| `ADMIN_PASSWORD` | — | **First-run only.** Password for the account created by `ADMIN_USERNAME`. |
| `RESET_ADMIN_PASSWORD` | — | **Emergency use.** Resets the first `super_admin`'s password on every startup until removed. Remove this variable immediately after use. |

---

## Deployment — Without Docker

### 1. Clone the repository

```bash
git clone <repo-url>
cd datavault-go
```

### 2. Download dependencies

```bash
go mod download
```

### 3. Build the binary

```bash
go build -o datavault .
```

This produces a single binary `datavault` in the current directory.

### 4. Create a data directory

```bash
mkdir -p /opt/datavault/data
```

### 5. Copy static assets next to the binary

The binary serves templates and static files from paths relative to the working directory. Keep them together:

```bash
cp -r static/ templates/ /opt/datavault/
cp datavault /opt/datavault/
```

### 6. Run the server

```bash
cd /opt/datavault
DB_PATH=/opt/datavault/data/db.json \
PORT=8000 \
JWT_SECRET=changeme_use_a_long_random_string \
ADMIN_USERNAME=admin \
ADMIN_PASSWORD=yourpassword \
./datavault
```

The server starts on `http://localhost:8000`.

> `ADMIN_USERNAME` / `ADMIN_PASSWORD` only create the account on first run (when the database is empty). Remove or ignore them on subsequent runs — they are safely skipped if users already exist.

### 7. (Optional) Run as a systemd service

Create `/etc/systemd/system/datavault.service`:

```ini
[Unit]
Description=DataVault Backup Manager
After=network.target

[Service]
WorkingDirectory=/opt/datavault
ExecStart=/opt/datavault/datavault
Restart=always
RestartSec=5
Environment=PORT=8000
Environment=DB_PATH=/opt/datavault/data/db.json
Environment=JWT_SECRET=changeme_use_a_long_random_string

[Install]
WantedBy=multi-user.target
```

Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable datavault
sudo systemctl start datavault
sudo systemctl status datavault
```

---

## Deployment — With Docker

### Option A: docker-compose (recommended)

#### 1. Clone the repository

```bash
git clone <repo-url>
cd datavault-go
```

#### 2. (Optional) Configure environment variables

Edit `docker-compose.yml` to set your values:

```yaml
services:
  datavault:
    build: .
    ports:
      - "8000:8000"
    volumes:
      - datavault_data:/data
    environment:
      - DB_PATH=/data/db.json
      - PORT=8000
      - JWT_SECRET=changeme_use_a_long_random_string
      - ADMIN_USERNAME=admin
      - ADMIN_PASSWORD=yourpassword
    restart: unless-stopped

volumes:
  datavault_data:
```

Or use an `.env` file and reference variables with `${VAR}` in the compose file.

#### 3. Build the Docker image and start

```bash
docker-compose up -d --build
```

#### 4. Check logs

```bash
docker-compose logs -f
```

The server is available at `http://localhost:8000`.

#### 5. Stop / restart

```bash
docker-compose down       # stop and remove containers
docker-compose restart    # restart without rebuilding
```

#### Data persistence

All data is stored in the named Docker volume `datavault_data` (mounted at `/data` inside the container). The volume persists across container restarts and rebuilds.

To back up the volume data:

```bash
docker run --rm \
  -v datavault-go_datavault_data:/data \
  -v $(pwd):/backup \
  alpine tar czf /backup/datavault-backup.tar.gz /data
```

---

### Option B: docker run (no compose)

```bash
# Build the image
docker build -t datavault .

# Create a volume
docker volume create datavault_data

# Run
docker run -d \
  --name datavault \
  -p 8000:8000 \
  -v datavault_data:/data \
  -e DB_PATH=/data/db.json \
  -e PORT=8000 \
  -e JWT_SECRET=changeme_use_a_long_random_string \
  -e ADMIN_USERNAME=admin \
  -e ADMIN_PASSWORD=yourpassword \
  --restart unless-stopped \
  datavault
```

---

## Deployment — Kubernetes

The `k8s/` directory contains production-ready manifests. DataVault uses a JSON file store (`db.json`), so it runs as a **StatefulSet with a single replica** backed by a PersistentVolumeClaim — this guarantees data survives pod restarts, rescheduling, and node replacements.

### Prerequisites

| Requirement | Notes |
|-------------|-------|
| kubectl | configured against your cluster |
| Ingress controller | nginx recommended |
| cert-manager | optional, for automatic TLS via Let's Encrypt |
| Container registry | to host the DataVault image |

### 1. Build and push the image

```bash
docker build -t ad204476/datavault:latest .
docker push ad204476/datavault:latest
```

### 2. Update the image reference

Edit `k8s/deployment.yaml` and replace the placeholder:

```yaml
image: ad204476/datavault:latest   # ← set your actual image here
```

### 3. Update the domain

Edit `k8s/ingress.yaml` — the host is already set to `backup.mogiio.com`. Change it if needed.

### 4. Create a Secret for environment variables

```bash
kubectl create namespace datavault

kubectl create secret generic datavault-env \
  --namespace datavault \
  --from-literal=JWT_SECRET=changeme_use_a_long_random_string \
  --from-literal=ADMIN_USERNAME=admin \
  --from-literal=ADMIN_PASSWORD=yourpassword
```

Then reference it in `k8s/deployment.yaml` under `envFrom`:

```yaml
envFrom:
  - secretRef:
      name: datavault-env
```

### 5. Apply the manifests

```bash
kubectl apply -f k8s/namespace.yaml
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/service.yaml
kubectl apply -f k8s/ingress.yaml
kubectl apply -f k8s/pdb.yaml
```

Or apply everything at once:

```bash
kubectl apply -f k8s/
```

### 6. Verify the deployment

```bash
# Check pod status
kubectl get pods -n datavault

# Check PVC is bound
kubectl get pvc -n datavault

# Check ingress has an address
kubectl get ingress -n datavault

# Stream logs
kubectl logs -n datavault -l app=datavault -f
```

### 7. Upgrading

```bash
docker build -t ad204476/datavault:latest .
docker push ad204476/datavault:latest

# Trigger a rolling restart
kubectl rollout restart statefulset/datavault -n datavault

# Watch rollout status
kubectl rollout status statefulset/datavault -n datavault
```

### Data persistence

The JSON data file lives in a PersistentVolumeClaim (`data-datavault-0`, 2 Gi) mounted at `/data`. The PVC is **not deleted** when the pod is replaced or the StatefulSet is updated — your data is safe across all restarts.

To back up the data from the running pod:

```bash
kubectl exec -n datavault datavault-0 -- cat /data/db.json > db-backup.json
```

To restore it:

```bash
kubectl cp db-backup.json datavault/datavault-0:/data/db.json
kubectl rollout restart statefulset/datavault -n datavault
```

### Kubernetes manifest overview

```
k8s/
├── namespace.yaml    — datavault namespace
├── deployment.yaml   — StatefulSet (1 replica) + PVC template (2 Gi)
├── service.yaml      — ClusterIP on port 80 → container 8000
├── ingress.yaml      — nginx ingress with TLS (cert-manager / letsencrypt-prod)
├── hpa.yaml          — HPA disabled (JSON store is single-writer; uncomment after DB migration)
└── pdb.yaml          — PodDisruptionBudget (maxUnavailable: 0, protects the single pod)
```

> **Scaling note:** Do not increase replicas without first migrating the data store from the JSON file to a shared database (PostgreSQL, MySQL, etc.). Multiple pods writing to the same `db.json` simultaneously will corrupt data.

---

## First Login

1. Open `http://localhost:8000` in your browser.
2. If `ADMIN_USERNAME` / `ADMIN_PASSWORD` were set, sign in with those credentials.
3. Otherwise, click **Sign up** to create the first account (automatically granted `super_admin` role).

---

## Upgrading

### Without Docker

```bash
git pull
go build -o datavault .
# copy binary and restart the service / process
sudo systemctl restart datavault
```

### With Docker (docker-compose)

```bash
git pull
docker-compose build --no-cache
docker-compose up -d
```

---

## User Roles

| Role | Permissions |
|------|-------------|
| `super_admin` | Full access including user management and settings |
| `admin` | Full CRUD on all resources except user management |
| `normal` | View all resources, create restores, download files from explorer |

---

## Configuration Export / Import

Navigate to **Settings** (super_admin only) to:

- **Export** — download a JSON bundle of all configuration (credentials, sources, destinations, jobs, etc.). Optionally include user accounts.
- **Import** — upload a previously exported bundle to restore configuration on a new instance.

---

## Emergency Access

If you are locked out of all accounts, set the following environment variable and restart the server:

```bash
RESET_ADMIN_PASSWORD=newpassword
```

This resets the first `super_admin` account's password on startup. **Remove the variable immediately after regaining access.**

---

## Project Structure

```
datavault-go/
├── main.go               # Entry point, router setup
├── handlers/             # HTTP handlers (auth, users, backups, etc.)
├── models/               # Data model structs
├── services/             # Backup/restore logic and DB clients
├── store/                # JSON file-based data store
├── templates/            # Go HTML templates
├── static/               # CSS, favicon
├── k8s/                  # Kubernetes manifests
│   ├── namespace.yaml
│   ├── deployment.yaml   # StatefulSet + PVC
│   ├── service.yaml
│   ├── ingress.yaml
│   ├── hpa.yaml
│   └── pdb.yaml
├── Dockerfile
└── docker-compose.yml
```

---

## License

MIT
