package pki

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// PKIConfig is the top-level configuration for the PKI system
type PKIConfig struct {
	// Directory for PKI files (default: secrets/pki)
	Directory string `yaml:"directory,omitempty"`

	// Age recipients for encrypting private keys
	Recipients []string `yaml:"recipients"`

	// Age identity files for decrypting private keys
	Identities []string `yaml:"identities,omitempty"`

	// Root CA configuration
	RootCA *RootCAConfig `yaml:"rootCA"`

	// Intermediate CA configuration (optional, but recommended)
	IntermediateCA *IntermediateCAYAMLConfig `yaml:"intermediateCA,omitempty"`

	// Default settings for issued certificates
	Defaults *CertDefaults `yaml:"defaults,omitempty"`

	// Host certificate definitions
	Hosts map[string]*HostConfig `yaml:"hosts,omitempty"`
}

// RootCAConfig configures the root CA
type RootCAConfig struct {
	CommonName   string `yaml:"commonName,omitempty"`
	Organization string `yaml:"organization,omitempty"`
	Validity     string `yaml:"validity,omitempty"` // e.g., "10y", "3650d"
}

// IntermediateCAYAMLConfig configures the intermediate CA
type IntermediateCAYAMLConfig struct {
	CommonName   string `yaml:"commonName,omitempty"`
	Organization string `yaml:"organization,omitempty"`
	Validity     string `yaml:"validity,omitempty"` // e.g., "5y", "1825d"
}

// CertDefaults provides default values for issued certificates
type CertDefaults struct {
	Validity     string `yaml:"validity,omitempty"` // e.g., "90d", "1y"
	Organization string `yaml:"organization,omitempty"`
}

// HostConfig defines certificates for a specific host
type HostConfig struct {
	// SANs to include in all certificates for this host
	SANs []string `yaml:"sans,omitempty"`

	// Named certificates for this host
	Certificates map[string]*CertConfig `yaml:"certificates,omitempty"`
}

// CertConfig defines a single certificate
type CertConfig struct {
	SANs     []string `yaml:"sans,omitempty"`
	Validity string   `yaml:"validity,omitempty"`
}

// LoadPKIConfig loads PKI configuration from a YAML file
func LoadPKIConfig(path string) (*PKIConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg PKIConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	// Apply defaults
	cfg.applyDefaults()

	return &cfg, nil
}

// applyDefaults fills in missing values with sensible defaults
func (c *PKIConfig) applyDefaults() {
	if c.Directory == "" {
		c.Directory = "secrets/pki"
	}

	if c.RootCA == nil {
		c.RootCA = &RootCAConfig{}
	}
	if c.RootCA.CommonName == "" {
		c.RootCA.CommonName = "NixFleet Root CA"
	}
	if c.RootCA.Organization == "" {
		c.RootCA.Organization = "NixFleet"
	}
	if c.RootCA.Validity == "" {
		c.RootCA.Validity = "10y"
	}

	if c.IntermediateCA != nil {
		if c.IntermediateCA.CommonName == "" {
			c.IntermediateCA.CommonName = "NixFleet Intermediate CA"
		}
		if c.IntermediateCA.Organization == "" {
			c.IntermediateCA.Organization = c.RootCA.Organization
		}
		if c.IntermediateCA.Validity == "" {
			c.IntermediateCA.Validity = "5y"
		}
	}

	if c.Defaults == nil {
		c.Defaults = &CertDefaults{}
	}
	if c.Defaults.Validity == "" {
		c.Defaults.Validity = "90d"
	}
	if c.Defaults.Organization == "" {
		c.Defaults.Organization = c.RootCA.Organization
	}
}

// GetRootCAConfig converts to the internal CAConfig type
func (c *PKIConfig) GetRootCAConfig() (*CAConfig, error) {
	validity, err := ParseValidityDuration(c.RootCA.Validity)
	if err != nil {
		return nil, fmt.Errorf("parsing root CA validity: %w", err)
	}

	return &CAConfig{
		CommonName:   c.RootCA.CommonName,
		Organization: c.RootCA.Organization,
		Validity:     validity,
	}, nil
}

// GetIntermediateCAConfig converts to the internal IntermediateCAConfig type
func (c *PKIConfig) GetIntermediateCAConfig() (*IntermediateCAConfig, error) {
	if c.IntermediateCA == nil {
		return nil, fmt.Errorf("intermediate CA not configured")
	}

	validity, err := ParseValidityDuration(c.IntermediateCA.Validity)
	if err != nil {
		return nil, fmt.Errorf("parsing intermediate CA validity: %w", err)
	}

	return &IntermediateCAConfig{
		CommonName:   c.IntermediateCA.CommonName,
		Organization: c.IntermediateCA.Organization,
		Validity:     validity,
	}, nil
}

// GetDefaultValidity returns the default certificate validity duration
func (c *PKIConfig) GetDefaultValidity() (time.Duration, error) {
	return ParseValidityDuration(c.Defaults.Validity)
}

// GetHostCertRequest creates a CertRequest for a host's named certificate
func (c *PKIConfig) GetHostCertRequest(hostname, certName string) (*CertRequest, error) {
	validity, err := c.GetDefaultValidity()
	if err != nil {
		return nil, err
	}

	req := &CertRequest{
		Hostname: hostname,
		Name:     certName,
		Validity: validity,
	}

	// Add host-level SANs
	if host, ok := c.Hosts[hostname]; ok {
		req.SANs = append(req.SANs, host.SANs...)

		// Add certificate-specific config
		if certName != "" && certName != "host" {
			if cert, ok := host.Certificates[certName]; ok {
				req.SANs = append(req.SANs, cert.SANs...)
				if cert.Validity != "" {
					v, err := ParseValidityDuration(cert.Validity)
					if err != nil {
						return nil, fmt.Errorf("parsing certificate validity: %w", err)
					}
					req.Validity = v
				}
			}
		}
	}

	return req, nil
}

// Validate checks the configuration for errors
func (c *PKIConfig) Validate() error {
	if len(c.Recipients) == 0 {
		return fmt.Errorf("at least one age recipient is required")
	}

	// Validate recipient format (should start with "age1")
	for _, r := range c.Recipients {
		if len(r) < 4 || r[:4] != "age1" {
			return fmt.Errorf("invalid age recipient format: %s (should start with 'age1')", r)
		}
	}

	// Validate validity durations
	if _, err := ParseValidityDuration(c.RootCA.Validity); err != nil {
		return fmt.Errorf("invalid root CA validity: %w", err)
	}

	if c.IntermediateCA != nil {
		if _, err := ParseValidityDuration(c.IntermediateCA.Validity); err != nil {
			return fmt.Errorf("invalid intermediate CA validity: %w", err)
		}
	}

	if _, err := ParseValidityDuration(c.Defaults.Validity); err != nil {
		return fmt.Errorf("invalid default certificate validity: %w", err)
	}

	return nil
}
