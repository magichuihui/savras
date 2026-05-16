# Project Savras: Grafana Auth Proxy & Sync Sidecar

## 1. Project Overview
**Savras** is an authentication proxy (Auth Proxy) and organization synchronization tool designed for Grafana. It runs in Sidecar mode, handling Active Directory (AD) authentication, JWT issuance, dynamic Header injection, and periodic synchronization of AD Groups to Grafana Teams.

## 2. Technology Stack Constraints
*   **Language**: Go 1.24+
*   **Mode**: Kubernetes Sidecar (shares Network Namespace with Grafana)
*   **Core Libraries**:
    *   HTTP Proxy: `net/http/httputil`
    *   JWT: `[github.com/golang-jwt/jwt/v5](https://github.com/golang-jwt/jwt/v5)`
    *   LDAP: `[github.com/go-ldap/ldap/v3](https://github.com/go-ldap/ldap/v3)`
    *   Logging: `log/slog` (Structured JSON)
    *   Config: `[github.com/spf13/viper](https://github.com/spf13/viper)`

## 3. Core Features & Logic Requirements

### A. Authentication Middleware
1.  **JWT Validation**: Intercept all non-management requests, check for the `savras_auth` Cookie.
2.  **Redirect Flow**: If the Cookie is invalid/missing, redirect to `/savras/login`.
3.  **AD Verification**: Login page accepts username/password, verifies via LDAP Bind. Issues JWT upon success.
4.  **Header Injection**: Upon successful verification, inject `X-WEBAUTH-USER` and `X-WEBAUTH-EMAIL` headers in the reverse proxy request.

### B. Routing & Reverse Proxy
1.  **Management Routes**: Requests with prefix `/-/savras/` are handled by Savras itself (not forwarded).
    *   `GET /-/savras/health`: Health check (must verify LDAP and downstream connectivity).
    *   `POST /-/savras/sync/trigger`: Immediately trigger manual synchronization.
2.  **Business Routes**: All other requests are forwarded to `[http://127.0.0.1:3000](http://127.0.0.1:3000)` (Grafana).
3.  **RBAC Extension**: Reserve `RBACMiddleware` for paths like `/api/datasources`, currently defaulting to allow.

### C. AD to Grafana Sync Engine
1.  **Periodic Execution**: Runs every `X` minutes.
2.  **Logic Flow**:
    *   Fetch specified Groups and their members from AD.
    *   Call Grafana API to check if the corresponding Team exists; create it if not.
    *   Compare Team member differences, perform Add/Remove operations (requires Grafana Admin Token).
    *   Define folder permissions in the config file to assign access rights to specific teams; Savras overwrites folder permissions based on configuration.
3.  **Optimization Requirement**: Implement memory cache to reduce redundant Grafana API calls.

## 4. Directory Structure
```text
.
├── cmd/
│   └── savras/          # Entry point
├── internal/
│   ├── auth/            # JWT and LDAP logic
│   ├── proxy/           # Middleware and reverse proxy logic
│   ├── sync/            # AD to Grafana sync logic
│   ├── config/          # Configuration loading
│   └── grafana/         # Grafana API Client wrapper
├── pkg/                 # Exportable utility tools
└── Dockerfile           # Multi-stage build (Distroless/Alpine)
```

## 5. Task Breakdown (Task Sequences)

Unit tests are required for each step upon completion.

### Task 1: Initialize Project Environment
> **Prompt**: "Initialize the project structure with Go 1.24, establish the directory layout above. Configure JSON logging with `log/slog`, and use `viper` to load YAML configuration containing LDAP credentials, Grafana secrets, and sync intervals."

### Task 2: Implement LDAP Authentication & JWT Issuance
> **Prompt**: "Implement LDAP Bind logic and JWT issuance functionality in `internal/auth`. Must support loading RSA private key from environment variables."

### Task 3: Build Core Proxy & Middleware Chain
> **Prompt**: "Implement the core proxy using `httputil.ReverseProxy`. Write middleware: 1. Auth middleware (check JWT and redirect); 2. Header injection middleware (inject X-WEBAUTH fields). Ensure `/-/savras/` paths are not forwarded."

### Task 4: Develop Sync Worker
> **Prompt**: "Implement sync logic in `internal/sync`. Use a Ticker timer to sync AD Group members to Grafana Teams every X minutes. Implement incremental update logic to minimize API calls."
