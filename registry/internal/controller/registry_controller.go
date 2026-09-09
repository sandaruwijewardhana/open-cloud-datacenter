package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	registryv1alpha1 "github.com/wso2/open-cloud-datacenter/crds/registry/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/config"
	"github.com/wso2/open-cloud-datacenter/crds/registry/internal/harbor"
)

const registryFinalizer = "registry.opencloud.wso2.com/registry-cleanup"

// RegistryReconciler serves one Registry: a project inside the central Harbor,
// with credentials written to a Secret beside the Registry.
//
// The operator does not deploy Harbor. It drives one that already exists,
// located by configuration, so every Registry on every cluster becomes a
// project inside that same Harbor.
type RegistryReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Recorder  events.EventRecorder
	HarborCfg config.HarborConfig

	// accessMu guards the cached VerifyAccess result behind CheckHarborAccess.
	accessMu    sync.Mutex
	accessErr   error
	accessAt    time.Time
}

// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=registry.opencloud.wso2.com,resources=registries/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete

// Reconcile converges one Registry: create its project, quota, robot account,
// and credentials Secret inside the central Harbor.
func (r *RegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cr registryv1alpha1.Registry
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get Registry: %w", err)
	}

	if !cr.DeletionTimestamp.IsZero() {
		return r.handleDelete(ctx, &cr, log)
	}
	if !controllerutil.ContainsFinalizer(&cr, registryFinalizer) {
		controllerutil.AddFinalizer(&cr, registryFinalizer)
		if err := r.Update(ctx, &cr); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 1. Build a client for the central Harbor. Credentials are read every
	// pass, so rotating the Secret takes effect without restarting.
	registryURL := r.HarborCfg.URL
	cli, err := r.harborClient(ctx)
	if err != nil {
		return r.transient(ctx, &cr, "read Harbor credentials", err)
	}

	// 2. Create the Harbor project with this Registry's quota. Creation is
	// idempotent: Harbor answers 409 once the project exists.
	plan := cr.Spec.Plan
	if plan == "" {
		plan = planOrder[0]
	}
	// An unrecognized plan is a spec error, not transient — retrying can't fix it.
	quotaBytes, err := projectQuotaBytes(plan)
	if err != nil {
		return r.fail(ctx, &cr, "resolve plan", err)
	}
	projectName := harborProjectName(&cr)
	// Harbor's own built-in project is a name a user could plausibly pick, and
	// adopting it would be silent (see reservedProjectNames). Terminal, not
	// transient: only renaming the Registry can resolve it.
	if reservedProjectNames[projectName] {
		return r.fail(ctx, &cr, "resolve Harbor project",
			fmt.Errorf("%q is a Harbor built-in project name; rename this Registry", projectName))
	}
	if err := cli.CreateHarborProject(ctx, projectName, quotaBytes); err != nil {
		return r.transient(ctx, &cr, "create Harbor project", err)
	}

	// 2b. Converge the quota every reconcile — this is how a plan change takes
	// effect, and it doubles as drift detection.
	proj, err := cli.GetProject(ctx, projectName)
	if err != nil {
		return r.transient(ctx, &cr, "get Harbor project", err)
	}
	if proj.ProjectID == 0 {
		return r.transient(ctx, &cr, "get Harbor project", fmt.Errorf("Harbor returned a project with no project_id for %q", projectName))
	}
	if err := cli.EnsureProjectQuota(ctx, proj.ProjectID, quotaBytes); err != nil {
		// Transient, not terminal — a quota-below-usage rejection can resolve
		// once images are removed or the plan changes again.
		return r.transient(ctx, &cr, "set project quota", err)
	}

	// 3. Mint the robot account once and keep its credentials in a Secret.
	credName := credentialsSecretName(&cr)
	if err := r.ensureCredentials(ctx, &cr, cli, projectName, registryURL, credName); err != nil {
		return r.transient(ctx, &cr, "provision credentials", err)
	}

	// 4. Ready.
	if err := r.patchStatus(ctx, req.NamespacedName, func(s *registryv1alpha1.RegistryStatus) {
		s.Phase = phaseReady
		s.ObservedGeneration = cr.Generation
		s.HarborProject = projectName
		s.RegistryURL = registryURL
		s.CredentialsSecretName = credName
		s.Message = fmt.Sprintf("registry %q ready", projectName)
		setReady(&s.Conditions, cr.Generation, metav1.ConditionTrue, reasonReady, "registry ready")
	}); err != nil {
		return ctrl.Result{}, err
	}
	r.Recorder.Eventf(&cr, nil, corev1.EventTypeNormal, reasonReady, actionProvision,
		"registry ready; credentials in Secret %s", credName)

	// Steady-state: re-check for drift (project deleted out-of-band, etc.).
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// ensureCredentials mints a project robot account only if the credentials
// Secret does not already exist, then writes an owned Secret. The Secret's
// presence is what makes this once-only: re-minting would invalidate
// credentials already in use. When it is absent, any robot from a previous
// half-finished attempt is unusable — its secret was never stored — so
// EnsureProjectRobotAccount replaces it rather than failing against it.
func (r *RegistryReconciler) ensureCredentials(ctx context.Context, cr *registryv1alpha1.Registry, cli *harbor.Client, projectName, registryURL, credName string) error {
	key := client.ObjectKey{Namespace: cr.Namespace, Name: credName}
	var existing corev1.Secret
	if err := r.Get(ctx, key, &existing); err == nil {
		return nil // already provisioned
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	robot, err := cli.EnsureProjectRobotAccount(ctx, projectName, robotAccountName(cr))
	if err != nil {
		return fmt.Errorf("create robot account: %w", err)
	}

	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: credName, Namespace: cr.Namespace},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"robot_username": []byte(robot.Name),
			"robot_secret":   []byte(robot.Secret),
			"registry_url":   []byte(registryURL),
			"project":        []byte(projectName),
			"robot_id":       []byte(fmt.Sprintf("%d", robot.ID)),
		},
	}
	if err := controllerutil.SetControllerReference(cr, sec, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, sec); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// handleDelete runs the Registry finalizer: the Harbor project and every image
