package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
)

const (
	testHarborNS  = "registry-system"
	testCredsName = "harbor-credentials"
	testHarborURL = "https://registry.example.com"
)

// testLogger is a discarding logger for functions that take one.
func testLogger() logr.Logger { return logr.Discard() }

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := registryv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add registry scheme: %v", err)
	}
	return s
}

func newRegistryReconciler(t *testing.T, fc client.WithWatch) *RegistryReconciler {
	return &RegistryReconciler{
		Client:   fc,
		Scheme:   newTestScheme(t),
		Recorder: events.NewFakeRecorder(64),
		HarborCfg: config.HarborConfig{
			URL:               testHarborURL,
			CredentialsSecret: testCredsName,
			Namespace:         testHarborNS,
		},
	}
}

func registryIn(namespace, name string) *registryv1alpha1.Registry {
	return &registryv1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       registryv1alpha1.RegistrySpec{Plan: "starter"},
	}
}

// harborCredsSecret is the Secret the operator authenticates to Harbor with.
func harborCredsSecret(data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testCredsName, Namespace: testHarborNS},
		Data:       data,
	}
}

func newFakeClient(t *testing.T, objs ...client.Object) client.WithWatch {
	return fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithStatusSubresource(&registryv1alpha1.Registry{}).
		WithObjects(objs...).
		Build()
}

// The operator authenticates with credentials from its own namespace, so a
// tenant namespace is never read to reach Harbor.
func TestHarborCredentials_ReadsFromTheOperatorNamespace(t *testing.T) {
	fc := newFakeClient(t, harborCredsSecret(map[string][]byte{
		config.HarborUsernameKey: []byte("robot$system"),
		config.HarborPasswordKey: []byte("s3cret"),
	}))
	r := newRegistryReconciler(t, fc)

	user, pass, err := r.harborCredentials(context.Background())
	if err != nil {
		t.Fatalf("harborCredentials() error = %v", err)
	}
	if user != "robot$system" || pass != "s3cret" {
		t.Errorf("harborCredentials() = %q/%q, want robot$system/s3cret", user, pass)
	}
}

// A malformed or absent Secret must produce a message naming what is wrong.
// Every Registry fails the same way for the same reason, so a vague error here
// costs the same debugging effort once per Registry.
func TestHarborCredentials_ErrorsNameTheProblem(t *testing.T) {
	tests := []struct {
		name    string
		objs    []client.Object
		wantIn  string
	}{
		{
			name:   "secret missing entirely",
			objs:   nil,
			wantIn: testCredsName,
		},
		{
			name:   "username key absent",
			objs:   []client.Object{harborCredsSecret(map[string][]byte{config.HarborPasswordKey: []byte("p")})},
			wantIn: config.HarborUsernameKey,
		},
		{
			name:   "password key absent",
			objs:   []client.Object{harborCredsSecret(map[string][]byte{config.HarborUsernameKey: []byte("u")})},
			wantIn: config.HarborPasswordKey,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRegistryReconciler(t, newFakeClient(t, tt.objs...))
			if _, _, err := r.harborCredentials(context.Background()); err == nil {
				t.Fatal("harborCredentials() error = nil, want an error")
			} else if !strings.Contains(err.Error(), tt.wantIn) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantIn)
			}
		})
	}
}

// Readiness must report a Harbor the operator cannot authenticate to. Failing
// to read credentials is exactly that case, and is reachable without a server.
func TestCheckHarborAccess_ReportsUnusableCredentials(t *testing.T) {
	r := newRegistryReconciler(t, newFakeClient(t))
	if err := r.CheckHarborAccess(context.Background()); err == nil {
		t.Fatal("CheckHarborAccess() error = nil, want an error when the credentials Secret is missing")
	}
}

// The cached result is what keeps a readiness probe polled every few seconds
// from becoming steady load on Harbor.
func TestCheckHarborAccess_ReusesTheCachedResult(t *testing.T) {
	r := newRegistryReconciler(t, newFakeClient(t))

	first := r.CheckHarborAccess(context.Background())
	if first == nil {
		t.Fatal("expected the first check to fail with no credentials Secret")
	}

	// Supplying the Secret now must NOT change the answer until the TTL lapses.
	if err := r.Create(context.Background(), harborCredsSecret(map[string][]byte{
		config.HarborUsernameKey: []byte("u"),
		config.HarborPasswordKey: []byte("p"),
	})); err != nil {
		t.Fatalf("create credentials Secret: %v", err)
	}
	if second := r.CheckHarborAccess(context.Background()); second == nil {
		t.Error("CheckHarborAccess() re-probed within the TTL; the cache is not being used")
	}
}

