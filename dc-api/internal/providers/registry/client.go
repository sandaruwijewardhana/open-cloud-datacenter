// Package registry is the dc-api-side driver for the registry operator's CRDs.
//
// Pattern mirrors KVI (KeyVaultBackend + KeyVaultInstance):
//   RegistryBackend  — one per tenant, in "dc-tenant-<tenantID>" namespace.
//                      Created lazily on first registry create for a tenant.
//                      Drives Harbor Helm deployment via the deploy_worker.
//   RegistryInstance — one per user-named registry (multiple per project allowed),
//                      in "dc-<tenantID>-<projectID>" namespace.
//                      Named reg-<8-char-uuid> (derived from dc-api registries.id).
//                      Drives Harbor project + robot creation via the project_worker.
//
// Backend CR naming:  rb-{tenantID}
// Instance CR naming: reg-{8-char-uuid}
// Credentials Secret: registry-credentials-reg-{8-char-uuid} (in instance namespace)
package registry

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/wso2/dc-api/internal/providers"
	"github.com/wso2/dc-api/internal/providers/common"
)

var (
	registryBackendsGVR = schema.GroupVersionResource{
		Group:    "registry.opencloud.wso2.com",
		Version:  "v1alpha1",
		Resource: "registrybackends",
	}
	registryInstancesGVR = schema.GroupVersionResource{
		Group:    "registry.opencloud.wso2.com",
		Version:  "v1alpha1",
		Resource: "registryinstances",
	}
	secretsGVR = schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "secrets",
	}
)

// Client implements providers.RegistryProvisioner.
type Client struct {
	dyn dynamic.Interface
}

// NewClient builds a registry provisioner backed by the given dynamic client.
func NewClient(dyn dynamic.Interface) *Client {
	return &Client{dyn: dyn}
}

var _ providers.RegistryProvisioner = (*Client)(nil)

// BackendName returns the canonical RegistryBackend CR name for a tenant.
func (c *Client) BackendName(tenantID string) string {
	return "rb-" + tenantID
}

// ─── Backend CR ──────────────────────────────────────────────────────────────

// EnsureRegistryBackend idempotently creates the per-tenant RegistryBackend CR
// in "dc-tenant-<tenantID>" namespace.
func (c *Client) EnsureRegistryBackend(
	ctx context.Context,
	tenantID string,
	tenantUUID interface{ String() string },
	plan string,
) error {
	name := c.BackendName(tenantID)
	ns := common.NamespaceForTenant(tenantID)

	_, err := c.dyn.Resource(registryBackendsGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil // already exists
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get RegistryBackend %s: %w", name, err)
	}

	if plan == "" {
		plan = "starter"
	}
	cr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "registry.opencloud.wso2.com/v1alpha1",
		"kind":       "RegistryBackend",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": ns,
			"labels": map[string]interface{}{
				"dc-api.wso2.com/tenant":        tenantID,
				"dc-api.wso2.com/tenant-uuid":   tenantUUID.String(),
				"dc-api.wso2.com/resource-kind": "registry-backend",
			},
		},
		"spec": map[string]interface{}{
			"tenantID": tenantID,
			"plan":     plan,
		},
	}}

	if _, err := c.dyn.Resource(registryBackendsGVR).Namespace(ns).Create(ctx, cr, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create RegistryBackend %s: %w", name, err)
	}
	return nil
}

// ─── Instance CR ─────────────────────────────────────────────────────────────

