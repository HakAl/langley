package main

import (
	"fmt"
	"strings"
)

// formatEnvVars returns copy-paste ready environment variables for the given OS.
// goos should be runtime.GOOS (e.g., "linux", "darwin", "windows").
func formatEnvVars(proxyAddr, caPath, goos string) string {
	var sb strings.Builder
	bundle := bundlePathForCA(caPath)

	sb.WriteString("  Environment variables (copy-paste):\n\n")

	if goos == "windows" {
		// PowerShell syntax
		sb.WriteString("  # Node.js (Claude CLI, Codex)\n")
		fmt.Fprintf(&sb, "  $env:HTTPS_PROXY = \"http://%s\"\n", proxyAddr)
		fmt.Fprintf(&sb, "  $env:HTTP_PROXY = \"http://%s\"\n", proxyAddr)
		fmt.Fprintf(&sb, "  $env:NO_PROXY = \"%s\"\n", noProxyHosts)
		fmt.Fprintf(&sb, "  $env:NODE_EXTRA_CA_CERTS = \"%s\"\n", caPath)
		sb.WriteString("\n")
		sb.WriteString("  # Python (httpx, OpenAI SDK)\n")
		fmt.Fprintf(&sb, "  $env:HTTPS_PROXY = \"http://%s\"\n", proxyAddr)
		fmt.Fprintf(&sb, "  $env:HTTP_PROXY = \"http://%s\"\n", proxyAddr)
		if bundle != "" {
			fmt.Fprintf(&sb, "  $env:SSL_CERT_FILE = \"%s\"\n", bundle)
			sb.WriteString("\n")
			sb.WriteString("  # Python (requests)\n")
			fmt.Fprintf(&sb, "  $env:REQUESTS_CA_BUNDLE = \"%s\"\n", bundle)
		}
	} else {
		// Unix syntax (Linux, macOS, etc.)
		sb.WriteString("  # Node.js (Claude CLI, Codex)\n")
		fmt.Fprintf(&sb, "  export HTTPS_PROXY=http://%s\n", proxyAddr)
		fmt.Fprintf(&sb, "  export HTTP_PROXY=http://%s\n", proxyAddr)
		fmt.Fprintf(&sb, "  export NO_PROXY=%s\n", noProxyHosts)
		fmt.Fprintf(&sb, "  export NODE_EXTRA_CA_CERTS=%s\n", caPath)
		sb.WriteString("\n")
		sb.WriteString("  # Python (httpx, OpenAI SDK)\n")
		fmt.Fprintf(&sb, "  export HTTPS_PROXY=http://%s\n", proxyAddr)
		fmt.Fprintf(&sb, "  export HTTP_PROXY=http://%s\n", proxyAddr)
		if bundle != "" {
			fmt.Fprintf(&sb, "  export SSL_CERT_FILE=%s\n", bundle)
			sb.WriteString("\n")
			sb.WriteString("  # Python (requests)\n")
			fmt.Fprintf(&sb, "  export REQUESTS_CA_BUNDLE=%s\n", bundle)
		}
	}

	sb.WriteString("\n")
	return sb.String()
}
