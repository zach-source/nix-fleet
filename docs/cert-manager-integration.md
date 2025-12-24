# cert-manager Integration

This guide shows how to use NixFleet's PKI as a certificate source for Kubernetes cert-manager.

## Overview

NixFleet can integrate with cert-manager in two ways:

1. **CA Issuer** - Export the CA certificate and key to Kubernetes, letting cert-manager sign certificates directly
2. **Webhook Signer** - Run a webhook server that signs CSRs without exposing the CA private key to Kubernetes

## PKI Hierarchy Options

NixFleet supports two PKI architectures:

### Single-Tier (Root CA Only)

```
Root CA → Server Certificates
```

Simple setup where the root CA directly signs server certificates.

### Two-Tier (Root + Intermediate CA) - Recommended

```
Root CA → Intermediate CA → Server Certificates
```

The recommended architecture for production:
- **Root CA**: Kept offline/encrypted, only used to sign the intermediate CA
- **Intermediate CA**: Used for day-to-day certificate signing
- **Server Certificates**: Include full chain (cert + intermediate + root) for validation

```bash
# Initialize two-tier PKI
nixfleet pki init -r age1...                    # Create root CA
nixfleet pki init-intermediate -r age1...       # Create intermediate CA

# Issue certificates (automatically uses intermediate CA)
nixfleet pki issue myhost --san myhost.example.com
```

When an intermediate CA is present, `pki issue` automatically uses it for signing and includes the full certificate chain.

## Option 1: CA Issuer (Simple)

This approach exports the CA to Kubernetes and lets cert-manager handle signing.

### Step 1: Export CA Secret

For **two-tier PKI** (recommended), export the intermediate CA:

```bash
# Export the intermediate CA as a Kubernetes secret
nixfleet pki certmanager export --intermediate \
  --namespace cert-manager \
  --secret-name nixfleet-ca \
  -o ca-secret.yaml

# Apply to cluster
kubectl apply -f ca-secret.yaml
```

For **single-tier PKI**, export the root CA:

```bash
# Export the root CA as a Kubernetes secret
nixfleet pki certmanager export --ca \
  --namespace cert-manager \
  --secret-name nixfleet-ca \
  -o ca-secret.yaml

# Apply to cluster
kubectl apply -f ca-secret.yaml
```

> **Note**: When using two-tier PKI, the intermediate CA secret includes the chain certificate (`ca.crt`) which contains both the intermediate and root CA certificates, allowing clients to build the full trust chain.

### Step 2: Create ClusterIssuer

```bash
# Generate the ClusterIssuer configuration
nixfleet pki certmanager issuer \
  --secret-name nixfleet-ca \
  --secret-namespace cert-manager \
  --issuer-name nixfleet-ca-issuer \
  -o cluster-issuer.yaml

# Apply to cluster
kubectl apply -f cluster-issuer.yaml
```

Or create manually:

```yaml
# cluster-issuer.yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: nixfleet-ca-issuer
  labels:
    app.kubernetes.io/managed-by: nixfleet
spec:
  ca:
    secretName: nixfleet-ca
```

### Step 3: Request Certificates

```yaml
# certificate.yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: my-app-tls
  namespace: default
spec:
  secretName: my-app-tls-secret
  duration: 2160h    # 90 days
  renewBefore: 360h  # 15 days before expiry

  subject:
    organizations:
      - NixFleet

  commonName: my-app.example.com

  dnsNames:
    - my-app.example.com
    - my-app.default.svc.cluster.local

  ipAddresses:
    - 10.0.0.1

  privateKey:
    algorithm: ECDSA
    size: 256

  issuerRef:
    name: nixfleet-ca-issuer
    kind: ClusterIssuer
    group: cert-manager.io
```

### Step 4: Use in Deployment

```yaml
# deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
  namespace: default
spec:
  replicas: 1
  selector:
    matchLabels:
      app: my-app
  template:
    metadata:
      labels:
        app: my-app
    spec:
      containers:
        - name: my-app
          image: my-app:latest
          ports:
            - containerPort: 8443
              name: https
          volumeMounts:
            - name: tls-certs
              mountPath: /etc/tls
              readOnly: true
          env:
            - name: TLS_CERT_FILE
              value: /etc/tls/tls.crt
            - name: TLS_KEY_FILE
              value: /etc/tls/tls.key
            - name: TLS_CA_FILE
              value: /etc/tls/ca.crt
      volumes:
        - name: tls-certs
          secret:
            secretName: my-app-tls-secret
---
apiVersion: v1
kind: Service
metadata:
  name: my-app
  namespace: default
spec:
  selector:
    app: my-app
  ports:
    - port: 443
      targetPort: 8443
      name: https
```

## Option 2: Webhook Signer (More Secure)

This approach keeps the CA private key outside of Kubernetes.

### Step 1: Start Webhook Server

Run the webhook server on a trusted host (not in Kubernetes):

```bash
# Generate TLS cert for the webhook server itself
nixfleet pki issue webhook-server --san webhook.example.com

# Start the webhook server
nixfleet pki certmanager serve \
  --listen :8443 \
  --tls-cert secrets/pki/hosts/webhook-server/host.crt \
  --tls-key secrets/pki/hosts/webhook-server/host.key \
  --identity ~/.config/age/key.txt
```

### Step 2: Configure cert-manager External Issuer

You'll need to configure cert-manager to use an external signer. This requires the [cert-manager external issuer](https://cert-manager.io/docs/configuration/external/) or a custom controller.

