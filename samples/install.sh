#!/bin/bash
# ============================================================
# Savras + Grafana + OpenLDAP — Full Stack K8s Deployment
# ============================================================
# Prerequisites:
#   - Helm 3.x installed
#   - Access to a Kubernetes cluster
#   - kubectl configured
#
# This script:
#   1. Creates the monitoring namespace
#   2. Deploys OpenLDAP with test seed data
#   3. Creates the Savras ConfigMap
#   4. Deploys Grafana (with Savras as a sidecar container)
#
# Usage:
#   chmod +x samples/install.sh
#   ./samples/install.sh
#
# For local testing without Kubernetes:
#   make run   # runs Savras standalone with config.yaml
# ============================================================

set -euo pipefail

NAMESPACE="${NAMESPACE:-monitoring}"
RELEASE_NAME="${RELEASE_NAME:-grafana}"
VALUES_FILE="${VALUES_FILE:-samples/grafana/values.yaml}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "============================================================"
echo "  Savras + Grafana + OpenLDAP Stack Installer"
echo "============================================================"
echo ""

# ---- Prerequisites ----
echo "==> Checking prerequisites..."
command -v helm  >/dev/null 2>&1 || { echo "ERROR: helm is required but not found."; exit 1; }
command -v kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl is required but not found."; exit 1; }
echo "    helm   — found"
echo "    kubectl — found"
echo ""

# ---- Namespace ----
echo "==> Creating namespace: ${NAMESPACE}"
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
echo ""

# ---- OpenLDAP ----
echo "==> Deploying OpenLDAP with test users..."
kubectl apply -f "${SCRIPT_DIR}/openldap/ldap.yaml"
echo ""

# ---- Savras ConfigMap ----
echo "==> Creating Savras ConfigMap..."
kubectl apply -f "${SCRIPT_DIR}/grafana/configmap.yaml"
echo ""

# ---- Grafana repo ----
echo "==> Adding Grafana Helm repo..."
helm repo add grafana https://grafana.github.io/helm-charts --force-update
helm repo update
echo ""

# ---- Grafana + Savras sidecar ----
echo "==> Deploying Grafana with Savras sidecar..."
helm upgrade --install "${RELEASE_NAME}" grafana/grafana \
  --namespace "${NAMESPACE}" \
  --values "${VALUES_FILE}" \
  --wait \
  --timeout 5m

echo ""
echo "============================================================"
echo "  Deployment complete!"
echo "============================================================"
echo ""
echo "Resources in namespace '${NAMESPACE}':"
kubectl -n "${NAMESPACE}" get pods
echo ""
echo "Next steps:"
echo "  1. Port-forward Savras:"
echo "     kubectl -n ${NAMESPACE} port-forward svc/savras 4181:4181"
echo ""
echo "  2. Port-forward Savras via Grafana (if no separate Service):"
echo "     kubectl -n ${NAMESPACE} port-forward svc/${RELEASE_NAME} 4181:4181"
echo ""
echo "  3. Login at http://localhost:4181"
echo "     - LDAP users:  testuser / testpass  (devops group)"
echo "     -               jane    / janepass   (developer group)"
echo "     - Local admin: admin   / <from Grafana secret>"
echo ""
echo "  4. To get the Grafana admin password:"
echo "     kubectl -n ${NAMESPACE} get secret ${RELEASE_NAME} -o jsonpath='{.data.admin-password}' | base64 -d"
echo ""
echo "  5. Watch pods:"
echo "     kubectl -n ${NAMESPACE} get pods -w"
echo ""
