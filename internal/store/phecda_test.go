package store

import (
	"context"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/google/uuid"
)

func TestPhecdaRepositoryPersistsProjectAndEvidence(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/isc.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := uuid.New()
	var source gen.PhecdaProjectSource
	if err := source.FromPhecdaNonDockerSource(gen.PhecdaNonDockerSource{Mode: gen.Directory, Value: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	project := gen.PhecdaProject{Id: id, Name: "demo", Purpose: "site", Source: source, Evidence: []gen.PhecdaScanEvidence{{File: "index.html", Signal: "static entry point", Confidence: 0.95}}}
	if err := s.Phecda().SaveProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Phecda().GetProject(ctx, id)
	if err != nil || !ok {
		t.Fatalf("get project: ok=%v err=%v", ok, err)
	}
	if got.Name != project.Name || len(got.Evidence) != 1 || got.Evidence[0].File != "index.html" {
		t.Fatalf("unexpected project: %#v", got)
	}
	if ok, err := s.Phecda().DeleteProject(ctx, id); err != nil || !ok {
		t.Fatalf("delete project: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.Phecda().GetProject(ctx, id); err != nil || ok {
		t.Fatalf("deleted project still present: ok=%v err=%v", ok, err)
	}
}
