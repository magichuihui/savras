# Savras - Grafana Auth Proxy & Sync Sidecar

![Built by OpenCode](https://img.shields.io/badge/Built%20by-OpenCode-blue?style=flat-square)
[![Codecov](https://codecov.io/gh/magichuihui/savras/branch/main/graph/badge.svg)](https://codecov.io/gh/magichuihui/savras)

Savras is a Grafana authentication proxy and organization sync tool that runs as a sidecar.

## Features

- Active Directory LDAP authentication
- JWT token issuance and validation
- Dynamic header injection (X-WEBAUTH-USER, X-WEBAUTH-EMAIL)
- Reverse proxy to Grafana with auth middleware
- Periodic AD group to Grafana team synchronization
- Folder permission assignment based on team mappings
- Health check endpoint
- Manual sync trigger endpoint

## Quick Start

1. Copy config.example.yaml to config.yaml and update values
2. Run: `go run cmd/savras/main.go`
3. Grafana should be configured to use auth proxy with headers

## Configuration

See config.example.yaml for all options. Key settings:
- LDAP server connection
- Grafana API credentials
- JWT secret for token signing
- Sync interval and group mappings
- Folder permissions: assign folder access to teams with specific permission levels

## Endpoints

- `/-/savras/health` - Health check
- `POST /-/savras/sync/trigger` - Manual sync trigger
- All other routes proxied to Grafana with auth

## Building

```bash
go build -o savras ./cmd/savras
```

## Docker

```bash
docker build -t savras .
docker run -v $(pwd)/config.yaml:/etc/savras/config.yaml -p 8080:8080 savras
```
