// Package handlers — registry.go
//
// Container Registry — CRUD mirroring the KeyVault handler pattern.
//
// Two operating modes:
//
//  1. Provisioner wired (production):
//       POST   creates a DB row in PENDING, ensures the per-tenant
//              RegistryBackend CR in "dc-tenant-<tenantID>", then creates
//              a per-registry RegistryInstance CR in "dc-<tenantID>-<projectID>".
//              The controller provisions a Harbor project + robot asynchronously;
//              status flips to Ready when the operator reports it.
//       GET    overlays the live CR status onto the DB row.
//       DELETE deletes the CR (finalizer handles Harbor teardown) and
//              drops the DB row immediately.
//
//  2. Provisioner nil (tests / non-Kubernetes deployments):
//       Synchronous DB-only CRUD with status=ACTIVE on create.
//
// Multiple registries per project are supported (unique by name within project).
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers"
	"github.com/wso2/dc-api/internal/providers/common"
	"github.com/wso2/dc-api/internal/rbac"
)

// RegistryHandler handles all .../registries/* routes.
type RegistryHandler struct {
	repo        *db.Repository
	provisioner providers.RegistryProvisioner
	log         zerolog.Logger
}

func NewRegistryHandler(repo *db.Repository, provisioner providers.RegistryProvisioner, log zerolog.Logger) *RegistryHandler {
	return &RegistryHandler{repo: repo, provisioner: provisioner, log: log}
}

// ── DTOs ──────────────────────────────────────────────────────────────────────

type createRegistryRequest struct {
	Name string `json:"name"`
	Plan string `json:"plan,omitempty"`
}

