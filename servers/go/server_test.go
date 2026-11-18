package main

import (
	"strings"
	"testing"
)

func TestInfo(t *testing.T) {
	info := Info()
	for _, want := range []string{"git=", " go=go", " date="} {
		if !strings.Contains(info, want) {
			t.Errorf("Info() = %q, missing %q", info, want)
		}
	}
	// Built straight from source (no `make build`) the placeholder must not
	// leak into the output.
	if gitVersion == versionPlaceholder && !strings.HasPrefix(info, "git=dev ") {
		t.Errorf("unstamped build should report git=dev, got %q", info)
	}
	if strings.Contains(info, versionPlaceholder) {
		t.Errorf("Info() exposes the raw placeholder: %q", info)
	}
}