// CreateRegistryInstance creates the RegistryInstance CR in the project namespace
// with the BackendRef pointing across to the tenant namespace.
func (c *Client) CreateRegistryInstance(ctx context.Context, req providers.RegistryInstanceCreateRequest) error {
	cr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "registry.opencloud.wso2.com/v1alpha1",
		"kind":       "RegistryInstance",
		"metadata": map[string]interface{}{
			"name":      req.CRName,
			"namespace": req.Namespace,
			"labels":    labelsToInterface(req.Labels),
		},
		"spec": map[string]interface{}{
			"tenantID":     req.TenantID,
			"projectID":    req.ProjectID,
			"plan":         req.Plan,
			"registryName": req.RegistryName,
			"backendRef": map[string]interface{}{
				"name":      req.BackendName,
				"namespace": req.BackendNS,
			},
		},
	}}
	if _, err := c.dyn.Resource(registryInstancesGVR).Namespace(req.Namespace).Create(ctx, cr, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create RegistryInstance %s: %w", req.CRName, err)
	}
	return nil
}

// GetRegistryInstance reads the RegistryInstance CR status.
// Returns (nil, nil) when the CR does not exist.
func (c *Client) GetRegistryInstance(ctx context.Context, namespace, crName string) (*providers.RegistryInstanceStatus, error) {
	obj, err := c.dyn.Resource(registryInstancesGVR).Namespace(namespace).Get(ctx, crName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get RegistryInstance %s: %w", crName, err)
	}

	out := &providers.RegistryInstanceStatus{}
	status, _ := obj.Object["status"].(map[string]interface{})
	if status != nil {
		out.Phase, _ = status["phase"].(string)
		out.RegistryURL, _ = status["registryURL"].(string)
		out.Message, _ = status["message"].(string)
		out.CredentialsSecretName, _ = status["credentialsSecretName"].(string)
		if raw, ok := status["progress"].(map[string]interface{}); ok {
			out.Progress = make(map[string]string, len(raw))
			for k, v := range raw {
				if s, ok := v.(string); ok {
					out.Progress[k] = s
				}
			}
		}
	}
	return out, nil
}

// DeleteRegistryInstance removes the RegistryInstance CR. Idempotent.
func (c *Client) DeleteRegistryInstance(ctx context.Context, namespace, crName string) error {
	err := c.dyn.Resource(registryInstancesGVR).Namespace(namespace).Delete(ctx, crName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete RegistryInstance %s: %w", crName, err)
	}
	return nil
}

// GetRegistryCredentials reads the credentials Secret the provisioner writes
// after Harbor project bootstrap. Returns (nil, nil) when the Secret does not exist.
func (c *Client) GetRegistryCredentials(ctx context.Context, namespace, secretName string) (*providers.RegistryCredentials, error) {
	obj, err := c.dyn.Resource(secretsGVR).Namespace(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get registry credentials secret %s: %w", secretName, err)
	}

	raw, _ := obj.Object["data"].(map[string]interface{})
	decode := func(key string) string {
		v, _ := raw[key]
		switch x := v.(type) {
		case []byte:
			return string(x)
		case string:
			if b, err := base64.StdEncoding.DecodeString(x); err == nil {
				return string(b)
			}
			return x
		}
		return ""
	}

	// firstNonEmpty reads the first key that has a value — tolerating both the
	// operator's Secret keys (robot_secret, project) and the older/richer names
	// (robot_password, harbor_project).
	firstNonEmpty := func(keys ...string) string {
		for _, k := range keys {
			if v := decode(k); v != "" {
				return v
			}
		}
		return ""
	}

	registryURL := decode("registry_url")
	loginServer := decode("login_server")
	if loginServer == "" {
		loginServer = hostFromURL(registryURL)
	}

	return &providers.RegistryCredentials{
		RobotUsername: decode("robot_username"),
		RobotPassword: firstNonEmpty("robot_password", "robot_secret"),
		AdminPassword: decode("admin_password"),
		RegistryURL:   registryURL,
		LoginServer:   loginServer,
		HarborProject: firstNonEmpty("harbor_project", "project"),
		CACert:        decode("ca_cert"),
	}, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// hostFromURL strips the scheme and any path from a registry URL, leaving the
// host[:port] used for `docker login`. E.g. https://registry.acme.example/ ->
// registry.acme.example.
func hostFromURL(u string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexByte(s, '/'); i != -1 {
		s = s[:i]
	}
	return s
}

func labelsToInterface(in map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
