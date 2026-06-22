package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/providers"
	"github.com/wso2/dc-api/internal/providers/common"
	registryprovider "github.com/wso2/dc-api/internal/providers/registry"
)

// RegistryRepositoriesHandler handles .../registries/{id}/repositories/* routes.
type RegistryRepositoriesHandler struct {
	repo        *db.Repository
	provisioner providers.RegistryProvisioner
	log         zerolog.Logger
}

func NewRegistryRepositoriesHandler(repo *db.Repository, provisioner providers.RegistryProvisioner, log zerolog.Logger) *RegistryRepositoriesHandler {
	return &RegistryRepositoriesHandler{repo: repo, provisioner: provisioner, log: log}
}

// harborClient resolves the registry by UUID, fetches credentials, and returns a browse client.
func (h *RegistryRepositoriesHandler) harborClient(r *http.Request) (*registryprovider.HarborBrowseClient, string, error) {
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		return nil, "", fmt.Errorf("no tenant UUID in context")
	}
	projectUUID, ok := middleware.ProjectUUIDFromContext(r.Context())
	if !ok {
		return nil, "", fmt.Errorf("no project UUID in context")
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return nil, "", fmt.Errorf("invalid registry id")
	}

	reg, err := h.repo.GetRegistry(r.Context(), id, tenantUUID, projectUUID)
	if errors.Is(err, db.ErrRegistryNotFound) {
		return nil, "", fmt.Errorf("registry not found")
	}
	if err != nil {
		return nil, "", fmt.Errorf("get registry: %w", err)
	}

	// Credentials Secret lives in the per-project namespace and is named after the
	// registry NAME (matching the provisioner worker + registry.go handler).
	ns := common.NamespaceForProject(reg.TenantID, reg.ProjectID)
	secretName := "registry-credentials-" + reg.Name
	creds, err := h.provisioner.GetRegistryCredentials(r.Context(), ns, secretName)
	if err != nil || creds == nil {
		return nil, "", fmt.Errorf("credentials not found")
	}
	return registryprovider.NewHarborBrowseClient(creds.RegistryURL, creds.RobotUsername, creds.RobotPassword), creds.HarborProject, nil
}

// isRegistryReady checks live CR status for the registry identified by URL param {id}.
func (h *RegistryRepositoriesHandler) isRegistryReady(r *http.Request) bool {
	tenantUUID, ok1 := middleware.TenantUUIDFromContext(r.Context())
	projectUUID, ok2 := middleware.ProjectUUIDFromContext(r.Context())
	if !ok1 || !ok2 {
		return false
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return false
	}
	reg, err := h.repo.GetRegistry(r.Context(), id, tenantUUID, projectUUID)
	if err != nil {
		return false
	}
	crName := common.NamespaceScopedName("reg", reg.ID)
	ns := common.NamespaceForProject(reg.TenantID, reg.ProjectID)
	st, err := h.provisioner.GetRegistryInstance(r.Context(), ns, crName)
	return err == nil && st != nil && st.Phase == "Ready"
}

func pageParams(r *http.Request) (int, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if size < 1 || size > 100 {
		size = 25
	}
	return page, size
}

