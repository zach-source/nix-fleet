package pki

import (
	"context"
	"fmt"
	"time"
)

// RotationConfig configures certificate rotation behavior
type RotationConfig struct {
	// RenewBefore is how long before expiry to renew (default: 30 days)
	RenewBefore time.Duration

	// DryRun shows what would be renewed without making changes
	DryRun bool

	// Force renews all certificates regardless of expiry
	Force bool
}

// DefaultRotationConfig returns sensible defaults
func DefaultRotationConfig() *RotationConfig {
	return &RotationConfig{
		RenewBefore: 30 * 24 * time.Hour, // 30 days
	}
}

// RotationResult tracks renewal outcomes
type RotationResult struct {
	Hostname  string
	CertName  string
	Action    string // "renewed", "skipped", "failed"
	Message   string
	ExpiresAt time.Time
	DaysLeft  int
}

// RotateCertificates checks and renews expiring certificates
func (s *Store) RotateCertificates(ctx context.Context, issuer CertIssuer, cfg *RotationConfig) ([]RotationResult, error) {
	if cfg == nil {
		cfg = DefaultRotationConfig()
	}

	hosts, err := s.ListHostCerts()
	if err != nil {
		return nil, fmt.Errorf("listing hosts: %w", err)
	}

	var results []RotationResult
	now := time.Now()
	renewThreshold := now.Add(cfg.RenewBefore)

	for _, hostname := range hosts {
		certNames, err := s.ListHostNamedCerts(hostname)
		if err != nil {
			results = append(results, RotationResult{
				Hostname: hostname,
				Action:   "failed",
				Message:  fmt.Sprintf("listing certs: %v", err),
			})
			continue
		}

		for _, certName := range certNames {
			// Skip chain files
			if len(certName) > 6 && certName[len(certName)-6:] == ".chain" {
				continue
			}

			info, err := s.GetNamedCertInfo(hostname, certName)
			if err != nil {
				results = append(results, RotationResult{
					Hostname: hostname,
					CertName: certName,
					Action:   "failed",
					Message:  fmt.Sprintf("reading cert: %v", err),
				})
				continue
			}

			daysLeft := int(info.NotAfter.Sub(now).Hours() / 24)
			needsRenewal := info.NotAfter.Before(renewThreshold) || cfg.Force

			if !needsRenewal {
				results = append(results, RotationResult{
					Hostname:  hostname,
					CertName:  certName,
					Action:    "skipped",
					Message:   fmt.Sprintf("valid for %d more days", daysLeft),
					ExpiresAt: info.NotAfter,
					DaysLeft:  daysLeft,
				})
				continue
			}

			if cfg.DryRun {
				results = append(results, RotationResult{
					Hostname:  hostname,
					CertName:  certName,
					Action:    "would-renew",
					Message:   fmt.Sprintf("expires in %d days", daysLeft),
					ExpiresAt: info.NotAfter,
					DaysLeft:  daysLeft,
				})
				continue
			}

			// Load existing cert to preserve SANs
			existingCert, err := s.LoadNamedCert(ctx, hostname, certName)
			if err != nil {
				results = append(results, RotationResult{
					Hostname: hostname,
					CertName: certName,
					Action:   "failed",
					Message:  fmt.Sprintf("loading cert: %v", err),
				})
				continue
			}

			// Create renewal request with same parameters
			req := &CertRequest{
				Hostname: hostname,
				Name:     certName,
				SANs:     existingCert.SANs,
				Validity: existingCert.NotAfter.Sub(existingCert.NotBefore), // Preserve original validity
			}

			// Issue new certificate
			newCert, err := issuer.IssueCert(req)
			if err != nil {
				results = append(results, RotationResult{
					Hostname: hostname,
					CertName: certName,
					Action:   "failed",
					Message:  fmt.Sprintf("issuing cert: %v", err),
				})
				continue
			}

			// Save renewed certificate
			if err := s.SaveHostCert(newCert); err != nil {
				results = append(results, RotationResult{
					Hostname: hostname,
					CertName: certName,
					Action:   "failed",
					Message:  fmt.Sprintf("saving cert: %v", err),
				})
				continue
			}

			results = append(results, RotationResult{
				Hostname:  hostname,
				CertName:  certName,
				Action:    "renewed",
				Message:   fmt.Sprintf("was expiring in %d days, now valid until %s", daysLeft, newCert.NotAfter.Format("2006-01-02")),
				ExpiresAt: newCert.NotAfter,
				DaysLeft:  int(newCert.NotAfter.Sub(now).Hours() / 24),
			})
		}
	}

	return results, nil
}

// CertIssuer is the interface for certificate issuers (CA or IntermediateCA)
type CertIssuer interface {
	IssueCert(req *CertRequest) (*IssuedCert, error)
}

// SystemdService returns the systemd service unit content
func SystemdService(nixfleetPath, configFile, pkiDir string, identities []string) string {
	identityArgs := ""
	for _, id := range identities {
		identityArgs += fmt.Sprintf(" --identity %s", id)
	}

	configArg := ""
	if configFile != "" {
		configArg = fmt.Sprintf(" --config %s", configFile)
	}

	return fmt.Sprintf(`[Unit]
Description=NixFleet PKI Certificate Rotation
Documentation=https://github.com/nixfleet/nixfleet
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=%s pki renew --pki-dir %s%s%s
# Run as root to access age identity files
User=root
# Prevent accidental exposure of secrets
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=%s

[Install]
WantedBy=multi-user.target
`, nixfleetPath, pkiDir, identityArgs, configArg, pkiDir)
}

// SystemdTimer returns the systemd timer unit content
func SystemdTimer(onCalendar string) string {
	if onCalendar == "" {
		onCalendar = "daily" // Default: run once per day
	}

	return fmt.Sprintf(`[Unit]
Description=NixFleet PKI Certificate Rotation Timer
Documentation=https://github.com/nixfleet/nixfleet

[Timer]
OnCalendar=%s
RandomizedDelaySec=1h
Persistent=true

[Install]
WantedBy=timers.target
`, onCalendar)
}

// SystemdUnitPaths returns the paths for the systemd units
func SystemdUnitPaths(unitName string) (servicePath, timerPath string) {
	servicePath = fmt.Sprintf("/etc/systemd/system/%s.service", unitName)
	timerPath = fmt.Sprintf("/etc/systemd/system/%s.timer", unitName)
	return
}
