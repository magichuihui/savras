# Savras - Grafana Auth Proxy & Sync Sidecar

![Built by OpenCode](https://img.shields.io/badge/Built%20by-OpenCode-blue?style=flat-square)
[![Codecov](https://codecov.io/gh/magichuihui/savras/branch/main/graph/badge.svg)](https://codecov.io/gh/magichuihui/savras)

Savras is a Grafana authentication proxy and organization sync tool that runs as a sidecar.

## Architecture

```mermaid
graph TB
    subgraph "External Services"
        USER([User Browser])
        GRAFANA([Grafana])
        LDAP([Active Directory])
    end

    subgraph "Savras Sidecar"
        direction TB
        MAIN[cmd/savras/main.go<br/>Entry & wiring]
        PROXY[internal/proxy<br/>HTTP handlers, middleware,<br/>reverse proxy, monitor]
        AUTH[internal/auth<br/>LDAP auth, JWT]
        SYNC[internal/sync<br/>AD→Grafana team sync]
        GCLIENT[internal/grafana<br/>Grafana API client]
        CONFIG[internal/config<br/>Config loader]

        MAIN --> PROXY & AUTH & SYNC & GCLIENT
        PROXY --> AUTH
        PROXY --> CONFIG
        SYNC --> GCLIENT
        SYNC --> CONFIG
        AUTH --> CONFIG
    end

    USER -- HTTPS :8080 --> PROXY
    PROXY -- header injection + reverse proxy --> GRAFANA
    AUTH -- LDAP bind/search --> LDAP
    SYNC -- HTTP API --> GRAFANA
```

### Request Flow

```mermaid
sequenceDiagram
    participant B as Browser
    participant S as Savras
    participant G as Grafana
    participant A as AD/LDAP

    B->>S: GET / (any protected path)
    S->>S: BlockWhenDown check
    alt Grafana is Down
        S-->>B: 503 Service Unavailable
    end
    S->>S: AuthMiddleware validate JWT
    alt No cookie or invalid
        S-->>B: 302 Redirect to /login
        B->>S: GET /login
        S-->>B: Login form HTML
        B->>S: POST /login (username, password)
        S->>A: LDAP bind + search
        A-->>S: user info or error
        alt Login success
            S->>S: Generate JWT, Set-Cookie
            S-->>B: 302 Redirect to /
        else Login failed
            S-->>B: Login form + error
        end
    end
    S->>S: HeaderInjection (X-WEBAUTH-USER/EMAIL)
    S->>G: reverse proxy
    G-->>B: Grafana page
```

### Middleware Chain

```mermaid
graph LR
    REQ[Incoming Request] --> BLOCK[BlockWhenDown<br/>503 if Grafana down]
    BLOCK --> MUX{Path?}

    MUX -->|health| H[healthHandler]
    MUX -->|sync trigger| S1[syncTriggerHandler]
    MUX -->|login| L[loginHandler]
    MUX -->|logout| LO[logoutHandler]

    MUX -->|everything else| AUTH_M[AuthMiddleware<br/>validate JWT]
    AUTH_M -->|no cookie| LOGIN_R[302 to /login]
    AUTH_M -->|invalid token| CLEAR[clear cookie, 302 to /login]
    AUTH_M -->|valid| HEADER[HeaderInjection<br/>X-WEBAUTH headers]
    HEADER --> RBAC[RBACMiddleware<br/>placeholder]
    RBAC --> RP[Reverse Proxy to Grafana]
```

### Grafana Lifecycle Monitor

```mermaid
stateDiagram-v2
    [*] --> Up
    Up --> Down: proxy error or health probe fail
    Down --> Up: exponential backoff probe succeeds

    state Up {
        [*] --> Serving
        Serving --> ProbeBG: every 30s GET /api/health
        ProbeBG --> Serving: 2xx
        ProbeBG --> Down: error
    }

    state Down {
        [*] --> Blocked
        Blocked --> Probing: backoff 1s->10s + jitter
        Probing --> Recovered: /api/health 2xx
        Probing --> Probing: retry
        Recovered --> Up: callback: sync + invalidate tokens
    }
```

### Sync Triggers

```mermaid
graph LR
    subgraph Triggers
        T1[SyncWorker timer<br/>every N minutes]
        T2[Grafana recovery<br/>monitor.onRecovery]
        T3[Manual POST<br/>/-/savras/sync/trigger]
        T4[Post-login<br/>goroutine]
    end

    subgraph Queue
        Q[SyncQueue<br/>chan size 1<br/>coalesces rapid triggers]
    end

    subgraph Execution
        SW[syncOnce<br/>mutex-serialized]
    end

    T1 --> SW
    T2 --> SW
    T3 --> Q --> SW
    T4 --> Q --> SW

    SW --> A[LDAP: search groups]
    SW --> B[Grafana API:<br/>lookup/create teams]
    SW --> C[Grafana API:<br/>sync team members]
    SW --> D[Grafana API:<br/>sync folder permissions]
```

## Features

- Active Directory LDAP authentication
- JWT token issuance and validation (RSA or HMAC)
- Dynamic header injection (X-WEBAUTH-USER, X-WEBAUTH-EMAIL)
- Reverse proxy to Grafana with auth middleware
- Auto-detection of Grafana restart → invalidate all tokens, block traffic until sync completes
- Periodic AD group to Grafana team synchronization
- Folder permission assignment based on team mappings
- Health check endpoint (`/-/savras/health`)
- Manual sync trigger endpoint (`POST /-/savras/sync/trigger`)

## Quick Start

```bash
# 1. Configure
cp config.example.yaml config.yaml
# edit config.yaml with your LDAP/Grafana settings

# 2. Run directly
make run

# 3. Login at http://localhost:8080
# Grafana must be configured to use auth proxy with headers
```

## Building

```bash
# Build for current platform
make build

# Cross-compile for Linux (amd64 + arm64)
make build-all

# Or use Go directly
go build -o savras ./cmd/savras
```

## Configuration

See config.example.yaml for all options. Key settings:
- LDAP server connection
- Grafana API credentials
- JWT secret for token signing
- Sync interval and group mappings
- Folder permissions: assign folder access to teams with specific permission levels

Sensitive fields (passwords, tokens, keys) can be overridden via environment variables,
which is useful when deploying with Kubernetes Secrets:

```bash
export SAVRAS_LDAP_BIND_PASSWORD=secret
export SAVRAS_GRAFANA_ADMIN_PASSWORD=admin
export SAVRAS_AUTH_JWT_SECRET=my-jwt-secret
make run
```

| Environment Variable | Overrides |
|---|---|
| `SAVRAS_LDAP_BIND_PASSWORD` | `ldap.bind_password` |
| `SAVRAS_GRAFANA_ADMIN_PASSWORD` | `grafana.admin_password` |
| `SAVRAS_GRAFANA_API_TOKEN` | `grafana.api_token` |
| `SAVRAS_AUTH_JWT_SECRET` | `auth.jwt_secret` |
| `SAVRAS_AUTH_JWT_PRIVATE_KEY` | `auth.jwt_private_key` |
| `SAVRAS_AUTH_LOCAL_ADMIN_PASSWORD` | `auth.local_admin_password` |

Env vars take precedence over values in config.yaml.

## Endpoints

- `/-/savras/health` - Health check
- `POST /-/savras/sync/trigger` - Manual sync trigger
- All other routes proxied to Grafana with auth

## Docker

### Build Locally

```bash
make docker-build
docker run -v $(pwd)/config.yaml:/etc/savras/config.yaml -p 8080:8080 savras
```

### Multi-arch Build

```bash
make docker-buildx
```

### GitHub Container Registry

Published automatically on every release (`v*` tag push):

```bash
docker pull ghcr.io/magichuihui/savras:v0.0.1
```

Images are multi-arch (linux/amd64, linux/arm64) and tagged with the full version
and major.minor (e.g. `v0.0.1`, `v0.0`).

## Development

```bash
make fmt      # format code
make vet      # static analysis
make test     # run all tests with race detector
make test-cover  # coverage report
make lint     # staticcheck
make tidy     # tidy go modules
make clean    # remove build artifacts
```

## Release

Releases are automated via GitHub Actions. Push a tag to trigger:

```bash
git tag v0.1.0
git push origin v0.1.0
```

This builds binaries for all platforms (linux/darwin/windows, amd64/arm64),
uploads them to the GitHub Release, and publishes a multi-arch Docker image
to `ghcr.io/magichuihui/savras`.
