package main

import (
	"strings"
	"testing"
)

func TestFormatEnvVars_Unix(t *testing.T) {
	output := formatEnvVars("localhost:9090", "/home/user/.config/langley/certs/ca.crt", "linux")

	// Should use export syntax
	if !strings.Contains(output, "export HTTPS_PROXY=") {
		t.Error("Unix output should use 'export' syntax")
	}
	if !strings.Contains(output, "export NODE_EXTRA_CA_CERTS=") {
		t.Error("Unix output should include NODE_EXTRA_CA_CERTS")
	}
	if !strings.Contains(output, "export SSL_CERT_FILE=") {
		t.Error("Unix output should include SSL_CERT_FILE")
	}
	if !strings.Contains(output, "export REQUESTS_CA_BUNDLE=") {
		t.Error("Unix output should include REQUESTS_CA_BUNDLE")
	}

	// Should NOT use PowerShell syntax
	if strings.Contains(output, "$env:") {
		t.Error("Unix output should not use PowerShell syntax")
	}
}

func TestFormatEnvVars_Darwin(t *testing.T) {
	output := formatEnvVars("localhost:9090", "/Users/test/.config/langley/certs/ca.crt", "darwin")

	// macOS should also use export syntax
	if !strings.Contains(output, "export HTTPS_PROXY=") {
		t.Error("macOS output should use 'export' syntax")
	}
}

func TestFormatEnvVars_Windows(t *testing.T) {
	output := formatEnvVars("localhost:9090", "C:\\Users\\test\\AppData\\Roaming\\langley\\certs\\ca.crt", "windows")

	// Should use PowerShell $env: syntax
	if !strings.Contains(output, "$env:HTTPS_PROXY") {
		t.Error("Windows output should use '$env:' syntax")
	}
	if !strings.Contains(output, "$env:NODE_EXTRA_CA_CERTS") {
		t.Error("Windows output should include NODE_EXTRA_CA_CERTS")
	}
	if !strings.Contains(output, "$env:SSL_CERT_FILE") {
		t.Error("Windows output should include SSL_CERT_FILE")
	}

	// Should NOT use export syntax
	if strings.Contains(output, "export ") {
		t.Error("Windows output should not use 'export' syntax")
	}
}

func TestFormatEnvVars_ContainsProxyAddr(t *testing.T) {
	proxyAddr := "127.0.0.1:8080"
	output := formatEnvVars(proxyAddr, "/path/to/ca.crt", "linux")

	if !strings.Contains(output, "http://"+proxyAddr) {
		t.Errorf("Output should contain proxy address %s", proxyAddr)
	}
}

func TestFormatEnvVars_ContainsCAPath(t *testing.T) {
	caPath := "/custom/path/ca.crt"
	output := formatEnvVars("localhost:9090", caPath, "linux")

	if !strings.Contains(output, caPath) {
		t.Errorf("Output should contain CA path %s", caPath)
	}
}