// in it are removed. There is no retain option — deleting a Registry means
// deleting the registry.
//
// The credentials Secret is garbage-collected through its owner reference.
func (r *RegistryReconciler) handleDelete(ctx context.Context, cr *registryv1alpha1.Registry, log logr.Logger) (ctrl.Result, error) {
	// No finalizer means cleanup already finished and this is the last pass.
	if !controllerutil.ContainsFinalizer(cr, registryFinalizer) {
		return ctrl.Result{}, nil
	}

	// Keep the finalizer until the project is really gone, so a Harbor that is
	// merely unreachable cannot leave images behind that the user asked to
	// destroy. Every error retries: deleteHarborProject signals "nothing to
	// clean up" by returning nil, so an error here always means the project may
	// still exist.
	if err := r.deleteHarborProject(ctx, cr, log); err != nil {
		r.Recorder.Eventf(cr, nil, corev1.EventTypeWarning, reasonTransient, actionDelete,
			"waiting to delete Harbor project: %s", err.Error())
		return ctrl.Result{RequeueAfter: 15 * time.Second}, err
	}

	controllerutil.RemoveFinalizer(cr, registryFinalizer)
	if err := r.Update(ctx, cr); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// deleteHarborProject removes the Harbor project backing this Registry, along
// with every repository inside it.
//
// status.harborProject is the gate: it is written only once the project really
// exists in Harbor, so an empty value means nothing was created and there is
// nothing to reclaim.
func (r *RegistryReconciler) deleteHarborProject(ctx context.Context, cr *registryv1alpha1.Registry, log logr.Logger) error {
	projectName := cr.Status.HarborProject
	if projectName == "" {
		return nil
	}

	cli, err := r.harborClient(ctx)
	if err != nil {
		return err
	}

	// Harbor refuses to delete a project that still holds repositories (412),
	// so empty it first. Without this the finalizer retries that 412 forever and
	// the Registry never leaves Terminating.
	repos, err := cli.ListRepositories(ctx, projectName)
	if err != nil {
		return fmt.Errorf("list repositories in %s: %w", projectName, err)
	}
	for _, repo := range repos {
		log.Info("deleting Harbor repository", "project", projectName, "repository", repo)
		if err := cli.DeleteRepository(ctx, projectName, repo); err != nil {
			return fmt.Errorf("delete repository %s/%s: %w", projectName, repo, err)
		}
	}

	log.Info("deleting Harbor project", "project", projectName, "repositories", len(repos))
	if err := cli.DeleteProject(ctx, projectName); err != nil {
		return fmt.Errorf("delete Harbor project: %w", err)
	}
	return nil
}

// harborCredentials reads the operator's Harbor credentials.
//
// The Secret lives in the operator's own namespace, so authenticating never
// requires reading a tenant's namespace. Reading it per reconcile rather than
// caching at startup is what lets a rotated Secret take effect without a
// restart.
func (r *RegistryReconciler) harborCredentials(ctx context.Context) (username, password string, err error) {
	key := client.ObjectKey{Namespace: r.HarborCfg.Namespace, Name: r.HarborCfg.CredentialsSecret}
	var sec corev1.Secret
	if err := r.Get(ctx, key, &sec); err != nil {
		return "", "", fmt.Errorf("get Harbor credentials Secret %s: %w", key, err)
	}
	u, ok := sec.Data[config.HarborUsernameKey]
	if !ok {
		return "", "", fmt.Errorf("key %q not found in Secret %s", config.HarborUsernameKey, key)
	}
	pw, ok := sec.Data[config.HarborPasswordKey]
	if !ok {
		return "", "", fmt.Errorf("key %q not found in Secret %s", config.HarborPasswordKey, key)
	}
	return string(u), string(pw), nil
}

// harborClient returns a client for the central Harbor.
func (r *RegistryReconciler) harborClient(ctx context.Context) (*harbor.Client, error) {
	username, password, err := r.harborCredentials(ctx)
	if err != nil {
		return nil, err
	}
	return harbor.NewClient(r.HarborCfg.URL, username, password), nil
}

// accessTTL is how long CheckHarborAccess reuses a result. Readiness is polled
// every few seconds, so without it every probe would become a request to
// Harbor — turning a health check into load on the thing it checks.
const accessTTL = 30 * time.Second

// CheckHarborAccess reports whether the central Harbor is reachable and accepts
// the operator's credentials, caching the answer for accessTTL.
//
// It backs the manager's readiness endpoint, so a Harbor the operator cannot
// use shows up as a pod that is not Ready — visible without reading logs. It
// deliberately does not affect liveness: retrying is correct behaviour, and
// restarting the operator would fix nothing.
func (r *RegistryReconciler) CheckHarborAccess(ctx context.Context) error {
	r.accessMu.Lock()
	defer r.accessMu.Unlock()

	if !r.accessAt.IsZero() && time.Since(r.accessAt) < accessTTL {
		return r.accessErr
	}

	cli, err := r.harborClient(ctx)
	if err != nil {
		r.accessErr, r.accessAt = err, time.Now()
		return r.accessErr
	}
	if err := cli.VerifyAccess(ctx); err != nil {
		r.accessErr = fmt.Errorf("Harbor at %s did not accept the configured credentials: %w", r.HarborCfg.URL, err)
	} else {
		r.accessErr = nil
	}
	r.accessAt = time.Now()
	return r.accessErr
}

// --- naming ---

// reservedProjectNames are Harbor project names a Registry must never resolve
// to. Harbor's built-in "library" project is PUBLIC, and CreateHarborProject
// treats 409 as success so creation is idempotent — so a Registry named
// "library" would bind straight to it, mint a push robot against it, and
// report Ready while publishing its images world-readable.
var reservedProjectNames = map[string]bool{"library": true}

// harborProjectName returns the Harbor project for a Registry: its own name.
//
// A Registry's name is a DNS label, which always satisfies Harbor's project
// naming rules.
//
// It is NOT unique across the central Harbor: two Registries in different
// namespaces, or on different clusters, resolve to the same project name.
// CreateHarborProject still treats 409 as success, so today that silently
// shares one tenant's project with another. Ownership verification is what
// closes it.
func harborProjectName(cr *registryv1alpha1.Registry) string {
	return strings.ToLower(cr.Name)
}

// credentialsSecretName returns the Secret holding this Registry's robot credentials.
func credentialsSecretName(cr *registryv1alpha1.Registry) string {
	return "registry-credentials-" + cr.Name
}

// robotAccountName returns the robot account name for a Registry. Harbor
// prefixes it with "robot$<project>+", so the suffix is kept short.
func robotAccountName(cr *registryv1alpha1.Registry) string {
	s := strings.ToLower(cr.Name)
	if len(s) > 12 {
		s = s[:12]
	}
	return "ci-" + s
}

// projectQuotaGi maps a plan to a registry's Harbor storage quota in gibibytes.
// This is separate from the backend's deployment sizing.
var projectQuotaGi = map[string]int64{
	"starter":      5,
	"professional": 20,
	"enterprise":   100,
}

// projectQuotaBytes resolves a plan to a storage quota in bytes. projectQuotaGi
// is the single source of truth for which plan names are valid.
func projectQuotaBytes(plan string) (int64, error) {
	gi, ok := projectQuotaGi[plan]
	if !ok {
		return 0, fmt.Errorf("unknown plan %q; valid: starter, professional, enterprise", plan)
	}
	return gi * 1024 * 1024 * 1024, nil
}

// --- status helpers ---

// patchStatus applies mutate to the latest status, retrying once on conflict.
func (r *RegistryReconciler) patchStatus(ctx context.Context, key client.ObjectKey, mutate func(*registryv1alpha1.RegistryStatus)) error {
	for attempt := 0; attempt < 2; attempt++ {
		var fresh registryv1alpha1.Registry
		if err := r.Get(ctx, key, &fresh); err != nil {
			return err
		}
		mutate(&fresh.Status)
		if err := r.Status().Update(ctx, &fresh); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("status update: too many conflicts")
}

// provisioning records a wait state and requeues after the given delay.
func (r *RegistryReconciler) provisioning(ctx context.Context, cr *registryv1alpha1.Registry, msg string, after time.Duration) (ctrl.Result, error) {
	err := r.patchStatus(ctx, client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryStatus) {
		s.Phase = phaseProvisioning
		s.Message = msg
		setReady(&s.Conditions, cr.Generation, metav1.ConditionFalse, reasonProvisioning, msg)
	})
	return ctrl.Result{RequeueAfter: after}, err
}

// transient records a retryable failure and returns the error for backoff.
func (r *RegistryReconciler) transient(ctx context.Context, cr *registryv1alpha1.Registry, step string, cause error) (ctrl.Result, error) {
	msg := fmt.Sprintf("%s: %v", step, cause)
	r.Recorder.Eventf(cr, nil, corev1.EventTypeWarning, reasonTransient, actionReconcile, "%s", msg)
	_ = r.patchStatus(ctx, client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryStatus) {
		s.Phase = phaseProvisioning
		s.Message = msg
		setReady(&s.Conditions, cr.Generation, metav1.ConditionFalse, reasonTransient, msg)
	})
	return ctrl.Result{}, cause
}

// fail marks a spec error that retrying cannot resolve: sets Failed and stops
// requeueing. Only for causes traceable to the spec, since editing it
// re-triggers reconcile through the watch.
func (r *RegistryReconciler) fail(ctx context.Context, cr *registryv1alpha1.Registry, step string, cause error) (ctrl.Result, error) {
	msg := fmt.Sprintf("%s: %v", step, cause)
	r.Recorder.Eventf(cr, nil, corev1.EventTypeWarning, reasonError, actionReconcile, "%s", msg)
	_ = r.patchStatus(ctx, client.ObjectKeyFromObject(cr), func(s *registryv1alpha1.RegistryStatus) {
		s.Phase = phaseFailed
		s.Message = msg
		setReady(&s.Conditions, cr.Generation, metav1.ConditionFalse, reasonError, msg)
	})
	return ctrl.Result{}, reconcile.TerminalError(cause)
}

// SetupWithManager registers the controller with the manager.
func (r *RegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&registryv1alpha1.Registry{}).
		Owns(&corev1.Secret{}).
		Named("registry").
		Complete(r)
}