type registryResponse struct {
	ID          string            `json:"id"`
	TenantID    string            `json:"tenant_id"`
	Name        string            `json:"name"`
	Plan        string            `json:"plan"`
	Status      string            `json:"status"`
	Message     string            `json:"message,omitempty"`
	RegistryURL string            `json:"registry_url,omitempty"`
	Progress    map[string]string `json:"progress,omitempty"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

func registryToResponse(reg *models.Registry) registryResponse {
	return registryResponse{
		ID:        reg.ID.String(),
		TenantID:  reg.TenantID,
		Name:      reg.Name,
		Plan:      reg.Plan,
		Status:    string(reg.Status),
		Message:   reg.Message,
		CreatedAt: reg.CreatedAt.Format(time.RFC3339),
		UpdatedAt: reg.UpdatedAt.Format(time.RFC3339),
	}
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// Create handles POST .../registries.
func (h *RegistryHandler) Create(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionRegistryWrite) {
		return
	}

	var req createRegistryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := validateResourceName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Plan == "" {
		req.Plan = "starter"
	}

	projectID, projectUUID, _ := lookupProjectUUID(w, r)

	initialStatus := models.StatusActive
	if h.provisioner != nil {
		initialStatus = models.StatusPending
	}

	reg, err := h.repo.CreateRegistry(r.Context(), &models.Registry{
		TenantID:    tenantID,
		TenantUUID:  tenantUUID,
		ProjectID:   projectID,
		ProjectUUID: projectUUID,
		Name:        req.Name,
		Plan:        req.Plan,
		Status:      initialStatus,
	})
	if err != nil {
		if strings.Contains(err.Error(), "23505") || strings.Contains(err.Error(), "duplicate key") {
			writeError(w, http.StatusConflict, "a registry named '"+req.Name+"' already exists for this project")
			return
		}
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("create registry")
		writeError(w, http.StatusInternalServerError, "failed to create registry")
		return
	}

	if h.provisioner != nil {
		if err := h.driveProvisioner(r.Context(), reg, tenantID, projectID, tenantUUID, projectUUID); err != nil {
			h.log.Warn().Err(err).
				Str("tenant", tenantID).
				Str("registry_id", reg.ID.String()).
				Msg("registry provisioning failed; row left in PENDING")
			reg.Message = "provisioning failed: " + err.Error()
		}
	}

	writeJSON(w, http.StatusCreated, registryToResponse(reg))
}

// driveProvisioner ensures the Backend CR and creates the Instance CR.
func (h *RegistryHandler) driveProvisioner(
	ctx context.Context,
	reg *models.Registry,
	tenantID, projectID string,
	tenantUUID, projectUUID uuid.UUID,
) error {
	if err := h.provisioner.EnsureRegistryBackend(ctx, tenantID, tenantUUID, reg.Plan); err != nil {
		return fmt.Errorf("ensure registry backend: %w", err)
	}

	labels := common.StandardLabels(tenantID, projectID, tenantUUID, projectUUID, reg.ID, "registry", reg.Name)
	crName := common.NamespaceScopedName("reg", reg.ID)
	ns := common.NamespaceForProject(tenantID, projectID)
	return h.provisioner.CreateRegistryInstance(ctx, providers.RegistryInstanceCreateRequest{
		CRName:       crName,
		Namespace:    ns,
		Labels:       labels,
		BackendName:  h.provisioner.BackendName(tenantID),
		BackendNS:    common.NamespaceForTenant(tenantID),
		TenantID:     tenantID,
		ProjectID:    projectID,
		Plan:         reg.Plan,
		RegistryName: reg.Name,
	})
}

// List handles GET .../registries.
func (h *RegistryHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	projectUUID, ok := middleware.ProjectUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "no project UUID in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionRegistryRead) {
		return
	}

	regs, err := h.repo.ListRegistriesByProject(r.Context(), tenantUUID, projectUUID)
	if err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Msg("list registries")
		writeError(w, http.StatusInternalServerError, "failed to list registries")
		return
	}

	out := make([]registryResponse, 0, len(regs))
	for _, reg := range regs {
		resp := registryToResponse(reg)
		if h.provisioner != nil {
			crName := common.NamespaceScopedName("reg", reg.ID)
			ns := common.NamespaceForProject(reg.TenantID, reg.ProjectID)
			st, gerr := h.provisioner.GetRegistryInstance(r.Context(), ns, crName)
			if gerr != nil {
				h.log.Warn().Err(gerr).Str("registry_id", reg.ID.String()).Msg("list: read CR status failed")
			} else if st != nil {
				if phase := mapRegistryPhase(st.Phase); phase != "" {
					resp.Status = phase
				}
				resp.Message = st.Message
				resp.RegistryURL = st.RegistryURL
				resp.Progress = st.Progress
			}
		}
		out = append(out, resp)
	}
	writeJSON(w, http.StatusOK, out)
}

// Get handles GET .../registries/{id}.
func (h *RegistryHandler) Get(w http.ResponseWriter, r *http.Request) {
	_, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	projectUUID, ok := middleware.ProjectUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "no project UUID in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionRegistryRead) {
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid registry id")
		return
	}
	reg, err := h.repo.GetRegistry(r.Context(), id, tenantUUID, projectUUID)
	if errors.Is(err, db.ErrRegistryNotFound) {
		writeError(w, http.StatusNotFound, "registry not found")
		return
	}
	if err != nil {
		h.log.Error().Err(err).Str("id", id.String()).Msg("get registry")
		writeError(w, http.StatusInternalServerError, "failed to fetch registry")
		return
	}

	resp := registryToResponse(reg)
	if h.provisioner != nil {
		crName := common.NamespaceScopedName("reg", reg.ID)
		ns := common.NamespaceForProject(reg.TenantID, reg.ProjectID)
		st, gerr := h.provisioner.GetRegistryInstance(r.Context(), ns, crName)
		if gerr != nil {
			h.log.Warn().Err(gerr).Str("registry_id", reg.ID.String()).Msg("read CR status failed; returning DB view")
		} else if st != nil {
			if phase := mapRegistryPhase(st.Phase); phase != "" {
				resp.Status = phase
			}
			resp.Message = st.Message
			resp.RegistryURL = st.RegistryURL
			resp.Progress = st.Progress
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// Delete handles DELETE .../registries/{id}.
func (h *RegistryHandler) Delete(w http.ResponseWriter, r *http.Request) {
	_, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	projectUUID, ok := middleware.ProjectUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "no project UUID in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionRegistryDelete) {
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid registry id")
		return
	}

	reg, err := h.repo.GetRegistry(r.Context(), id, tenantUUID, projectUUID)
	if errors.Is(err, db.ErrRegistryNotFound) {
		writeError(w, http.StatusNotFound, "registry not found")
		return
	}
	if err != nil {
		h.log.Error().Err(err).Str("id", id.String()).Msg("get registry for delete")
		writeError(w, http.StatusInternalServerError, "failed to fetch registry")
		return
	}

	if h.provisioner != nil {
		crName := common.NamespaceScopedName("reg", reg.ID)
		ns := common.NamespaceForProject(reg.TenantID, reg.ProjectID)
		if err := h.provisioner.DeleteRegistryInstance(r.Context(), ns, crName); err != nil {
			h.log.Error().Err(err).Str("registry_id", reg.ID.String()).Msg("delete registry CR")
			writeError(w, http.StatusInternalServerError, "failed to delete registry from operator")
			return
		}
	}

	if err := h.repo.DeleteRegistry(r.Context(), id); err != nil {
		if errors.Is(err, db.ErrRegistryNotFound) {
			writeError(w, http.StatusNotFound, "registry not found")
			return
		}
		h.log.Error().Err(err).Str("id", id.String()).Msg("delete registry")
		writeError(w, http.StatusInternalServerError, "failed to delete registry")
		return
	}

	_, principalID, _ := middleware.PrincipalFromContext(r.Context())
	h.log.Info().Str("id", id.String()).Str("actor", principalID).Msg("registry delete triggered")

	w.WriteHeader(http.StatusNoContent)
}

// Credentials handles GET .../registries/{id}/credentials.
func (h *RegistryHandler) Credentials(w http.ResponseWriter, r *http.Request) {
	_, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	projectUUID, ok := middleware.ProjectUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "no project UUID in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionRegistryCredentials) {
		return
	}
	if h.provisioner == nil {
		writeError(w, http.StatusNotImplemented, "credentials endpoint requires the registry operator; not available on this deployment")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid registry id")
		return
	}
	reg, err := h.repo.GetRegistry(r.Context(), id, tenantUUID, projectUUID)
	if errors.Is(err, db.ErrRegistryNotFound) {
		writeError(w, http.StatusNotFound, "registry not found")
		return
	}
	if err != nil {
		h.log.Error().Err(err).Str("id", id.String()).Msg("get registry for credentials")
		writeError(w, http.StatusInternalServerError, "failed to fetch registry")
		return
	}

	// Verify live CR is ready before returning credentials.
	crName := common.NamespaceScopedName("reg", reg.ID)
	ns := common.NamespaceForProject(reg.TenantID, reg.ProjectID)
	st, err := h.provisioner.GetRegistryInstance(r.Context(), ns, crName)
	if err != nil {
		h.log.Error().Err(err).Str("registry_id", reg.ID.String()).Msg("read CR for credentials")
		writeError(w, http.StatusInternalServerError, "failed to read registry status")
		return
	}
	if st == nil || st.Phase != "Ready" {
		phase := "not yet provisioned"
		if st != nil && st.Phase != "" {
			phase = st.Phase
		}
		writeError(w, http.StatusConflict,
			"registry is not Ready yet (phase="+phase+
				"); poll GET .../registries/"+reg.ID.String()+" until status=active before requesting credentials")
		return
	}

	// Prefer the Secret name the operator reports in status.credentialsSecretName
	// (authoritative). Fall back to the legacy registry-name convention for
	// compatibility with older operators.
	secretName := st.CredentialsSecretName
	if secretName == "" {
		secretName = "registry-credentials-" + reg.Name
	}
	creds, err := h.provisioner.GetRegistryCredentials(r.Context(), ns, secretName)
	if err != nil {
		h.log.Error().Err(err).Str("registry_id", reg.ID.String()).Msg("read credentials secret")
		writeError(w, http.StatusInternalServerError, "failed to read credentials secret")
		return
	}
	if creds == nil {
		writeError(w, http.StatusConflict, "credentials secret not yet written by the operator — try again shortly")
		return
	}

	// Harbor uses a self-signed ingress cert, so a direct `docker login` must
	// trust the CA. We ship the CA PEM with the credentials (same pattern as the
	// dbaas operator's ca_cert) and per-OS trust instructions so the UI can show
	// end-to-end steps — no kubectl needed on the client. All commands assume the
	// user first saved the CA as a file named "ca.crt" (the UI offers a download).
	host := creds.LoginServer
	var trustLinux, trustMac, trustWindows string
	if creds.CACert != "" {
		trustLinux = "# Docker Engine (Linux): save the CA above as ca.crt, then:\n" +
			"sudo mkdir -p /etc/docker/certs.d/" + host + "\n" +
			"sudo cp ca.crt /etc/docker/certs.d/" + host + "/ca.crt"
		trustMac = "# Docker Desktop (macOS): save the CA above as ca.crt, then:\n" +
			"sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain ca.crt\n" +
			"# then restart Docker Desktop"
		trustWindows = "# Docker Desktop (Windows, PowerShell as Administrator): save the CA above as ca.crt, then:\n" +
			"Import-Certificate -FilePath .\\ca.crt -CertStoreLocation Cert:\\LocalMachine\\Root\n" +
			"# then restart Docker Desktop"
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"tenantId":      reg.TenantID,
		"projectId":     reg.ProjectID,
		"registryId":    reg.ID.String(),
		"registryUrl":   creds.RegistryURL,
		"loginServer":   creds.LoginServer,
		"robotUsername": creds.RobotUsername,
		"robotPassword": creds.RobotPassword,
		"harborProject": creds.HarborProject,
		"caCert":        creds.CACert,
		"quickstart": map[string]string{
			"trustCaLinux":   trustLinux,
			"trustCaMac":     trustMac,
			"trustCaWindows": trustWindows,
			"login":          "docker login " + host + " -u " + creds.RobotUsername,
			"push":           "docker push " + host + "/" + creds.HarborProject + "/IMAGE:TAG",
			"pull":           "docker pull " + host + "/" + creds.HarborProject + "/IMAGE:TAG",
		},
	})
}

// mapRegistryPhase maps the CR status.phase (title case, KVI standard) to the
// resource_status values the UI understands. Returns "" for unknown phases so
// callers can fall back to the DB status.
func mapRegistryPhase(phase string) string {
	switch phase {
	case "Pending", "Provisioning":
		return string(models.StatusPending)
	case "Ready":
		return string(models.StatusActive)
	case "Failed":
		return string(models.StatusFailed)
	case "Terminating":
		return string(models.StatusDeleting)
	}
	return ""
}

// ── Plan upgrade ──────────────────────────────────────────────────────────────

type updateRegistryPlanRequest struct {
	Plan string `json:"plan"`
}

// planRank orders the Harbor resource profiles. Higher = bigger.
var planRank = map[string]int{"starter": 1, "professional": 2, "enterprise": 3}

// UpdatePlan handles PATCH .../registries/{id}/plan. It upgrades the tenant's
// SHARED Harbor backend to a bigger resource profile: the operator reacts to
// the CR change with a pinned-values helm upgrade plus grow-only PVC
// expansion, so existing images and databases are preserved. Downgrades are
// rejected here and — defense in depth — by the RegistryBackend CRD's CEL
// transition rule at admission.
func (h *RegistryHandler) UpdatePlan(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := middleware.TenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant in context")
		return
	}
	tenantUUID, ok := middleware.TenantUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no tenant UUID in context")
		return
	}
	projectUUID, ok := middleware.ProjectUUIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "no project UUID in context")
		return
	}
	if !requireAction(w, r, h.repo, rbac.ActionRegistryWrite) {
		return
	}
	if h.provisioner == nil {
		writeError(w, http.StatusNotImplemented, "registry provisioning is disabled")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid registry id")
		return
	}

	var req updateRegistryPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	newRank, known := planRank[req.Plan]
	if !known {
		writeError(w, http.StatusBadRequest, "unknown plan: valid plans are starter, professional, enterprise")
		return
	}

	// Ownership check: the row must belong to this tenant+project.
	reg, err := h.repo.GetRegistry(r.Context(), id, tenantUUID, projectUUID)
	if errors.Is(err, db.ErrRegistryNotFound) {
		writeError(w, http.StatusNotFound, "registry not found")
		return
	}
	if err != nil {
		h.log.Error().Err(err).Str("id", id.String()).Msg("get registry for plan update")
		writeError(w, http.StatusInternalServerError, "failed to fetch registry")
		return
	}

	if curRank := planRank[reg.Plan]; newRank < curRank {
		writeError(w, http.StatusBadRequest,
			"plan downgrades are not supported: persistent volumes cannot shrink")
		return
	} else if newRank == curRank {
		writeJSON(w, http.StatusOK, registryToResponse(reg)) // idempotent no-op
		return
	}

	// Patch the shared Backend CR; the operator converges asynchronously.
	if err := h.provisioner.UpdateRegistryBackendPlan(r.Context(), tenantID, req.Plan); err != nil {
		h.log.Error().Err(err).Str("tenant", tenantID).Str("plan", req.Plan).Msg("update backend plan")
		writeError(w, http.StatusBadGateway, "failed to update backend plan: "+err.Error())
		return
	}

	// The backend is tenant-shared, so reflect the new plan on every row.
	// Best-effort: the CR is the source of truth; a failed row update only
	// staleness the UI until the next successful write.
	if err := h.repo.UpdateTenantRegistriesPlan(r.Context(), tenantUUID, req.Plan); err != nil {
		h.log.Warn().Err(err).Str("tenant", tenantID).Msg("plan updated on CR but not in DB rows")
	}

	reg.Plan = req.Plan
	writeJSON(w, http.StatusOK, registryToResponse(reg))
}