// Project names are the Registry's own name, which is unique only within one
// namespace. Two Registries in different namespaces resolve to the SAME Harbor
// project — with one shared Harbor that is a cross-tenant collision, and it is
// what project ownership verification has to close.
func TestHarborProjectName_IsNotUniqueAcrossNamespaces(t *testing.T) {
	a := harborProjectName(registryIn("team-a", "web"))
	b := harborProjectName(registryIn("team-b", "web"))
	if a != b {
		t.Fatalf("harborProjectName() = %q and %q; expected both to be %q", a, b, "web")
	}
	if a != "web" {
		t.Errorf("harborProjectName() = %q, want web", a)
	}
}

func TestHarborProjectName_ReservedNamesAreRejected(t *testing.T) {
	if !reservedProjectNames[harborProjectName(registryIn("acme-project-1", "library"))] {
		t.Error(`a Registry named "library" resolves to Harbor's public built-in project and must be refused`)
	}
	if !reservedProjectNames[harborProjectName(registryIn("acme-project-1", "LIBRARY"))] {
		t.Error("the reserved-name check must survive case folding, since harborProjectName lowercases")
	}
	for _, ok := range []string{"web", "api", "libraries", "my-library"} {
		if reservedProjectNames[harborProjectName(registryIn("acme-project-1", ok))] {
			t.Errorf("%q is not reserved and must be allowed", ok)
		}
	}
}

func TestProjectQuotaBytes(t *testing.T) {
	for plan, wantGi := range map[string]int64{"starter": 5, "professional": 20, "enterprise": 100} {
		got, err := projectQuotaBytes(plan)
		if err != nil {
			t.Fatalf("projectQuotaBytes(%q) error = %v", plan, err)
		}
		if want := wantGi * 1024 * 1024 * 1024; got != want {
			t.Errorf("projectQuotaBytes(%q) = %d, want %d", plan, got, want)
		}
	}
	if _, err := projectQuotaBytes("gigantic"); err == nil {
		t.Error("projectQuotaBytes() error = nil, want an unknown plan rejected")
	}
}

// Deleting a Registry that never got a Harbor project must not hang: there is
// nothing to remove.
func TestHandleDelete_RegistryWithoutProjectReleasesImmediately(t *testing.T) {
	reg := registryIn("acme-project-1", "web")
	reg.Finalizers = []string{registryFinalizer}
	reg.DeletionTimestamp = &metav1.Time{Time: time.Now()}

	fc := newFakeClient(t, reg)
	r := newRegistryReconciler(t, fc)

	res, err := r.handleDelete(context.Background(), reg, testLogger())
	if err != nil {
		t.Fatalf("handleDelete() error = %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("handleDelete() requeued after %v for a Registry with no Harbor project", res.RequeueAfter)
	}
	for _, f := range reg.Finalizers {
		if f == registryFinalizer {
			t.Error("finalizer still present; a Registry with no Harbor project has nothing to clean up")
		}
	}
}

// An unreachable Harbor must keep the finalizer rather than release it, or
// images a user asked to destroy are silently left behind.
func TestHandleDelete_KeepsFinalizerWhenHarborIsUnreachable(t *testing.T) {
	reg := registryIn("acme-project-1", "web")
	reg.Finalizers = []string{registryFinalizer}
	reg.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	reg.Status.HarborProject = "web"

	// No credentials Secret, so the Harbor client cannot even be built.
	r := newRegistryReconciler(t, newFakeClient(t, reg))

	res, err := r.handleDelete(context.Background(), reg, testLogger())
	if err == nil {
		t.Fatal("handleDelete() error = nil, want the failure surfaced for backoff")
	}
	if res.RequeueAfter == 0 {
		t.Error("handleDelete() did not requeue; an unreachable Harbor must be retried")
	}
	found := false
	for _, f := range reg.Finalizers {
		if f == registryFinalizer {
			found = true
		}
	}
	if !found {
		t.Error("finalizer was removed while the Harbor project may still exist")
	}
}