## Complete Example: Ingress with TLS

### Prerequisites

```bash
# Ensure CA is initialized
nixfleet pki init -r age1...

# Export CA to cluster
nixfleet pki certmanager export --ca -n cert-manager | kubectl apply -f -
nixfleet pki certmanager issuer | kubectl apply -f -
```

### Ingress with Automatic TLS

```yaml
# ingress.yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: my-app-ingress
  namespace: default
  annotations:
    cert-manager.io/cluster-issuer: nixfleet-ca-issuer
spec:
  ingressClassName: nginx
  tls:
    - hosts:
        - my-app.example.com
      secretName: my-app-ingress-tls
  rules:
    - host: my-app.example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: my-app
                port:
                  number: 80
```

cert-manager will automatically:
1. Create a Certificate resource
2. Generate a CSR
3. Sign it using the NixFleet CA
4. Store the certificate in `my-app-ingress-tls` secret
5. Renew before expiration

## mTLS Example: Service-to-Service Authentication

### Issue Client and Server Certificates

```bash
# Server certificate
nixfleet pki issue api-server \
  --name server \
  --san api-server.default.svc.cluster.local

# Client certificate
nixfleet pki issue api-client \
  --name client \
  --san api-client.default.svc.cluster.local

# Export as secrets
nixfleet pki certmanager export --hostname api-server --cert-name server \
  -n default --secret-name api-server-tls | kubectl apply -f -

nixfleet pki certmanager export --hostname api-client --cert-name client \
  -n default --secret-name api-client-tls | kubectl apply -f -
```

### Server Deployment (Requires Client Certs)

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api-server
spec:
  replicas: 1
  selector:
    matchLabels:
      app: api-server
  template:
    metadata:
      labels:
        app: api-server
    spec:
      containers:
        - name: api-server
          image: api-server:latest
          ports:
            - containerPort: 8443
          volumeMounts:
            - name: server-tls
              mountPath: /etc/tls/server
              readOnly: true
          env:
            - name: TLS_CERT
              value: /etc/tls/server/tls.crt
            - name: TLS_KEY
              value: /etc/tls/server/tls.key
            - name: TLS_CA
              value: /etc/tls/server/ca.crt
            - name: TLS_CLIENT_AUTH
              value: "require"  # Require valid client certificates
      volumes:
        - name: server-tls
          secret:
            secretName: api-server-tls
```

### Client Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api-client
spec:
  replicas: 1
  selector:
    matchLabels:
      app: api-client
  template:
    metadata:
      labels:
        app: api-client
    spec:
      containers:
        - name: api-client
          image: api-client:latest
          volumeMounts:
            - name: client-tls
              mountPath: /etc/tls/client
              readOnly: true
          env:
            - name: API_URL
              value: https://api-server:8443
            - name: TLS_CERT
              value: /etc/tls/client/tls.crt
            - name: TLS_KEY
              value: /etc/tls/client/tls.key
            - name: TLS_CA
              value: /etc/tls/client/ca.crt
      volumes:
        - name: client-tls
          secret:
            secretName: api-client-tls
```

## Trusting the CA in Pods

To trust the NixFleet CA in your application pods:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: nixfleet-ca-bundle
  namespace: default
data:
  ca.crt: |
    -----BEGIN CERTIFICATE-----
    # Contents of secrets/pki/ca/root.crt
    -----END CERTIFICATE-----
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-app
spec:
  template:
    spec:
      containers:
        - name: my-app
          volumeMounts:
            - name: ca-bundle
              mountPath: /etc/ssl/certs/nixfleet-ca.crt
              subPath: ca.crt
              readOnly: true
      volumes:
        - name: ca-bundle
          configMap:
            name: nixfleet-ca-bundle
```

Or export the CA certificate directly:

```bash
nixfleet pki export --format pem > ca.crt
kubectl create configmap nixfleet-ca-bundle --from-file=ca.crt
```

## Automation with ArgoCD/Flux

### Sealed Secrets Approach

For GitOps workflows, you can seal the CA secret:

```bash
# Export and seal the CA secret
nixfleet pki certmanager export --ca -n cert-manager | \
  kubeseal --format yaml > sealed-ca-secret.yaml

# Commit to git
git add sealed-ca-secret.yaml
git commit -m "Add sealed NixFleet CA secret"
```

### External Secrets Approach

If using External Secrets Operator with a vault:

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: nixfleet-ca
  namespace: cert-manager
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-backend
    kind: ClusterSecretStore
  target:
    name: nixfleet-ca
    template:
      type: kubernetes.io/tls
  data:
    - secretKey: tls.crt
      remoteRef:
        key: pki/nixfleet/ca
        property: certificate
    - secretKey: tls.key
      remoteRef:
        key: pki/nixfleet/ca
        property: private_key
```

## Monitoring Certificate Expiry

cert-manager exposes Prometheus metrics for certificate status:

```yaml
# servicemonitor.yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: cert-manager
  namespace: cert-manager
spec:
  selector:
    matchLabels:
      app: cert-manager
  endpoints:
    - port: tcp-prometheus-servicemonitor
```

Alert on expiring certificates:

```yaml
# alerting-rules.yaml
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: cert-manager-alerts
spec:
  groups:
    - name: cert-manager
      rules:
        - alert: CertificateExpiringSoon
          expr: certmanager_certificate_expiration_timestamp_seconds - time() < 604800
          for: 1h
          labels:
            severity: warning
          annotations:
            summary: "Certificate {{ $labels.name }} expires in less than 7 days"
```
