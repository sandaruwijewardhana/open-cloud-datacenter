package models

import (
	"time"

	"github.com/google/uuid"
)

// Registry is the persisted representation of a container registry resource.
// Mirrors the registries table.
type Registry struct {
	ID          uuid.UUID      `json:"id"`
	TenantID    string         `json:"tenant_id"`
	TenantUUID  uuid.UUID      `json:"tenant_uuid"`
	ProjectID   string         `json:"project_id"`
	ProjectUUID uuid.UUID      `json:"project_uuid"`
	Name        string         `json:"name"`
	Plan        string         `json:"plan"`
	Status      ResourceStatus `json:"status"`
	Message     string         `json:"message,omitempty"`
	RegistryURL string         `json:"registry_url,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}
