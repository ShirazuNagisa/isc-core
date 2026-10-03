package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
)

func TestPhecdaDirectoryScanIsReadOnlyAndIgnoresSecrets(t *testing.T) {
	root := t.TempDir()
	for name := range map[string]string{"index.html": "ok", "package.json": "{}", ".env": "SECRET", ".git/config": "private"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(map[string]string{"index.html": "ok", "package.json": "{}", ".env": "SECRET", ".git/config": "private"}[name]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	evidence := scanPhecdaDirectory(root)
	if len(evidence) != 2 {
		t.Fatalf("expected two safe markers, got %d: %#v", len(evidence), evidence)
	}
	candidates := candidatesForEvidence(evidence)
	if len(candidates) < 2 {
		t.Fatalf("expected static and node candidates, got %#v", candidates)
	}
}

func TestPhecdaPresetCatalogKeepsDockerSeparate(t *testing.T) {
	catalog := phecdaCatalog()
	if len(catalog.Docker) == 0 || len(catalog.NonDocker) == 0 {
		t.Fatal("preset catalog groups must both be non-empty")
	}
	for _, preset := range catalog.Docker {
		if !preset.DockerOnly || preset.Runtime != gen.PhecdaPresetRuntimeDocker {
			t.Fatalf("invalid Docker preset: %#v", preset)
		}
	}
	for _, preset := range catalog.NonDocker {
		if preset.DockerOnly || preset.Runtime == gen.PhecdaPresetRuntimeDocker {
			t.Fatalf("Docker preset leaked into non-Docker group: %#v", preset)
		}
	}
}
