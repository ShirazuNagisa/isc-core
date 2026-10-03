package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/google/uuid"
	"github.com/oapi-codegen/runtime/types"
)

// Phecda handlers intentionally stop at metadata and read-only detection. Runtime
// downloads and process supervision belong to the Phecda Supervisor boundary.
func (s *Server) ListPhecdaPresets(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.Log, http.StatusOK, "application/json", phecdaCatalog())
}

func (s *Server) ListPhecdaProjects(w http.ResponseWriter, r *http.Request) {
	if s.Phecda != nil {
		items, err := s.Phecda.ListProjects(r.Context())
		if err != nil {
			writeProblem(w, r, s.Log, http.StatusInternalServerError, CodeInternal, "error.internal", "failed to list Phecda projects")
			return
		}
		writeJSON(w, s.Log, http.StatusOK, "application/json", gen.PhecdaProjectList{Items: items})
		return
	}
	s.phecdaMu.RLock()
	defer s.phecdaMu.RUnlock()
	items := make([]gen.PhecdaProject, 0, len(s.phecdaProjects))
	for _, project := range s.phecdaProjects {
		items = append(items, project)
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", gen.PhecdaProjectList{Items: items})
}

func (s *Server) CreatePhecdaProject(w http.ResponseWriter, r *http.Request) {
	var input gen.PhecdaProjectInput
	if r.Body == nil || json.NewDecoder(r.Body).Decode(&input) != nil || strings.TrimSpace(input.Name) == "" {
		writeProblem(w, r, s.Log, http.StatusBadRequest, CodeInvalidRequest, "error.invalid_request", "invalid Phecda project")
		return
	}
	if _, err := input.Source.AsPhecdaDockerSource(); err != nil {
		if _, nonDockerErr := input.Source.AsPhecdaNonDockerSource(); nonDockerErr != nil {
			writeProblem(w, r, s.Log, http.StatusBadRequest, CodeInvalidRequest, "error.invalid_request", "invalid Phecda source")
			return
		}
	}
	id := uuid.New()
	project := gen.PhecdaProject{Id: id, Name: input.Name, Purpose: string(input.Purpose), Source: input.Source, Evidence: []gen.PhecdaScanEvidence{}}
	if s.Phecda != nil {
		if err := s.Phecda.SaveProject(r.Context(), project); err != nil {
			writeProblem(w, r, s.Log, http.StatusInternalServerError, CodeInternal, "error.internal", "failed to save Phecda project")
			return
		}
	} else {
		s.phecdaMu.Lock()
		s.phecdaProjects[id.String()] = project
		s.phecdaMu.Unlock()
	}
	writeJSON(w, s.Log, http.StatusCreated, "application/json", project)
}

func (s *Server) GetPhecdaProject(w http.ResponseWriter, r *http.Request, id types.UUID) {
	if s.Phecda != nil {
		project, ok, err := s.Phecda.GetProject(r.Context(), id)
		if err != nil {
			writeProblem(w, r, s.Log, http.StatusInternalServerError, CodeInternal, "error.internal", "failed to read Phecda project")
			return
		}
		if !ok {
			writeProblem(w, r, s.Log, http.StatusNotFound, CodeNotFound, "error.not_found", "Phecda project not found")
			return
		}
		writeJSON(w, s.Log, http.StatusOK, "application/json", project)
		return
	}
	s.phecdaMu.RLock()
	project, ok := s.phecdaProjects[id.String()]
	s.phecdaMu.RUnlock()
	if !ok {
		writeProblem(w, r, s.Log, http.StatusNotFound, CodeNotFound, "error.not_found", "Phecda project not found")
		return
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", project)
}

func (s *Server) DeletePhecdaProject(w http.ResponseWriter, r *http.Request, id types.UUID) {
	if s.Phecda != nil {
		ok, err := s.Phecda.DeleteProject(r.Context(), id)
		if err != nil {
			writeProblem(w, r, s.Log, http.StatusInternalServerError, CodeInternal, "error.internal", "failed to delete Phecda project")
			return
		}
		if !ok {
			writeProblem(w, r, s.Log, http.StatusNotFound, CodeNotFound, "error.not_found", "Phecda project not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.phecdaMu.Lock()
	_, ok := s.phecdaProjects[id.String()]
	delete(s.phecdaProjects, id.String())
	s.phecdaMu.Unlock()
	if !ok {
		writeProblem(w, r, s.Log, http.StatusNotFound, CodeNotFound, "error.not_found", "Phecda project not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ScanPhecdaProject(w http.ResponseWriter, r *http.Request, id types.UUID) {
	var project gen.PhecdaProject
	var ok bool
	var err error
	if s.Phecda != nil {
		project, ok, err = s.Phecda.GetProject(r.Context(), id)
	} else {
		s.phecdaMu.RLock()
		project, ok = s.phecdaProjects[id.String()]
		s.phecdaMu.RUnlock()
	}
	if err != nil {
		writeProblem(w, r, s.Log, http.StatusInternalServerError, CodeInternal, "error.internal", "failed to read Phecda project")
		return
	}
	if !ok {
		writeProblem(w, r, s.Log, http.StatusNotFound, CodeNotFound, "error.not_found", "Phecda project not found")
		return
	}
	result := gen.PhecdaScanResult{ProjectId: id, ReadOnly: true, Evidence: project.Evidence}
	if docker, err := project.Source.AsPhecdaDockerSource(); err == nil {
		result.Candidates = phecdaDockerPresets()
		if docker.Mode == gen.Image || docker.Mode == gen.Command {
			warning := "Docker image/command sources have no local source tree; review the command before execution."
			result.Warning = &warning
		}
	} else if source, sourceErr := project.Source.AsPhecdaNonDockerSource(); sourceErr == nil && source.Mode == gen.Directory {
		evidence := scanPhecdaDirectory(source.Value)
		result.Evidence = evidence
		result.Candidates = candidatesForEvidence(evidence)
		project.Evidence = evidence
		if s.Phecda != nil {
			if err := s.Phecda.SaveEvidence(r.Context(), id, evidence); err != nil {
				writeProblem(w, r, s.Log, http.StatusInternalServerError, CodeInternal, "error.internal", "failed to save Phecda evidence")
				return
			}
		} else {
			s.phecdaMu.Lock()
			s.phecdaProjects[id.String()] = project
			s.phecdaMu.Unlock()
		}
	} else {
		result.Candidates = phecdaNonDockerPresets()
		warning := "This source cannot be scanned locally; choose a preset and review its commands."
		result.Warning = &warning
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", result)
}

func (s *Server) ListPhecdaDeployments(w http.ResponseWriter, r *http.Request) {
	if s.Phecda != nil {
		items, err := s.Phecda.ListDeployments(r.Context())
		if err != nil {
			writeProblem(w, r, s.Log, http.StatusInternalServerError, CodeInternal, "error.internal", "failed to list Phecda deployments")
			return
		}
		writeJSON(w, s.Log, http.StatusOK, "application/json", gen.PhecdaDeploymentList{Items: items})
		return
	}
	s.phecdaMu.RLock()
	defer s.phecdaMu.RUnlock()
	items := make([]gen.PhecdaDeployment, 0, len(s.phecdaDeployments))
	for _, deployment := range s.phecdaDeployments {
		items = append(items, deployment)
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", gen.PhecdaDeploymentList{Items: items})
}

func (s *Server) CreatePhecdaDeployment(w http.ResponseWriter, r *http.Request) {
	var input gen.PhecdaDeploymentInput
	if r.Body == nil || json.NewDecoder(r.Body).Decode(&input) != nil || input.ProjectId == uuid.Nil || strings.TrimSpace(input.PresetId) == "" {
		writeProblem(w, r, s.Log, http.StatusBadRequest, CodeInvalidRequest, "error.invalid_request", "invalid Phecda deployment")
		return
	}
	deploymentID := input.Id
	if deploymentID == nil {
		value := uuid.New()
		deploymentID = &value
	}
	deployment := gen.PhecdaDeployment{Id: *deploymentID, ProjectId: input.ProjectId, PresetId: input.PresetId, State: gen.PhecdaDeploymentState(input.State), LocalPort: input.LocalPort, LastError: input.LastError, PublicServiceId: input.PublicServiceId}
	if s.Phecda != nil {
		if err := s.Phecda.SaveDeployment(r.Context(), deployment); err != nil {
			writeProblem(w, r, s.Log, http.StatusInternalServerError, CodeInternal, "error.internal", "failed to save Phecda deployment")
			return
		}
	} else {
		s.phecdaMu.Lock()
		s.phecdaDeployments[deployment.Id.String()] = deployment
		s.phecdaMu.Unlock()
	}
	writeJSON(w, s.Log, http.StatusCreated, "application/json", deployment)
}
func (s *Server) GetPhecdaDeployment(w http.ResponseWriter, r *http.Request, id types.UUID) {
	if s.Phecda != nil {
		deployment, ok, err := s.Phecda.GetDeployment(r.Context(), id)
		if err != nil {
			writeProblem(w, r, s.Log, http.StatusInternalServerError, CodeInternal, "error.internal", "failed to read Phecda deployment")
			return
		}
		if !ok {
			writeProblem(w, r, s.Log, http.StatusNotFound, CodeNotFound, "error.not_found", "Phecda deployment not found")
			return
		}
		writeJSON(w, s.Log, http.StatusOK, "application/json", deployment)
		return
	}
	s.phecdaMu.RLock()
	deployment, ok := s.phecdaDeployments[id.String()]
	s.phecdaMu.RUnlock()
	if !ok {
		writeProblem(w, r, s.Log, http.StatusNotFound, CodeNotFound, "error.not_found", "Phecda deployment not found")
		return
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", deployment)
}

func phecdaCatalog() gen.PhecdaPresetCatalog {
	return gen.PhecdaPresetCatalog{NonDocker: phecdaNonDockerPresets(), Docker: phecdaDockerPresets()}
}
func preset(id, title string, runtime gen.PhecdaPresetRuntime, docker bool, port int, files ...string) gen.PhecdaPreset {
	return gen.PhecdaPreset{Id: id, Version: "1", Title: title, Runtime: runtime, DockerOnly: docker, DefaultPort: port, DetectorFiles: files}
}
func phecdaNonDockerPresets() []gen.PhecdaPreset {
	return []gen.PhecdaPreset{preset("static-html", "Static HTML", gen.PhecdaPresetRuntimeStaticFiles, false, 8080, "index.html"), preset("node-auto", "Node.js", gen.PhecdaPresetRuntimeNode, false, 3000, "package.json"), preset("python-auto", "Python", gen.PhecdaPresetRuntimePython, false, 8000, "requirements.txt", "pyproject.toml"), preset("php-composer", "PHP", gen.PhecdaPresetRuntimePhp, false, 8080, "composer.json", "index.php"), preset("go-module", "Go", gen.PhecdaPresetRuntimeGo, false, 8080, "go.mod"), preset("java-build", "Java", gen.PhecdaPresetRuntimeJava, false, 8080, "pom.xml", "build.gradle")}
}
func phecdaDockerPresets() []gen.PhecdaPreset {
	return []gen.PhecdaPreset{preset("docker-compose", "Docker Compose", gen.PhecdaPresetRuntimeDocker, true, 8080, "compose.yaml", "compose.yml", "docker-compose.yml"), preset("dockerfile", "Dockerfile", gen.PhecdaPresetRuntimeDocker, true, 8080, "Dockerfile"), preset("docker-image", "Docker image", gen.PhecdaPresetRuntimeDocker, true, 8080), preset("docker-command", "Docker command", gen.PhecdaPresetRuntimeDocker, true, 8080)}
}

func scanPhecdaDirectory(root string) []gen.PhecdaScanEvidence {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	ignored := map[string]bool{".git": true, ".svn": true, ".hg": true, ".env": true, ".ssh": true, "node_modules": true, "vendor": true, "target": true, "dist": true, "build": true}
	evidence := make([]gen.PhecdaScanEvidence, 0)
	for _, entry := range entries {
		name := entry.Name()
		if ignored[name] {
			continue
		}
		switch name {
		case "index.html":
			evidence = append(evidence, gen.PhecdaScanEvidence{File: name, Signal: "static entry point", Confidence: 0.95})
		case "package.json":
			evidence = append(evidence, gen.PhecdaScanEvidence{File: name, Signal: "Node package manifest", Confidence: 0.95})
		case "requirements.txt", "pyproject.toml":
			evidence = append(evidence, gen.PhecdaScanEvidence{File: name, Signal: "Python dependency manifest", Confidence: 0.9})
		case "composer.json", "index.php":
			evidence = append(evidence, gen.PhecdaScanEvidence{File: name, Signal: "PHP project marker", Confidence: 0.85})
		case "go.mod":
			evidence = append(evidence, gen.PhecdaScanEvidence{File: name, Signal: "Go module", Confidence: 0.95})
		case "pom.xml", "build.gradle":
			evidence = append(evidence, gen.PhecdaScanEvidence{File: name, Signal: "Java build manifest", Confidence: 0.9})
		}
	}
	return evidence
}
func candidatesForEvidence(evidence []gen.PhecdaScanEvidence) []gen.PhecdaPreset {
	names := map[string]bool{}
	for _, e := range evidence {
		names[e.File] = true
	}
	out := []gen.PhecdaPreset{}
	for _, p := range phecdaNonDockerPresets() {
		for _, file := range p.DetectorFiles {
			if names[file] {
				out = append(out, p)
				break
			}
		}
	}
	if len(out) == 0 {
		out = phecdaNonDockerPresets()
	}
	return out
}
