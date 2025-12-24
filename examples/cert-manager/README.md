# cert-manager Integration Examples

This directory contains example Kubernetes manifests for integrating NixFleet PKI with cert-manager.

## Quick Start

### Two-Tier PKI (Recommended)

```bash
# 1. Initialize Root and Intermediate CA
nixfleet pki init -r age1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
nixfleet pki init-intermediate -r age1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx

# 2. Export intermediate CA secret and ClusterIssuer
nixfleet pki certmanager export --intermediate -n cert-manager -o ca-secret.json
nixfleet pki certmanager issuer -o cluster-issuer.json

# 3. Apply to cluster
kubectl apply -f ca-secret.json
kubectl apply -f cluster-issuer.json

# 4. Apply example resources
kubectl apply -f certificate.yaml
kubectl apply -f deployment.yaml
```

### Single-Tier PKI (Simple)

```bash
# 1. Initialize CA (if not already done)
nixfleet pki init -r age1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx

# 2. Export CA secret and ClusterIssuer
nixfleet pki certmanager export --ca -n cert-manager -o ca-secret.json
nixfleet pki certmanager issuer -o cluster-issuer.json

# 3. Apply to cluster
kubectl apply -f ca-secret.json
kubectl apply -f cluster-issuer.json

# 4. Apply example resources
kubectl apply -f certificate.yaml
kubectl apply -f deployment.yaml
```

## Files

| File | Description |
|------|-------------|
| `cluster-issuer.yaml` | ClusterIssuer that uses NixFleet CA |
| `certificate.yaml` | Example Certificate resource |
| `deployment.yaml` | Example deployment using the certificate |
| `ingress-tls.yaml` | Ingress with automatic TLS |
| `mtls-server.yaml` | mTLS server deployment |
| `mtls-client.yaml` | mTLS client deployment |

## Generating Secrets

The CA secret and ClusterIssuer are generated dynamically:

```bash
# CA secret (contains private key - handle carefully!)
nixfleet pki certmanager export --ca -n cert-manager

# ClusterIssuer configuration
nixfleet pki certmanager issuer

# Host certificate as secret
nixfleet pki certmanager export --hostname myhost --cert-name web -n default
```
