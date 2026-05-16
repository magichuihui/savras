#!/bin/sh
# Savras startup script

# Copy config.example.yaml to config.yaml and update values:
#   - ldap.bind_password: your LDAP admin password
#   - auth.jwt_secret: a random string for JWT signing
#   - grafana.url: your Grafana address
#   - grafana.api_token: your Grafana API token

if [ ! -f config.yaml ]; then
    cp config.example.yaml config.yaml
    echo "Please edit config.yaml with your settings"
    exit 1
fi

./savras
