package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/wso2/dc-api/internal/models"
)

// ErrRegistryNotFound is returned when the requested registry does not exist.
var ErrRegistryNotFound = errors.New("registry not found")

// CreateRegistry inserts a new Registry row. The unique (project_uuid, name)
// constraint is mapped by the handler to 409 Conflict.
func (r *Repository) CreateRegistry(ctx context.Context, reg *models.Registry) (*models.Registry, error) {
	const q = `
		INSERT INTO registries (tenant_id, tenant_uuid, project_id, project_uuid, name, plan, status, message)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at, updated_at`

	var message *string
	if reg.Message != "" {
		message = &reg.Message
	}
	if err := r.pool.QueryRow(ctx, q,
		reg.TenantID, reg.TenantUUID,
		nilIfEmpty(reg.ProjectID), reg.ProjectUUID,
		reg.Name, reg.Plan, string(reg.Status), message,
	).Scan(&reg.ID, &reg.CreatedAt, &reg.UpdatedAt); err != nil {
		return nil, fmt.Errorf("db create registry: %w", err)
	}
	return reg, nil
}

// GetRegistry fetches a registry row by ID scoped to tenant+project.
// Returns ErrRegistryNotFound on missing rows.
func (r *Repository) GetRegistry(ctx context.Context, id, tenantUUID, projectUUID uuid.UUID) (*models.Registry, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, project_id, project_uuid,
		       name, plan, status, message, registry_url, created_at, updated_at
		FROM   registries
		WHERE  id = $1 AND tenant_uuid = $2 AND project_uuid = $3`

	var reg models.Registry
	var message, registryURL, projectID *string
	err := r.pool.QueryRow(ctx, q, id, tenantUUID, projectUUID).Scan(
		&reg.ID, &reg.TenantID, &reg.TenantUUID,
		&projectID, &reg.ProjectUUID,
		&reg.Name, &reg.Plan, &reg.Status, &message, &registryURL,
		&reg.CreatedAt, &reg.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRegistryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db get registry: %w", err)
	}
	if message != nil {
		reg.Message = *message
	}
	if registryURL != nil {
		reg.RegistryURL = *registryURL
	}
	if projectID != nil {
		reg.ProjectID = *projectID
	}
	return &reg, nil
}

// ListRegistriesByProject returns all registries for a project, newest first.
func (r *Repository) ListRegistriesByProject(ctx context.Context, tenantUUID, projectUUID uuid.UUID) ([]*models.Registry, error) {
	const q = `
		SELECT id, tenant_id, tenant_uuid, project_id, project_uuid,
		       name, plan, status, message, registry_url, created_at, updated_at
		FROM   registries
		WHERE  tenant_uuid = $1 AND project_uuid = $2
		ORDER  BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, tenantUUID, projectUUID)
	if err != nil {
		return nil, fmt.Errorf("db list registries by project: %w", err)
	}
	defer rows.Close()

	var out []*models.Registry
	for rows.Next() {
		var reg models.Registry
		var message, registryURL, projectID *string
		if err := rows.Scan(
			&reg.ID, &reg.TenantID, &reg.TenantUUID,
			&projectID, &reg.ProjectUUID,
			&reg.Name, &reg.Plan, &reg.Status, &message, &registryURL,
			&reg.CreatedAt, &reg.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("db scan registry: %w", err)
		}
		if message != nil {
			reg.Message = *message
		}
		if registryURL != nil {
			reg.RegistryURL = *registryURL
		}
		if projectID != nil {
			reg.ProjectID = *projectID
		}
		out = append(out, &reg)
	}
	return out, rows.Err()
}

// DeleteRegistry removes a registry row by ID. Hard delete — the handler has
// already triggered CR deletion which drives Harbor project teardown.
func (r *Repository) DeleteRegistry(ctx context.Context, id uuid.UUID) error {
	const q = `DELETE FROM registries WHERE id = $1`
	tag, err := r.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("db delete registry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRegistryNotFound
	}
	return nil
}

// UpdateTenantRegistriesPlan sets the plan column on every registry row of the
// tenant. The Harbor backend (and therefore the plan) is shared per tenant, so
// a plan upgrade applies to all of the tenant's registries at once.
func (r *Repository) UpdateTenantRegistriesPlan(ctx context.Context, tenantUUID uuid.UUID, plan string) error {
	const q = `UPDATE registries SET plan = $1, updated_at = now() WHERE tenant_uuid = $2`
	if _, err := r.pool.Exec(ctx, q, plan, tenantUUID); err != nil {
		return fmt.Errorf("db update registries plan: %w", err)
	}
	return nil
}
