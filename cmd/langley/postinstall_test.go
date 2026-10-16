package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestWritePostInstall_LeadsWithOneCommandWorkflow(t *testing.T) {
	var buf bytes.Buffer
	writePostInstall(&buf)
	out := buf.String()

	runIdx := strings.Index(out, "\n     langley\n")
	exportIdx := strings.Index(out, "export HTTPS_PROXY=")
	if runIdx < 0 {
		t.Fatalf("expected bare 'langley' command in output:\n%s", out)
	}
	if exportIdx >= 0 && exportIdx < runIdx {
		t.Errorf("manual HTTPS_PROXY export should come after the one-command workflow:\n%s", out)
	}
	for _, want := range []string{"http://localhost:9091", "shell", "langley run <command>", "-no-shell", "Firefox"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to mention %q:\n%s", want, out)
		}
	}
}
