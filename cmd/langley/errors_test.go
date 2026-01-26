package main

import (
	"errors"
	"strings"
	"testing"
)

func TestActionableError_Format(t *testing.T) {
	err := &ActionableError{
		What:  "Port binding failed",
		Cause: errors.New("address already in use"),
		Fix:   "Kill the existing process",
	}

	formatted := err.Format()
	if !strings.Contains(formatted, "Error: Port binding failed") {
		t.Error("Format should contain what failed")
	}
	if !strings.Contains(formatted, "Cause: address already in use") {
		t.Error("Format should contain the cause")
	}
	if !strings.Contains(formatted, "Fix:   Kill the existing process") {
		t.Error("Format should contain the fix")
	}
}

func TestActionableError_Error(t *testing.T) {
	err := &ActionableError{
		What:  "Port binding failed",
		Cause: errors.New("address already in use"),
		Fix:   "Kill the existing process",
	}

	if err.Error() != "Port binding failed: address already in use" {
		t.Errorf("Error() = %q, want %q", err.Error(), "Port binding failed: address already in use")
	}
}

func TestPortInUseFix(t *testing.T) {
	fix := portInUseFix("localhost:9090", 10)

	// Should contain port number
	if !strings.Contains(fix, "9090") {
		t.Error("Fix should contain the port number")
	}

	// Should contain kill/stop instructions
	if !strings.Contains(fix, "kill") && !strings.Contains(fix, "taskkill") {
		t.Error("Fix should contain kill instructions")
	}

	// Should suggest alternative port
	if !strings.Contains(fix, "9100") {
		t.Error("Fix should suggest alternative port")
	}
}

func TestPortNum(t *testing.T) {
	tests := []struct {
		port string
		want int
	}{
		{"9090", 9090},
		{"8080", 8080},
		{"abc", 0},
		{"", 0},
	}

	for _, tt := range tests {
		if got := portNum(tt.port); got != tt.want {
			t.Errorf("portNum(%q) = %d, want %d", tt.port, got, tt.want)
		}
	}
}

func TestCaCorruptFix(t *testing.T) {
	fix := caCorruptFix("/path/to/certs")

	// Should contain the certs directory
	if !strings.Contains(fix, "/path/to/certs") {
		t.Error("Fix should contain the certs directory")
	}

	// Should suggest deleting and regenerating
	if !strings.Contains(fix, "ca.crt") || !strings.Contains(fix, "ca.key") {
		t.Error("Fix should mention ca.crt and ca.key files")
	}

	// Should suggest running setup
	if !strings.Contains(fix, "langley setup") {
		t.Error("Fix should suggest running langley setup")
	}
}

func TestDbLockedFix(t *testing.T) {
	fix := dbLockedFix("/path/to/db.sqlite")

	// Should mention checking for other processes
	if !strings.Contains(fix, "another") || !strings.Contains(fix, "langley") {
		t.Error("Fix should mention checking for other langley instances")
	}

	// Should contain the database path
	if !strings.Contains(fix, "/path/to/db.sqlite") {
		t.Error("Fix should contain the database path")
	}
}

func TestIsDBLocked(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{errors.New("database is locked"), true},
		{errors.New("SQLITE_BUSY"), true},
		{errors.New("cannot start a transaction within a transaction"), true},
		{errors.New("some other error"), false},
		{nil, false},
	}

	for _, tt := range tests {
		if got := isDBLocked(tt.err); got != tt.want {
			errStr := "<nil>"
			if tt.err != nil {
				errStr = tt.err.Error()
			}
			t.Errorf("isDBLocked(%q) = %v, want %v", errStr, got, tt.want)
		}
	}
}

func TestIsPermissionError(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{errors.New("permission denied"), true},
		{errors.New("access is denied"), true},
		{errors.New("Access is denied"), true},
		{errors.New("some other error"), false},
		{nil, false},
	}

	for _, tt := range tests {
		if got := isPermissionError(tt.err); got != tt.want {
			errStr := "<nil>"
			if tt.err != nil {
				errStr = tt.err.Error()
			}
			t.Errorf("isPermissionError(%q) = %v, want %v", errStr, got, tt.want)
		}
	}
}

func TestIsCorruptCert(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{errors.New("failed to decode certificate"), true},
		{errors.New("parsing CA certificate: invalid data"), true},
		{errors.New("parsing CA private key: bad format"), true},
		{errors.New("malformed PEM data"), true},
		{errors.New("invalid certificate"), true},
		{errors.New("network timeout"), false},
		{nil, false},
	}

	for _, tt := range tests {
		if got := isCorruptCert(tt.err); got != tt.want {
			errStr := "<nil>"
			if tt.err != nil {
				errStr = tt.err.Error()
			}
			t.Errorf("isCorruptCert(%q) = %v, want %v", errStr, got, tt.want)
		}
	}
}
