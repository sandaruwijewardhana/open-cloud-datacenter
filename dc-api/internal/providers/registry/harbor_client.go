package registry

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// HarborRepository is one repository inside a Harbor project.
type HarborRepository struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	ArtifactCount int64     `json:"artifact_count"`
	PullCount     int64     `json:"pull_count"`
	UpdateTime    time.Time `json:"update_time"`
}

// HarborArtifact is one image artifact inside a repository.
type HarborArtifact struct {
	Digest      string        `json:"digest"`
	Tags        []HarborTag   `json:"tags"`
	Size        int64         `json:"size"`
	PushTime    time.Time     `json:"push_time"`
	PullTime    time.Time     `json:"pull_time"`
	MediaType   string        `json:"media_type"`
}

// HarborTag is a named tag on an artifact.
type HarborTag struct {
	Name     string    `json:"name"`
	PushTime time.Time `json:"push_time"`
}

// HarborBrowseClient calls Harbor's /api/v2.0 REST endpoints using project-scoped
// robot credentials (not admin). One instance per request — create via NewHarborBrowseClient.
type HarborBrowseClient struct {
	baseURL  string
	username string
	password string
	http     *http.Client
}

// NewHarborBrowseClient creates a client targeting the given Harbor base URL
// (e.g. "https://harbor.tenant.lkdc.io") using robot credentials.
// caCert is the PEM-encoded CA that signed Harbor's TLS cert (from the internal-ca
// ClusterIssuer). Pass nil or empty to use the system CA pool (public certs only).
func NewHarborBrowseClient(baseURL, robotUsername, robotPassword string, caCert []byte) *HarborBrowseClient {
	transport := http.DefaultTransport
	if len(caCert) > 0 {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		pool.AppendCertsFromPEM(caCert)
		transport = &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
		}
	}
	return &HarborBrowseClient{
		baseURL:  baseURL,
		username: robotUsername,
		password: robotPassword,
		http:     &http.Client{Timeout: 15 * time.Second, Transport: transport},
	}
}

// ListRepositories returns all repositories inside the Harbor project.
// page is 1-based; pageSize ≤ 100.
func (c *HarborBrowseClient) ListRepositories(ctx context.Context, harborProject string, page, pageSize int) ([]HarborRepository, error) {
	u := fmt.Sprintf("%s/api/v2.0/projects/%s/repositories?page=%d&page_size=%d&sort=-update_time",
		c.baseURL,
		url.PathEscape(harborProject),
		page,
		pageSize,
	)
	var repos []HarborRepository
	if err := c.get(ctx, u, &repos); err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	return repos, nil
}

// ListArtifacts returns artifacts for one repository.
// repoName must NOT include the project prefix (Harbor stores it as "project/name"
// but the API path uses the bare repo name after the project segment).
func (c *HarborBrowseClient) ListArtifacts(ctx context.Context, harborProject, repoName string, page, pageSize int) ([]HarborArtifact, error) {
	u := fmt.Sprintf("%s/api/v2.0/projects/%s/repositories/%s/artifacts?with_tag=true&with_scan_overview=false&page=%d&page_size=%d",
		c.baseURL,
		url.PathEscape(harborProject),
		url.PathEscape(repoName),
		page,
		pageSize,
	)
	var artifacts []HarborArtifact
	if err := c.get(ctx, u, &artifacts); err != nil {
		return nil, fmt.Errorf("list artifacts: %w", err)
	}
	return artifacts, nil
}

// DeleteRepository deletes an entire repository (all tags + artifacts).
func (c *HarborBrowseClient) DeleteRepository(ctx context.Context, harborProject, repoName string) error {
	u := fmt.Sprintf("%s/api/v2.0/projects/%s/repositories/%s",
		c.baseURL,
		url.PathEscape(harborProject),
		url.PathEscape(repoName),
	)
	return c.delete(ctx, u)
}

// DeleteArtifact deletes a single artifact by digest or tag reference.
func (c *HarborBrowseClient) DeleteArtifact(ctx context.Context, harborProject, repoName, reference string) error {
	u := fmt.Sprintf("%s/api/v2.0/projects/%s/repositories/%s/artifacts/%s",
		c.baseURL,
		url.PathEscape(harborProject),
		url.PathEscape(repoName),
		url.PathEscape(reference),
	)
	return c.delete(ctx, u)
}

func (c *HarborBrowseClient) get(ctx context.Context, rawURL string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("not found: %s", rawURL)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("harbor returned %s for %s", strconv.Itoa(resp.StatusCode), rawURL)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *HarborBrowseClient) delete(ctx context.Context, rawURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, rawURL, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.username, c.password)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil // idempotent
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("harbor delete returned %d for %s", resp.StatusCode, rawURL)
	}
	return nil
}
