# Redis Sentinel — Standalone Deployment

Deploys a Redis HA cluster with **1 master, 1 replica, and 3 sentinel nodes** on an isolated Docker network. No dependency on any other Compose project. DataVault (or any other client) connects via the host machine's IP and exposed ports.

---

## Architecture

```
                        ┌─────────────────────────────────────┐
                        │         sentinel-net (bridge)       │
                        │                                     │
                        │  redis-master :6379 ←── writes      │
                        │       │                             │
                        │  redis-replica :6379 ←── reads      │
                        │                                     │
                        │  redis-sentinel-1 :26379            │
                        │  redis-sentinel-2 :26379            │
                        │  redis-sentinel-3 :26379            │
                        └─────────┬───────────────────────────┘
                                  │ port-mapped to host
                    ──────────────┴──────────────────────────
                    HOST_IP:6379        ← Redis master
                    HOST_IP:26379-26381 ← Sentinel nodes
                    ──────────────────────────────────────────
                              ↑
                         DataVault / any client
```

---

## Prerequisites

| Requirement | Notes |
|---|---|
| Docker Engine 20.10+ | `docker --version` |
| Docker Compose v2 or `docker-compose` v1 | `docker-compose --version` |
| Ports 6379, 26379, 26380, 26381 free on host | Check with `ss -tlnp` |
| `redis-cli` on host (optional) | For manual verification only |

---

## Step 1 — Find your HOST_IP

`HOST_IP` must be the **LAN or public IP** of the machine where you run this stack. It must be reachable from wherever DataVault is running.

```bash
# Linux — pick the IP of your primary interface
hostname -I | awk '{print $1}'

# Or find it explicitly
ip addr show | grep "inet " | grep -v 127.0.0.1
```

> **Do not use `127.0.0.1` or `localhost`.**
> Sentinels advertise this IP back to clients. If you use loopback, DataVault
> will try to connect to itself instead of the Redis master.

---

## Step 2 — Start the stack

```bash
cd redis-sentinel

HOST_IP=<your-ip> docker-compose up -d
```

Example:

```bash
HOST_IP=192.168.1.100 docker-compose up -d
```

Expected output:

```
Network redis-sentinel_sentinel-net  Created
Container redis-master               Started
Container redis-replica              Started
Container redis-sentinel-1           Started
Container redis-sentinel-2           Started
Container redis-sentinel-3           Started
```

---

## Step 3 — Verify the cluster

**Check all containers are running:**

```bash
docker-compose ps
```

Expected: all 5 containers `Up`, redis-master shows `(healthy)`.

**Verify sentinels report the correct master:**

```bash
redis-cli -p 26379 SENTINEL get-master-addr-by-name mymaster
redis-cli -p 26380 SENTINEL get-master-addr-by-name mymaster
redis-cli -p 26381 SENTINEL get-master-addr-by-name mymaster
```

Each should return your `HOST_IP` and port `6379` — **not** an internal Docker IP (172.x.x.x).

**Check replication is working:**

```bash
redis-cli -p 6379 INFO replication
```

Look for:
```
role:master
connected_slaves:1
slave0:ip=...,state=online,offset=...,lag=0
```

---

## Step 4 — Configure DataVault source

In DataVault → **Database Sources** → **New Source**:

| Field | Value |
|---|---|
| Database Type | `redis sentinel` |
| Connection Mode | `Direct URI` |
| Connection URI | `redis-sentinel://<HOST_IP>:26379,<HOST_IP>:26380,<HOST_IP>:26381/mymaster` |

**With Redis password** (if `REDIS_PASSWORD` is set):

```
redis-sentinel://<REDIS_PASSWORD>@<HOST_IP>:26379,<HOST_IP>:26380,<HOST_IP>:26381/mymaster
```

Click **Test Connection** — it should return `Connected`.

---

## Step 5 — Choose backup mode

When creating a backup or cron job in DataVault, select the **Redis Backup Format**:

| Mode | When to use |
|---|---|
| **NDJSON** | Default. Exports key-by-key. Works with all Redis. Respects target DB filter. |
| **RDB Snapshot** | Full binary dump of all databases. Faster for large datasets. Requires direct TCP access to master — works with this self-hosted setup. |

Both modes are supported with Redis Sentinel.

---

## Optional — Add Redis password

Edit `docker-compose.yml` and update the `redis-master` and `redis-replica` commands:

```yaml
redis-master:
  command: >
    redis-server
    --bind 0.0.0.0
    --protected-mode no
    --requirepass yourpassword

redis-replica:
  command: >
    redis-server
    --bind 0.0.0.0
    --protected-mode no
    --replicaof redis-master 6379
    --masterauth yourpassword
    --requirepass yourpassword
```

Also add to each sentinel config:

```
echo "sentinel auth-pass mymaster yourpassword"  >> /tmp/sentinel.conf
```

And update the DataVault URI:

```
redis-sentinel://yourpassword@<HOST_IP>:26379,<HOST_IP>:26380,<HOST_IP>:26381/mymaster
```

---

## Stopping and cleanup

```bash
# Stop containers (data preserved in volumes)
docker-compose down

# Stop and remove all data
docker-compose down -v
```

---

## Troubleshooting

**Sentinels return a Docker internal IP (172.x.x.x) instead of HOST_IP**

`HOST_IP` was not set or was set to loopback. Stop, then restart with the correct IP:

```bash
docker-compose down
HOST_IP=192.168.1.100 docker-compose up -d
```

**DataVault "Test Connection" fails with timeout**

- Confirm ports 6379 and 26379–26381 are reachable: `telnet <HOST_IP> 26379`
- Check firewall: `sudo ufw status` or `sudo iptables -L`
- Confirm HOST_IP is the correct interface IP

**`sentinel get-master-addr-by-name` returns nothing / nil**

Sentinel hasn't reached quorum yet. Wait 5–10 seconds after startup and retry. Requires at least 2 of 3 sentinels to agree on the master.

**RDB backup fails: "PSYNC returned empty response"**

DataVault is connecting to the sentinel port (26379) instead of the master port (6379). Check that the URI format is correct — the sentinel URI should point to sentinel addresses, DataVault resolves the master internally.


