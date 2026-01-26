package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

// ActionableError represents an error with user-friendly guidance.
type ActionableError struct {
	What  string // What failed (short summary)
	Cause error  // Technical error details
	Fix   string // Actionable guidance
}

func (e *ActionableError) Error() string {
	return fmt.Sprintf("%s: %v", e.What, e.Cause)
}

// Format returns the full actionable error message for display.
func (e *ActionableError) Format() string {
	var sb strings.Builder
	sb.WriteString("Error: ")
	sb.WriteString(e.What)
	sb.WriteString("\nCause: ")
	sb.WriteString(e.Cause.Error())
	sb.WriteString("\nFix:   ")
	sb.WriteString(e.Fix)
	return sb.String()
}

// printError prints an actionable error to stderr and exits.
func printError(what string, cause error, fix string) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Error:", what)
	fmt.Fprintln(os.Stderr, "Cause:", cause)
	fmt.Fprintln(os.Stderr, "Fix:  ", fix)
	fmt.Fprintln(os.Stderr)
	os.Exit(1)
}

// portInUseFix returns OS-specific instructions for freeing a port.
func portInUseFix(baseAddr string, attempts int) string {
	// Extract port from address (e.g., "localhost:9090" -> "9090")
	port := baseAddr
	if idx := strings.LastIndex(baseAddr, ":"); idx != -1 {
		port = baseAddr[idx+1:]
	}

	switch runtime.GOOS {
	case "windows":
		return fmt.Sprintf(`Ports %s-%d are all in use. Find and stop the process:
       netstat -ano | findstr :%s
       taskkill /PID <pid> /F

       Or use a different port:
       langley -listen localhost:9100`, port, portNum(port)+attempts-1, port)

	case "darwin":
		return fmt.Sprintf(`Ports %s-%d are all in use. Find and stop the process:
       lsof -i :%s
       kill <pid>

       Or use a different port:
       langley -listen localhost:9100`, port, portNum(port)+attempts-1, port)

	default: // linux and others
		return fmt.Sprintf(`Ports %s-%d are all in use. Find and stop the process:
       ss -tlnp | grep :%s
       # or: lsof -i :%s
       kill <pid>

       Or use a different port:
       langley -listen localhost:9100`, port, portNum(port)+attempts-1, port, port)
	}
}

// portNum converts port string to int, returns 0 on error.
func portNum(port string) int {
	var n int
	_, _ = fmt.Sscanf(port, "%d", &n)
	return n
}

// caCorruptFix returns instructions for regenerating the CA certificate.
func caCorruptFix(certsDir string) string {
	switch runtime.GOOS {
	case "windows":
		return fmt.Sprintf(`The CA certificate appears corrupted. Delete and regenerate:
       del /Q "%s\\ca.crt" "%s\\ca.key"
       langley setup`, certsDir, certsDir)

	default:
		return fmt.Sprintf(`The CA certificate appears corrupted. Delete and regenerate:
       rm -f "%s/ca.crt" "%s/ca.key"
       langley setup`, certsDir, certsDir)
	}
}

// caPermissionFix returns instructions for fixing CA file permissions.
func caPermissionFix(certsDir string) string {
	switch runtime.GOOS {
	case "windows":
		return fmt.Sprintf(`Cannot write to certificate directory. Check permissions:
       icacls "%s"

       Or run as Administrator`, certsDir)

	default:
		return fmt.Sprintf(`Cannot write to certificate directory. Fix permissions:
       chmod 700 "%s"
       chown $USER "%s"`, certsDir, certsDir)
	}
}

// dbLockedFix returns instructions for fixing database lock issues.
func dbLockedFix(dbPath string) string {
	switch runtime.GOOS {
	case "windows":
		return fmt.Sprintf(`Database is locked by another process. Check for:
       1. Another langley instance running:
          tasklist | findstr langley
          taskkill /IM langley.exe /F

       2. Database viewer with file open:
          Close any SQLite browser tools

       Database: %s`, dbPath)

	default:
		return fmt.Sprintf(`Database is locked by another process. Check for:
       1. Another langley instance running:
          pgrep -f langley
          pkill langley

       2. Database viewer with file open:
          lsof "%s"

       Database: %s`, dbPath, dbPath)
	}
}

// dbPathFix returns instructions for fixing database path issues.
func dbPathFix(dbPath string) string {
	switch runtime.GOOS {
	case "windows":
		return fmt.Sprintf(`Cannot open database. Check the path exists and is writable:
       if not exist "%s" mkdir "%s"

       Or specify a different path:
       set LANGLEY_DB_PATH=C:\Users\%%USERNAME%%\langley.db`, dbPath, dbPath)

	default:
		return fmt.Sprintf(`Cannot open database. Check the path exists and is writable:
       mkdir -p "$(dirname '%s')"
       touch "%s"

       Or specify a different path:
       export LANGLEY_DB_PATH=~/langley.db`, dbPath, dbPath)
	}
}

// configLoadFix returns instructions for fixing config loading issues.
func configLoadFix(configPath string) string {
	if configPath == "" {
		switch runtime.GOOS {
		case "windows":
			return `Config file not found or invalid. Create one:
       langley -listen localhost:9090

       Or check the default location:
       %APPDATA%\langley\langley.yaml`

		default:
			return `Config file not found or invalid. Create one:
       langley -listen localhost:9090

       Or check the default location:
       ~/.config/langley/langley.yaml`
		}
	}
	return fmt.Sprintf(`Config file not found or invalid:
       %s

       Check the file exists and contains valid YAML.
       See 'langley --help' for configuration options.`, configPath)
}

// isDBLocked checks if an error indicates a database lock.
func isDBLocked(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "database is locked") ||
		strings.Contains(errStr, "SQLITE_BUSY") ||
		strings.Contains(errStr, "cannot start a transaction within a transaction")
}

// isPermissionError checks if an error is permission-related.
func isPermissionError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "permission denied") ||
		strings.Contains(errStr, "access is denied") ||
		strings.Contains(errStr, "Access is denied")
}

// isCorruptCert checks if an error indicates a corrupted certificate.
func isCorruptCert(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "failed to decode") ||
		strings.Contains(errStr, "parsing CA certificate") ||
		strings.Contains(errStr, "parsing CA private key") ||
		strings.Contains(errStr, "malformed") ||
		strings.Contains(errStr, "invalid")
}