// ListRepositories — GET .../registries/{id}/repositories
func (h *RegistryRepositoriesHandler) ListRepositories(w http.ResponseWriter, r *http.Request) {
	if _, ok := middleware.TenantFromContext(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	if !h.isRegistryReady(r) {
		writeError(w, http.StatusBadRequest, "REGISTRY_NOT_READY")
		return
	}

	client, harborProject, err := h.harborClient(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not load registry credentials")
		return
	}

	page, size := pageParams(r)
	repos, err := client.ListRepositories(r.Context(), harborProject, page, size)
	if err != nil {
		h.log.Error().Err(err).Str("id", chi.URLParam(r, "id")).Msg("list repositories")
		writeError(w, http.StatusBadGateway, "could not reach registry")
		return
	}

	out := make([]map[string]interface{}, 0, len(repos))
	for _, repo := range repos {
		repoShort := strings.TrimPrefix(repo.Name, harborProject+"/")
		out = append(out, map[string]interface{}{
			"name":          repoShort,
			"fullName":      repo.Name,
			"artifactCount": repo.ArtifactCount,
			"pullCount":     repo.PullCount,
			"updatedAt":     repo.UpdateTime,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"repositories": out,
		"page":         page,
		"pageSize":     size,
	})
}

// ListArtifacts — GET .../registries/{id}/repositories/{repo_name}/artifacts
func (h *RegistryRepositoriesHandler) ListArtifacts(w http.ResponseWriter, r *http.Request) {
	if _, ok := middleware.TenantFromContext(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	if !h.isRegistryReady(r) {
		writeError(w, http.StatusBadRequest, "REGISTRY_NOT_READY")
		return
	}
	repoName := chi.URLParam(r, "repo_name")

	client, harborProject, err := h.harborClient(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not load registry credentials")
		return
	}

	page, size := pageParams(r)
	artifacts, err := client.ListArtifacts(r.Context(), harborProject, repoName, page, size)
	if err != nil {
		h.log.Error().Err(err).Str("id", chi.URLParam(r, "id")).Str("repo", repoName).Msg("list artifacts")
		writeError(w, http.StatusBadGateway, "could not reach registry")
		return
	}

	out := make([]map[string]interface{}, 0, len(artifacts))
	for _, a := range artifacts {
		shortDigest := a.Digest
		if len(shortDigest) > 19 {
			shortDigest = shortDigest[:19]
		}
		tags := make([]string, 0, len(a.Tags))
		for _, t := range a.Tags {
			tags = append(tags, t.Name)
		}
		out = append(out, map[string]interface{}{
			"digest":      a.Digest,
			"shortDigest": shortDigest,
			"tags":        tags,
			"size":        a.Size,
			"sizeHuman":   humanBytes(a.Size),
			"pushedAt":    a.PushTime,
			"pulledAt":    a.PullTime,
			"mediaType":   a.MediaType,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"artifacts": out,
		"page":      page,
		"pageSize":  size,
	})
}

// DeleteRepository — DELETE .../registries/{id}/repositories/{repo_name}
func (h *RegistryRepositoriesHandler) DeleteRepository(w http.ResponseWriter, r *http.Request) {
	if _, ok := middleware.TenantFromContext(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	repoName := chi.URLParam(r, "repo_name")

	client, harborProject, err := h.harborClient(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not load registry credentials")
		return
	}

	if err := client.DeleteRepository(r.Context(), harborProject, repoName); err != nil {
		h.log.Error().Err(err).Str("id", chi.URLParam(r, "id")).Str("repo", repoName).Msg("delete repository")
		writeError(w, http.StatusBadGateway, "delete failed")
		return
	}

	_, principalID, _ := middleware.PrincipalFromContext(r.Context())
	h.log.Info().Str("id", chi.URLParam(r, "id")).Str("repo", repoName).Str("actor", principalID).Msg("repository deleted")
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// DeleteArtifact — DELETE .../registries/{id}/repositories/{repo_name}/artifacts/{reference}
func (h *RegistryRepositoriesHandler) DeleteArtifact(w http.ResponseWriter, r *http.Request) {
	if _, ok := middleware.TenantFromContext(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	repoName := chi.URLParam(r, "repo_name")
	reference := chi.URLParam(r, "reference")

	client, harborProject, err := h.harborClient(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "could not load registry credentials")
		return
	}

	if err := client.DeleteArtifact(r.Context(), harborProject, repoName, reference); err != nil {
		h.log.Error().Err(err).Str("id", chi.URLParam(r, "id")).Str("repo", repoName).Str("ref", reference).Msg("delete artifact")
		writeError(w, http.StatusBadGateway, "delete failed")
		return
	}

	_, principalID, _ := middleware.PrincipalFromContext(r.Context())
	h.log.Info().Str("id", chi.URLParam(r, "id")).Str("repo", repoName).Str("ref", reference).Str("actor", principalID).Msg("artifact deleted")
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
