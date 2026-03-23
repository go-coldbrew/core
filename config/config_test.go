package config

import (
	"strings"
	"testing"
)

func TestValidateDefaults(t *testing.T) {
	c := Config{
		GRPCPort:                        9090,
		HTTPPort:                        9091,
		ShutdownDurationInSeconds:       15,
		HealthcheckWaitDurationInSeconds: 7,
	}
	warnings := c.Validate()
	if len(warnings) > 0 {
		t.Errorf("default config should have no warnings, got: %v", warnings)
	}
}

func TestValidatePortZero(t *testing.T) {
	// Port 0 means ephemeral — should be valid
	c := Config{GRPCPort: 0, HTTPPort: 0}
	warnings := c.Validate()
	for _, w := range warnings {
		if strings.Contains(w, "Port") && strings.Contains(w, "range") {
			t.Errorf("port 0 should be valid, got warning: %s", w)
		}
		if strings.Contains(w, "port conflict") {
			t.Errorf("port 0 should not warn about conflict, got warning: %s", w)
		}
	}
}

func TestValidatePortConflict(t *testing.T) {
	c := Config{GRPCPort: 8080, HTTPPort: 8080}
	warnings := c.Validate()
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "port conflict") {
			found = true
		}
	}
	if !found {
		t.Error("same non-zero ports should warn about conflict")
	}
}

func TestValidateSamplingRatio(t *testing.T) {
	c := Config{
		GRPCPort:                    9090,
		HTTPPort:                    9091,
		NewRelicOpentelemetrySample: 1.5,
		OTLPSamplingRatio:          -0.1,
	}
	warnings := c.Validate()
	foundNR := false
	foundOTLP := false
	for _, w := range warnings {
		if strings.Contains(w, "NewRelicOpentelemetrySample") {
			foundNR = true
		}
		if strings.Contains(w, "OTLPSamplingRatio") {
			foundOTLP = true
		}
	}
	if !foundNR || !foundOTLP {
		t.Errorf("expected warnings for both sampling fields, got: %v", warnings)
	}
}

func TestValidateTLSMismatch(t *testing.T) {
	c := Config{
		GRPCPort:        9090,
		HTTPPort:        9091,
		GRPCTLSCertFile: "/path/to/cert",
		// GRPCTLSKeyFile intentionally empty
	}
	warnings := c.Validate()
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "TLS") {
			found = true
		}
	}
	if !found {
		t.Error("mismatched TLS cert/key should produce a warning")
	}
}

func TestValidateShutdownTiming(t *testing.T) {
	c := Config{
		GRPCPort:                        9090,
		HTTPPort:                        9091,
		ShutdownDurationInSeconds:       5,
		HealthcheckWaitDurationInSeconds: 10,
	}
	warnings := c.Validate()
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "HealthcheckWaitDurationInSeconds") {
			found = true
		}
	}
	if !found {
		t.Error("healthcheck duration >= shutdown duration should produce a warning")
	}
}
