/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"maps"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

const (
	// Default values
	defaultImage          = "ghcr.io/home-assistant/home-assistant"
	defaultVersion        = "stable"
	defaultPort           = 8123
	defaultStorageSize    = "5Gi"
	defaultTimezone       = "UTC"
	defaultInitRepository = "docker.io/library"
	defaultInitImage      = "busybox"
	defaultInitTag        = "1.36"

	// operatorIPConfigMapSuffix is appended to the HA name to form the ConfigMap
	// that holds the operator's current pod IP for the unban init-container.
	// Using a ConfigMap (instead of embedding the IP directly in the pod template)
	// prevents StatefulSet rollouts every time the operator pod restarts with a new IP.
	operatorIPConfigMapSuffix = "-operator-ip"
	operatorIPConfigMapKey    = "ip"

	// Labels
	labelAppName      = "app.kubernetes.io/name"
	labelAppInstance  = "app.kubernetes.io/instance"
	labelAppManagedBy = "app.kubernetes.io/managed-by"

	// Condition types
	conditionTypeReady           = "Ready"
	conditionTypeBanRecovery     = "BanRecoveryFailed"
	conditionTypeDevicesReady    = "DevicesReady"
	conditionTypeSchedulingReady = "SchedulingReady"

	// DevicesReady condition reasons
	reasonNoDevicesDeclared = "NoDevicesDeclared"
	reasonDevicesMounted    = "DevicesMounted"
	reasonDeviceUnavailable = "DeviceUnavailable"
	reasonDevicesPending    = "Pending"

	// SchedulingReady condition reasons
	reasonNoConstraintsDeclared = "NoConstraintsDeclared"
	reasonScheduled             = "Scheduled"
	reasonUnschedulable         = "Unschedulable"
	reasonSchedulingPending     = "Pending"

	// failedMountEventReason is the Kubernetes Event reason emitted by the
	// kubelet when a volume (including a spec.alpha.devices hostPath) fails
	// to mount.
	failedMountEventReason = "FailedMount"

	// Ban-recovery sliding window: at most banRestartMaxCount pod restarts within
	// banRestartWindow before the operator stops retrying and requires manual action.
	// 3 restarts × ~60 s HA startup ≈ 3 min of automated recovery per 30-min window.
	banRestartMaxCount = 3
	banRestartWindow   = 30 * time.Minute

	// selfUnbanCooldown is the minimum wait between consecutive ban-recovery restarts.
	selfUnbanCooldown    = 5 * time.Minute
	selfUnbanRequeueWait = 2 * time.Minute

	// Condition reasons for ban-recovery
	reasonBanRecoveryLimitExceeded = "RestartLimitExceeded"
	reasonBanRecoveryInProgress    = "RecoveryInProgress"
)

// charDeviceHostPathType makes the kubelet itself verify a spec.alpha.devices
// hostPath is actually a character device before starting the pod, on top of
// the validating webhook's own path checks.
var charDeviceHostPathType = corev1.HostPathCharDev

// HomeAssistantReconciler reconciles a HomeAssistant object
type HomeAssistantReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder

	// APIReader bypasses the informer cache for reads that must not pin an
	// unbounded, high-churn resource (Events) in memory just to serve an
	// occasional lookup. See buildDevicesReadyCondition.
	APIReader client.Reader

	// NewHAClient overrides the default haclient constructor (for testing)
	NewHAClient func(baseURL string) *haclient.Client

	// lastConfigHashSync tracks last time config hash was synced from ConfigMap
	// Used for debouncing to avoid rapid StatefulSet updates
	// sync.Map is used because reconcilers run concurrently for different resources
	lastConfigHashSync sync.Map // map[string]time.Time

	// cert-manager availability detection cache (guarded by certMgrMu). This is
	// a pure optimization — its loss is safely recovered by the next reconcile
	// (constitution principle IV). See tls_helpers.go.
	certMgrMu        sync.Mutex
	certMgrAvailable bool
	certMgrCheckedAt time.Time
}

// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistants,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistants/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistants/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;delete
// Read-only, namespaced: lets the operator surface *why* a pod isn't
// starting (e.g. a FailedMount event for a spec.alpha.devices entry) on
// HomeAssistant status, instead of requiring the user to inspect raw pod
// events themselves.
// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// TLS / cert-manager integration. Narrow verbs on specific resources — the
// operator issues certificates and manages exposure resources; it never installs
// cert-manager or manages GatewayClass.
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes;gateways,verbs=get;list;watch;create;update;patch;delete
// Webhook serving certificate self-management (cert-controller): the operator
// injects the CA bundle into its own ValidatingWebhookConfiguration.
// +kubebuilder:rbac:groups=admissionregistration.k8s.io,resources=validatingwebhookconfigurations,verbs=get;list;watch;update;patch
// Read-only, cluster-scoped: used by the validating webhook (not this
// reconciler) to reject a spec.scheduling.priorityClassName referencing a
// PriorityClass that doesn't exist, at admission time rather than letting it
// fail later as an opaque StatefulSet/Pod creation error.
// +kubebuilder:rbac:groups=scheduling.k8s.io,resources=priorityclasses,verbs=get

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *HomeAssistantReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the HomeAssistant instance
	ha := &hav1.HomeAssistant{}
	if err := r.Get(ctx, req.NamespacedName, ha); err != nil {
		if errors.IsNotFound(err) {
			log.Info("HomeAssistant resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get HomeAssistant")
		return ctrl.Result{}, err
	}

	// Set initial status if not set
	if ha.Status.Phase == "" {
		if err := r.updateHAStatusWithRetry(ctx, ha, func(h *hav1.HomeAssistant) bool {
			if h.Status.Phase != "" {
				return false
			}
			h.Status.Phase = hav1.PhasePending
			return true
		}); err != nil {
			log.Error(err, "Failed to update HomeAssistant status")
			return ctrl.Result{}, err
		}
	}

	// Validate that HomeAssistantConfiguration exists
	// This is REQUIRED for v0.3.0+ architecture
	generatedConfigMapName, err := r.getGeneratedConfigMapName(ctx, ha)
	if err != nil {
		log.Error(err, "Failed to check for HomeAssistantConfiguration")
		return r.updateStatusFailed(ctx, ha, fmt.Errorf("failed to validate HomeAssistantConfiguration: %w", err))
	}
	if generatedConfigMapName == "" {
		// HomeAssistantConfiguration doesn't exist yet - requeue to wait for it
		log.Info("Waiting for HomeAssistantConfiguration to be created",
			"homeassistant", ha.Name,
			"namespace", ha.Namespace,
			"expected-haconfig", ha.Name+"-config")
		if err := r.updateHAStatusWithRetry(ctx, ha, func(h *hav1.HomeAssistant) bool {
			h.Status.Phase = hav1.PhasePending
			h.Status.Ready = false
			meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{
				Type:               conditionTypeReady,
				Status:             metav1.ConditionFalse,
				ObservedGeneration: h.Generation,
				Reason:             "WaitingForConfiguration",
				Message:            "Waiting for HomeAssistantConfiguration to be created",
			})
			return true
		}); err != nil {
			log.Error(err, "Failed to update status")
		}
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}

	// Reconcile PVC
	if err := r.reconcilePVC(ctx, ha); err != nil {
		log.Error(err, "Failed to reconcile PVC")
		return r.updateStatusFailed(ctx, ha, err)
	}

	// Reconcile operator-IP ConfigMap (must run before StatefulSet so the
	// ConfigMap exists when the init-container spec references it).
	if err := r.reconcileOperatorIPConfigMap(ctx, ha); err != nil {
		log.Error(err, "Failed to reconcile operator-IP ConfigMap")
		return r.updateStatusFailed(ctx, ha, err)
	}

	// Reconcile StatefulSet
	if err := r.reconcileStatefulSet(ctx, ha); err != nil {
		log.Error(err, "Failed to reconcile StatefulSet")
		return r.updateStatusFailed(ctx, ha, err)
	}

	// Reconcile Service
	if err := r.reconcileService(ctx, ha); err != nil {
		log.Error(err, "Failed to reconcile Service")
		return r.updateStatusFailed(ctx, ha, err)
	}

	// Reconcile NetworkPolicy (alpha, opt-in via spec.alpha.networkPolicy.enabled)
	if err := r.reconcileNetworkPolicy(ctx, ha); err != nil {
		log.Error(err, "Failed to reconcile NetworkPolicy")
		return r.updateStatusFailed(ctx, ha, err)
	}

	// Transitional: clean up after the removed native TLS feature (spec.alpha.tls)
	// before reconcileTLS, so a stale CertManagerAvailable is dropped before the
	// edge-TLS gate below may legitimately re-add it. Best-effort; never blocks.
	if err := r.reconcileNativeTLSRemoval(ctx, ha); err != nil {
		log.Error(err, "Failed to clean up removed native TLS state")
	}

	// Reconcile TLS / cert-manager integration (opt-in). Missing cert-manager is
	// never an error: it degrades to a status condition + requeue so the rest of
	// the reconcile (and other resources) keep working. The requeue itself is
	// deferred until after reconcileExposure so HTTP-only exposure still gets
	// reconciled while TLS is waiting (e.g. for cert-manager to appear).
	tlsResult, err := r.reconcileTLS(ctx, ha)
	if err != nil {
		log.Error(err, "Failed to reconcile TLS")
		return r.updateStatusFailed(ctx, ha, err)
	}

	// Reconcile exposure (Ingress / Gateway API). Best-effort on the cert-manager
	// side: HTTP exposure works even without cert-manager.
	if err := r.reconcileExposure(ctx, ha); err != nil {
		log.Error(err, "Failed to reconcile exposure")
		return r.updateStatusFailed(ctx, ha, err)
	}

	if tlsResult.RequeueAfter > 0 {
		return tlsResult, nil
	}

	// Publish SchedulingReady before reconcileBootstrap, since an unschedulable
	// pod (PodScheduled=False) never becomes healthy — bootstrap's own health
	// check would otherwise requeue indefinitely below without this reconcile
	// ever reaching updateStatusFromStatefulSet, hiding the reason from status.
	if err := r.publishSchedulingReadyEarly(ctx, ha); err != nil {
		log.Error(err, "Failed to publish early SchedulingReady condition")
	}

	// Reconcile Bootstrap - let bootstrap controller decide when HA is ready
	// Bootstrap has its own health check and will requeue if HA is not ready yet
	result, err := r.reconcileBootstrap(ctx, ha)
	if err != nil {
		log.Error(err, "Failed to reconcile Bootstrap")
		// Return result - may include RequeueAfter
		return result, err
	}
	// If bootstrap needs requeue, honor it
	if result.RequeueAfter > 0 {
		return result, nil
	}

	// Reconcile Backup configuration (requires bootstrap token)
	result, err = r.reconcileBackupConfig(ctx, ha)
	if err != nil {
		log.Error(err, "Failed to reconcile Backup config")
		return result, err
	}
	if result.RequeueAfter > 0 {
		return result, nil
	}

	// Update status based on StatefulSet status
	statusResult, err := r.updateStatusFromStatefulSet(ctx, ha)
	if err != nil || statusResult.RequeueAfter > 0 {
		return statusResult, err
	}

	// When bootstrap is complete, requeue at a bounded interval so that the
	// post-bootstrap ban-detection health check in reconcileBootstrap fires
	// periodically even when the cluster is otherwise idle.
	if ha.Status.Bootstrap != nil && ha.Status.Bootstrap.Completed {
		return ctrl.Result{RequeueAfter: banDetectionInterval}, nil
	}
	return statusResult, nil
}

// reconcileOperatorIPConfigMap keeps a ConfigMap <ha-name>-operator-ip up to date
// with the operator pod's current IP. The unban init-container reads the IP from
// this ConfigMap via an env var — decoupling the StatefulSet template from the
// operator's pod IP so that operator restarts don't trigger HA rolling restarts.
// When POD_IP is not set (e.g. in local dev), this is a no-op.
func (r *HomeAssistantReconciler) reconcileOperatorIPConfigMap(ctx context.Context, ha *hav1.HomeAssistant) error {
	podIP := os.Getenv("POD_IP")
	if podIP == "" {
		return nil
	}

	log := logf.FromContext(ctx)
	cmName := ha.Name + operatorIPConfigMapSuffix

	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: ha.Namespace,
			Labels: map[string]string{
				labelAppName:      "homeassistant",
				labelAppInstance:  ha.Name,
				labelAppManagedBy: "homeassistant-operator",
			},
		},
		Data: map[string]string{
			operatorIPConfigMapKey: podIP,
		},
	}
	if err := controllerutil.SetControllerReference(ha, desired, r.Scheme); err != nil {
		return fmt.Errorf("set owner reference on operator-IP ConfigMap: %w", err)
	}

	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: ha.Namespace}, existing)
	if errors.IsNotFound(err) {
		log.Info("Creating operator-IP ConfigMap", "configmap", cmName, "ip", podIP)
		return r.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("get operator-IP ConfigMap: %w", err)
	}

	if existing.Data[operatorIPConfigMapKey] != podIP {
		log.Info("Updating operator-IP ConfigMap",
			"configmap", cmName,
			"oldIP", existing.Data[operatorIPConfigMapKey],
			"newIP", podIP)
		existing.Data = desired.Data
		return r.Update(ctx, existing)
	}
	return nil
}

// reconcilePVC ensures the PVC exists for Home Assistant data
func (r *HomeAssistantReconciler) reconcilePVC(ctx context.Context, ha *hav1.HomeAssistant) error {
	log := logf.FromContext(ctx)
	pvcName := fmt.Sprintf("%s-data", ha.Name)

	pvc := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: ha.Namespace}, pvc)

	if err != nil && errors.IsNotFound(err) {
		// Create new PVC
		pvc = r.buildPVC(ha, pvcName)
		retain := ha.Spec.Storage != nil && ha.Spec.Storage.RetainPVC
		if !retain {
			if err := controllerutil.SetControllerReference(ha, pvc, r.Scheme); err != nil {
				return err
			}
		}
		log.Info("Creating PVC", "PVC.Name", pvc.Name, "retainPVC", retain)
		return r.Create(ctx, pvc)
	} else if err != nil {
		return err
	}

	// PVC exists — reconcile ownerReference to match current retainPVC setting
	retain := ha.Spec.Storage != nil && ha.Spec.Storage.RetainPVC
	isOwned := false
	for _, ref := range pvc.OwnerReferences {
		if ref.Controller != nil && *ref.Controller && ref.UID == ha.UID {
			isOwned = true
			break
		}
	}

	if !retain && !isOwned {
		if err := controllerutil.SetControllerReference(ha, pvc, r.Scheme); err != nil {
			return err
		}
		log.Info("Setting ownerReference on PVC (retainPVC=false)", "PVC.Name", pvc.Name)
		return r.Update(ctx, pvc)
	}

	if retain && isOwned {
		var filtered []metav1.OwnerReference
		for _, ref := range pvc.OwnerReferences {
			if ref.UID != ha.UID {
				filtered = append(filtered, ref)
			}
		}
		pvc.OwnerReferences = filtered
		log.Info("Removing ownerReference from PVC (retainPVC=true)", "PVC.Name", pvc.Name)
		return r.Update(ctx, pvc)
	}

	log.V(1).Info("PVC already exists", "PVC.Name", pvc.Name)
	return nil
}

// buildPVC creates a PVC spec for Home Assistant
func (r *HomeAssistantReconciler) buildPVC(ha *hav1.HomeAssistant, name string) *corev1.PersistentVolumeClaim {
	labels := r.labelsForHomeAssistant(ha)

	storageSize := resource.MustParse(defaultStorageSize)
	if ha.Spec.Storage != nil && !ha.Spec.Storage.Size.IsZero() {
		storageSize = ha.Spec.Storage.Size
	}

	accessMode := corev1.ReadWriteOnce
	if ha.Spec.Storage != nil && ha.Spec.Storage.AccessMode != "" {
		accessMode = ha.Spec.Storage.AccessMode
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ha.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{accessMode},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storageSize,
				},
			},
		},
	}

	if ha.Spec.Storage != nil && ha.Spec.Storage.StorageClassName != nil {
		pvc.Spec.StorageClassName = ha.Spec.Storage.StorageClassName
	}

	return pvc
}

// reconcileStatefulSet ensures the StatefulSet exists and is up to date
func (r *HomeAssistantReconciler) reconcileStatefulSet(ctx context.Context, ha *hav1.HomeAssistant) error {
	log := logf.FromContext(ctx)

	sts := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: ha.Name, Namespace: ha.Namespace}, sts)

	if err != nil && errors.IsNotFound(err) {
		// Create new StatefulSet
		sts, err = r.buildStatefulSet(ctx, ha)
		if err != nil {
			return err
		}

		// Sync config hash from ConfigMap even for new StatefulSet
		// This ensures the hash annotation is set from the beginning
		if err := r.syncConfigHashFromConfigMap(ctx, ha, sts); err != nil {
			log.Error(err, "Failed to sync config hash from ConfigMap during creation")
			// Don't fail creation - just log the error
		}

		if err := controllerutil.SetControllerReference(ha, sts, r.Scheme); err != nil {
			return err
		}
		log.Info("Creating StatefulSet", "StatefulSet.Name", sts.Name)
		return r.Create(ctx, sts)
	} else if err != nil {
		return err
	}

	// Update StatefulSet if needed
	desired, err := r.buildStatefulSet(ctx, ha)
	if err != nil {
		return err
	}

	// Sync config hash from ConfigMap to desired StatefulSet (Faza 2)
	// This allows HomeAssistantConfiguration Controller to signal configuration changes
	// by updating ConfigMap annotation, which we then propagate to StatefulSet
	if err := r.syncConfigHashFromConfigMap(ctx, ha, desired); err != nil {
		log.Error(err, "Failed to sync config hash from ConfigMap")
		// Don't fail reconciliation - just log the error
	}

	if needsUpdate(sts, desired) {
		log.Info("Updating StatefulSet", "StatefulSet.Name", sts.Name)

		// Retry with exponential backoff to handle optimistic locking conflicts
		// This prevents race conditions when multiple controllers try to update StatefulSet simultaneously
		const maxRetries = 3
		backoff := time.Millisecond * 100

		for attempt := 1; attempt <= maxRetries; attempt++ {
			// Refresh StatefulSet from API server to get latest resourceVersion
			freshSts := &appsv1.StatefulSet{}
			if err := r.Get(ctx, types.NamespacedName{Name: ha.Name, Namespace: ha.Namespace}, freshSts); err != nil {
				return err
			}

			// Apply desired spec to fresh object
			freshSts.Spec = desired.Spec

			// Attempt update
			if err := r.Update(ctx, freshSts); err != nil {
				if errors.IsConflict(err) && attempt < maxRetries {
					// Optimistic locking conflict - retry with backoff
					log.Info("StatefulSet update conflict, retrying",
						"attempt", attempt,
						"backoff", backoff)
					time.Sleep(backoff)
					backoff *= 2 // Exponential backoff
					continue
				}
				// Non-conflict error or max retries exceeded
				return err
			}

			// Success
			log.Info("StatefulSet updated successfully", "attempt", attempt)
			return nil
		}

		return fmt.Errorf("failed to update StatefulSet after %d retries", maxRetries)
	}

	return nil
}

// getGeneratedConfigMapName returns the name of the auto-generated ConfigMap if
// HomeAssistantConfiguration exists
func (r *HomeAssistantReconciler) getGeneratedConfigMapName(
	ctx context.Context,
	ha *hav1.HomeAssistant,
) (string, error) {
	log := logf.FromContext(ctx)

	haConfig, err := r.findHomeAssistantConfiguration(ctx, ha)
	if err != nil {
		return "", err
	}
	if haConfig != nil {
		generatedConfigMapName := ha.Name + "-configuration"
		log.V(1).Info("Found HomeAssistantConfiguration, using generated ConfigMap", "configmap", generatedConfigMapName)
		return generatedConfigMapName, nil
	}

	return "", nil
}

// syncConfigHashFromConfigMap syncs the config hash annotation from ConfigMap to StatefulSet.
// This implements the architectural pattern from Faza 2 (rozwiazanie-architektury.md):
// - HomeAssistantConfiguration Controller updates ConfigMap with hash annotation
// - HomeAssistant Controller (this function) reads the hash and syncs to StatefulSet
// - StatefulSet annotation change triggers Kubernetes rolling restart
// Includes debouncing to prevent rapid updates during concurrent reconciliation
func (r *HomeAssistantReconciler) syncConfigHashFromConfigMap(
	ctx context.Context,
	ha *hav1.HomeAssistant,
	sts *appsv1.StatefulSet,
) error {
	log := logf.FromContext(ctx)

	// 1. Get generated ConfigMap name
	configMapName, err := r.getGeneratedConfigMapName(ctx, ha)
	if err != nil || configMapName == "" {
		return nil // No HomeAssistantConfiguration exists, skip
	}

	// 2. Fetch ConfigMap
	configMap := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      configMapName,
		Namespace: ha.Namespace,
	}, configMap); err != nil {
		if errors.IsNotFound(err) {
			return nil // ConfigMap doesn't exist yet
		}
		return err
	}

	// 3. Get hash from ConfigMap annotation
	configMapHash := configMap.Annotations[configHashAnnotationKey]
	if configMapHash == "" {
		return nil // No hash yet
	}

	// 4. Get current hash from StatefulSet pod template
	if sts.Spec.Template.Annotations == nil {
		sts.Spec.Template.Annotations = make(map[string]string)
	}
	currentHash := sts.Spec.Template.Annotations[configHashAnnotationKey]

	// 5. If different, check debouncing before updating
	if configMapHash != currentHash {
		// Check if we should debounce (wait before applying change)
		resourceKey := fmt.Sprintf("%s/%s", ha.Namespace, ha.Name)
		lastSyncVal, exists := r.lastConfigHashSync.Load(resourceKey)
		var lastSync time.Time
		if exists {
			lastSync = lastSyncVal.(time.Time)
		}
		debounceWindow := time.Second * 2 // Wait 2 seconds between config hash syncs

		if exists && time.Since(lastSync) < debounceWindow {
			// Too soon since last sync - skip this update to avoid race conditions
			log.V(1).Info("Debouncing config hash sync",
				"configmap", configMapName,
				"timeSinceLastSync", time.Since(lastSync),
				"debounceWindow", debounceWindow)
			return nil
		}

		// Update debounce timestamp
		r.lastConfigHashSync.Store(resourceKey, time.Now())

		log.Info("Config hash changed, updating StatefulSet annotation",
			"configmap", configMapName,
			"oldHash", currentHash,
			"newHash", configMapHash)

		sts.Spec.Template.Annotations[configHashAnnotationKey] = configMapHash
	}

	return nil
}

// getGeneratedSecretsName returns the name of the auto-generated Secret if
// HomeAssistantSecrets exists
func (r *HomeAssistantReconciler) getGeneratedSecretsName(
	ctx context.Context,
	ha *hav1.HomeAssistant,
) (string, error) {
	log := logf.FromContext(ctx)

	// List all HomeAssistantSecrets in the same namespace
	haSecretsList := &hav1.HomeAssistantSecretsList{}
	if err := r.List(ctx, haSecretsList, client.InNamespace(ha.Namespace)); err != nil {
		return "", err
	}

	// Find HomeAssistantSecrets that references this HomeAssistant
	for _, haSecrets := range haSecretsList.Items {
		if haSecrets.Spec.HomeAssistantRef.Name == ha.Name {
			// Found a HomeAssistantSecrets for this HA
			generatedSecretName := ha.Name + generatedSecretSuffix
			log.V(1).Info("Found HomeAssistantSecrets, using generated secret", "secret", generatedSecretName)
			return generatedSecretName, nil
		}
	}

	return "", nil
}

// buildStatefulSet creates a StatefulSet spec for Home Assistant
func (r *HomeAssistantReconciler) buildStatefulSet(
	ctx context.Context,
	ha *hav1.HomeAssistant,
) (*appsv1.StatefulSet, error) {
	labels := r.labelsForHomeAssistant(ha)
	replicas := int32(1)

	image := defaultImage
	if ha.Spec.Image != "" {
		image = ha.Spec.Image
	}

	version := defaultVersion
	if ha.Spec.Version != "" {
		version = ha.Spec.Version
	}

	timezone := defaultTimezone
	if ha.Spec.Timezone != "" {
		timezone = ha.Spec.Timezone
	}

	pvcName := fmt.Sprintf("%s-data", ha.Name)

	// Build volume mounts
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "config",
			MountPath: "/config",
		},
	}

	if ha.Spec.AdditionalVolumes != nil && ha.Spec.AdditionalVolumes.VolumeMounts != nil {
		volumeMounts = append(volumeMounts, ha.Spec.AdditionalVolumes.VolumeMounts...)
	}

	// Build volumes
	volumes := []corev1.Volume{
		{
			Name: "config",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName,
				},
			},
		},
	}

	if ha.Spec.AdditionalVolumes != nil && ha.Spec.AdditionalVolumes.Volumes != nil {
		volumes = append(volumes, ha.Spec.AdditionalVolumes.Volumes...)
	}

	// Add ConfigMap volume for configuration.yaml
	// HomeAssistantConfiguration CRD is REQUIRED - always add the volume
	generatedConfigMapName, err := r.getGeneratedConfigMapName(ctx, ha)
	if err != nil || generatedConfigMapName == "" {
		// This should not happen if validation passed in Reconcile()
		// But handle gracefully - skip volume mount
		// The Reconcile() loop will requeue until HomeAssistantConfiguration exists
	} else {
		// Always add the ConfigMap volume (ConfigMap is guaranteed to exist by HomeAssistantConfiguration controller)
		volumes = append(volumes, corev1.Volume{
			Name: "ha-configuration",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: generatedConfigMapName,
					},
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "ha-configuration",
			MountPath: "/config/configuration.yaml",
			SubPath:   "configuration.yaml",
		})
	}

	// Add Secret volume for recorder_db_url.yaml when databaseSecretRef is configured.
	// The HAConfig controller creates <ha-name>-recorder-db and stores the actual DB URL
	// there so it never lands in the ConfigMap. HA reads it via "!include recorder_db_url.yaml".
	recorderDBSecretName := ha.Name + recorderDBSecretSuffix
	recorderDBSecret := &corev1.Secret{}
	recorderDBKey := types.NamespacedName{Name: recorderDBSecretName, Namespace: ha.Namespace}
	if getErr := r.Get(ctx, recorderDBKey, recorderDBSecret); getErr == nil {
		volumes = append(volumes, corev1.Volume{
			Name: "ha-recorder-db",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: recorderDBSecretName,
					Items: []corev1.KeyToPath{
						{Key: "recorder_db_url.yaml", Path: "recorder_db_url.yaml"},
					},
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "ha-recorder-db",
			MountPath: "/config/recorder_db_url.yaml",
			SubPath:   "recorder_db_url.yaml",
		})
	}

	// Add Secret volume for secrets.yaml
	// Priority: 1) Auto-generated by HomeAssistantSecrets, 2) Spec.SecretsFrom
	generatedSecretName, err := r.getGeneratedSecretsName(ctx, ha)
	if err == nil && generatedSecretName != "" {
		// Use auto-generated secret from HomeAssistantSecrets
		volumes = append(volumes, corev1.Volume{
			Name: "ha-secrets",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: generatedSecretName,
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "ha-secrets",
			MountPath: "/config/secrets.yaml",
			SubPath:   "secrets.yaml",
		})
	} else if ha.Spec.SecretsFrom != nil {
		// Fallback to manually specified secret
		volumes = append(volumes, corev1.Volume{
			Name: "ha-secrets",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: ha.Spec.SecretsFrom.Name,
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "ha-secrets",
			MountPath: "/config/secrets.yaml",
			SubPath:   "secrets.yaml",
		})
	}

	// Device passthrough (spec.alpha.devices): mount declared host device
	// nodes (e.g. /dev/ttyACM0 for a Zigbee/Z-Wave USB coordinator) into the
	// home-assistant container. Volumes are named by index rather than
	// content, since nothing needs name stability across reorders. Never
	// sets `privileged: true` — the container already runs as root with the
	// runtime's default capabilities (which include DAC_OVERRIDE), enough to
	// open a root-owned device node without broader escalation.
	var homeAssistantSecurityContext *corev1.SecurityContext
	if ha.Spec.Alpha != nil && len(ha.Spec.Alpha.Devices) > 0 {
		for i, dev := range ha.Spec.Alpha.Devices {
			containerPath := dev.ContainerPath
			if containerPath == "" {
				containerPath = dev.HostPath
			}
			volumes = append(volumes, corev1.Volume{
				Name: fmt.Sprintf("device-%d", i),
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: dev.HostPath,
						Type: &charDeviceHostPathType,
					},
				},
			})
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      fmt.Sprintf("device-%d", i),
				MountPath: containerPath,
			})
		}
		homeAssistantSecurityContext = &corev1.SecurityContext{Privileged: ptr.To(false)}
	}

	// Preserve existing pod template annotations from current StatefulSet
	// This is critical to avoid infinite reconciliation loops when config hash annotations exist
	existingAnnotations := make(map[string]string)
	currentSts := &appsv1.StatefulSet{}
	if err = r.Get(ctx, types.NamespacedName{Name: ha.Name, Namespace: ha.Namespace}, currentSts); err == nil {
		// StatefulSet exists - preserve its pod template annotations
		if currentSts.Spec.Template.Annotations != nil {
			for k, v := range currentSts.Spec.Template.Annotations {
				existingAnnotations[k] = v
			}
		}
	}
	// If StatefulSet doesn't exist (NotFound error), existingAnnotations will be empty - this is correct

	// Probes always speak plain HTTP: HA serves HTTP inside the cluster and TLS
	// is terminated at the edge (Ingress / Gateway API), never in the HA pod.
	probeScheme := corev1.URISchemeHTTP

	// Community repository sidecar: only injected when at least one
	// HomeAssistantCommunityRepository actually targets this instance — the stable
	// HomeAssistant CRD carries no footprint from this alpha feature unless it is
	// in use.
	containers := []corev1.Container{
		{
			Name:            "home-assistant",
			Image:           fmt.Sprintf("%s:%s", image, version),
			ImagePullPolicy: corev1.PullIfNotPresent,
			Ports: []corev1.ContainerPort{
				{
					Name:          "http",
					ContainerPort: defaultPort,
					Protocol:      corev1.ProtocolTCP,
				},
			},
			Env: []corev1.EnvVar{
				{
					Name:  "TZ",
					Value: timezone,
				},
			},
			VolumeMounts: volumeMounts,
			LivenessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path:   "/",
						Port:   intstr.FromInt(defaultPort),
						Scheme: probeScheme,
					},
				},
				InitialDelaySeconds: 30,
				PeriodSeconds:       10,
				TimeoutSeconds:      5,
				SuccessThreshold:    1,
				FailureThreshold:    3,
			},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path:   "/",
						Port:   intstr.FromInt(defaultPort),
						Scheme: probeScheme,
					},
				},
				InitialDelaySeconds: 10,
				PeriodSeconds:       5,
				TimeoutSeconds:      3,
				SuccessThreshold:    1,
				FailureThreshold:    3,
			},
			SecurityContext: homeAssistantSecurityContext,
		},
	}
	hasCR, err := hasCommunityRepositories(ctx, r.Client, ha)
	if err != nil {
		return nil, fmt.Errorf("failed to check for HomeAssistantCommunityRepository resources: %w", err)
	}
	if hasCR {
		containers = append(containers, r.buildCommunityRepositorySidecar(ha))
		volumes = append(volumes, buildCommunityRepositoryConfigMapVolume(ha))
	}

	initContainers, err := r.buildInitContainers(ctx, ha)
	if err != nil {
		return nil, err
	}

	// The HA pod (and its community-repository sidecar) never call the Kubernetes
	// API — deny it a ServiceAccount token rather than relying on the "default"
	// ServiceAccount's implicit automount, least-privilege.
	automountSAToken := false

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ha.Name,
			Namespace: ha.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			ServiceName: ha.Name,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: existingAnnotations,
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &automountSAToken,
					InitContainers:               initContainers,
					Containers:                   containers,
					Volumes:                      volumes,
				},
			},
		},
	}

	// Apply resource requirements if specified
	if ha.Spec.Resources.Limits != nil || ha.Spec.Resources.Requests != nil {
		sts.Spec.Template.Spec.Containers[0].Resources = ha.Spec.Resources
	}

	// Apply host networking if specified
	if ha.Spec.HostNetwork != nil && *ha.Spec.HostNetwork {
		sts.Spec.Template.Spec.HostNetwork = true
		sts.Spec.Template.Spec.DNSPolicy = corev1.DNSClusterFirstWithHostNet
	} else {
		sts.Spec.Template.Spec.DNSPolicy = corev1.DNSClusterFirst
	}

	// Apply pod scheduling constraints if specified. Every field is copied
	// verbatim onto the pod template — Kubernetes' own scheduler applies them,
	// this operator implements no scheduling logic of its own.
	if ha.Spec.Scheduling != nil {
		sts.Spec.Template.Spec.NodeSelector = ha.Spec.Scheduling.NodeSelector
		sts.Spec.Template.Spec.Affinity = ha.Spec.Scheduling.Affinity
		sts.Spec.Template.Spec.Tolerations = ha.Spec.Scheduling.Tolerations
		sts.Spec.Template.Spec.PriorityClassName = ha.Spec.Scheduling.PriorityClassName
	}

	return sts, nil
}

// reconcileService ensures the Service exists and is up to date
func (r *HomeAssistantReconciler) reconcileService(ctx context.Context, ha *hav1.HomeAssistant) error {
	log := logf.FromContext(ctx)

	svc := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: ha.Name, Namespace: ha.Namespace}, svc)

	if err != nil && errors.IsNotFound(err) {
		// Create new Service
		svc = r.buildService(ha)
		if err := controllerutil.SetControllerReference(ha, svc, r.Scheme); err != nil {
			return err
		}
		log.Info("Creating Service", "Service.Name", svc.Name)
		return r.Create(ctx, svc)
	} else if err != nil {
		return err
	}

	// Update Service if needed
	desired := r.buildService(ha)
	if svc.Spec.Type != desired.Spec.Type ||
		len(svc.Spec.Ports) == 0 ||
		len(desired.Spec.Ports) == 0 ||
		svc.Spec.Ports[0].Port != desired.Spec.Ports[0].Port {
		svc.Spec.Type = desired.Spec.Type
		svc.Spec.Ports = desired.Spec.Ports
		log.Info("Updating Service", "Service.Name", svc.Name)
		return r.Update(ctx, svc)
	}

	return nil
}

// buildService creates a Service spec for Home Assistant
func (r *HomeAssistantReconciler) buildService(ha *hav1.HomeAssistant) *corev1.Service {
	labels := r.labelsForHomeAssistant(ha)

	serviceType := corev1.ServiceTypeClusterIP
	if ha.Spec.Service != nil && ha.Spec.Service.Type != "" {
		serviceType = ha.Spec.Service.Type
	}

	port := int32(defaultPort)
	if ha.Spec.Service != nil && ha.Spec.Service.Port != 0 {
		port = ha.Spec.Service.Port
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ha.Name,
			Namespace: ha.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     serviceType,
			Selector: labels,
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       port,
					TargetPort: intstr.FromInt(defaultPort),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	// Set NodePort if specified
	if serviceType == corev1.ServiceTypeNodePort && ha.Spec.Service != nil && ha.Spec.Service.NodePort != 0 {
		svc.Spec.Ports[0].NodePort = ha.Spec.Service.NodePort
	}

	return svc
}

// reconcileNetworkPolicy creates, updates, or removes the (alpha) NetworkPolicy
// for the Home Assistant pod based on spec.alpha.networkPolicy.enabled. This is
// the first conditionally-created resource in this reconciler, so unlike
// reconcileService/reconcilePVC it must also handle the disabled case by
// deleting a previously-created policy.
func (r *HomeAssistantReconciler) reconcileNetworkPolicy(ctx context.Context, ha *hav1.HomeAssistant) error {
	log := logf.FromContext(ctx)

	enabled := ha.Spec.Alpha != nil &&
		ha.Spec.Alpha.NetworkPolicy != nil &&
		ha.Spec.Alpha.NetworkPolicy.Enabled

	if enabled && os.Getenv("OPERATOR_NAMESPACE") == "" {
		log.Info("WARNING: spec.alpha.networkPolicy.enabled is true but OPERATOR_NAMESPACE is not set — " +
			"the NetworkPolicy will not include an operator-namespace ingress peer, which may break " +
			"bootstrap, hot-reload, and health-check connectivity from the operator to this HomeAssistant")
	}

	np := &networkingv1.NetworkPolicy{}
	err := r.Get(ctx, types.NamespacedName{Name: ha.Name, Namespace: ha.Namespace}, np)

	if !enabled {
		if err == nil {
			if !metav1.IsControlledBy(np, ha) {
				log.Info("Existing NetworkPolicy is not owned by this HomeAssistant, leaving it untouched",
					"NetworkPolicy.Name", np.Name)
				return nil
			}
			log.Info("Deleting NetworkPolicy (spec.alpha.networkPolicy.enabled is false)", "NetworkPolicy.Name", np.Name)
			return client.IgnoreNotFound(r.Delete(ctx, np))
		}
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	if err != nil && errors.IsNotFound(err) {
		np = r.buildNetworkPolicy(ha)
		if err := controllerutil.SetControllerReference(ha, np, r.Scheme); err != nil {
			return err
		}
		log.Info("Creating NetworkPolicy", "NetworkPolicy.Name", np.Name)
		return r.Create(ctx, np)
	} else if err != nil {
		return err
	}

	if !metav1.IsControlledBy(np, ha) {
		log.Info("Existing NetworkPolicy is not owned by this HomeAssistant, leaving it untouched",
			"NetworkPolicy.Name", np.Name)
		return nil
	}

	desired := r.buildNetworkPolicy(ha)
	if !reflect.DeepEqual(np.Spec, desired.Spec) {
		np.Spec = desired.Spec
		log.Info("Updating NetworkPolicy", "NetworkPolicy.Name", np.Name)
		return r.Update(ctx, np)
	}

	return nil
}

// buildNetworkPolicy builds the (alpha) NetworkPolicy restricting ingress to the
// Home Assistant pod to the same namespace and the operator's namespace, on the
// Service port. Egress is intentionally left unrestricted — Home Assistant needs
// broad, unpredictable egress to IoT devices, cloud APIs, and MQTT brokers.
func (r *HomeAssistantReconciler) buildNetworkPolicy(ha *hav1.HomeAssistant) *networkingv1.NetworkPolicy {
	labels := r.labelsForHomeAssistant(ha)

	port := int32(defaultPort)
	if ha.Spec.Service != nil && ha.Spec.Service.Port != 0 {
		port = ha.Spec.Service.Port
	}
	tcpPort := intstr.FromInt(int(port))
	protocol := corev1.ProtocolTCP

	// Same namespace as the Home Assistant pod (e.g. other tooling, add-ons).
	peers := []networkingv1.NetworkPolicyPeer{
		{PodSelector: &metav1.LabelSelector{}},
	}

	// The operator's own namespace, so it can keep reaching the HA API
	// (bootstrap, hot-reload, health checks). Uses the well-known
	// kubernetes.io/metadata.name label present on every namespace.
	if operatorNamespace := os.Getenv("OPERATOR_NAMESPACE"); operatorNamespace != "" {
		peers = append(peers, networkingv1.NetworkPolicyPeer{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					corev1.LabelMetadataName: operatorNamespace,
				},
			},
		})
	}

	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ha.Name,
			Namespace: ha.Namespace,
			Labels:    labels,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: labels,
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: peers,
					Ports: []networkingv1.NetworkPolicyPort{
						{
							Protocol: &protocol,
							Port:     &tcpPort,
						},
					},
				},
			},
		},
	}
}

// labelsForHomeAssistant returns the labels for selecting resources belonging to the given HomeAssistant CR
func (r *HomeAssistantReconciler) labelsForHomeAssistant(ha *hav1.HomeAssistant) map[string]string {
	return map[string]string{
		labelAppName:      "homeassistant",
		labelAppInstance:  ha.Name,
		labelAppManagedBy: "homeassistant-operator",
	}
}

// updateHAStatusWithRetry applies mutate to ha's status and persists it via the
// status subresource, retrying on resourceVersion conflicts by re-fetching the
// latest object and replaying mutate against it. Child-resource creation (PVC,
// StatefulSet, Service, ...) fires several owner-reference watch events in a
// burst right after a HomeAssistant is created, which can trigger overlapping
// reconciles; without this retry, the first conflict aborts the whole
// reconcile and the status write is lost until the next trigger. mutate must
// be idempotent (it may run more than once) and return whether it changed
// anything; when it returns false, no write is attempted.
func (r *HomeAssistantReconciler) updateHAStatusWithRetry(
	ctx context.Context, ha *hav1.HomeAssistant, mutate func(*hav1.HomeAssistant) bool,
) error {
	log := logf.FromContext(ctx)
	key := client.ObjectKeyFromObject(ha)
	attempted := false
	attempts := 0
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		attempts++
		if attempted {
			if err := r.Get(ctx, key, ha); err != nil {
				log.V(1).Info("updateHAStatusWithRetry: re-fetch failed", "attempts", attempts, "error", err)
				return err
			}
		}
		attempted = true
		if !mutate(ha) {
			return nil
		}
		err := r.Status().Update(ctx, ha)
		if err != nil {
			log.V(1).Info("updateHAStatusWithRetry: Status().Update failed",
				"attempts", attempts, "isConflict", errors.IsConflict(err), "error", err)
		}
		return err
	})
}

// updateStatusFailed updates the status when reconciliation fails
func (r *HomeAssistantReconciler) updateStatusFailed(
	ctx context.Context,
	ha *hav1.HomeAssistant,
	reconcileErr error,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if err := r.updateHAStatusWithRetry(ctx, ha, func(h *hav1.HomeAssistant) bool {
		h.Status.Phase = hav1.PhaseFailed
		h.Status.Ready = false
		h.Status.ObservedGeneration = h.Generation
		meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionFalse,
			Reason:             "ReconciliationFailed",
			Message:            reconcileErr.Error(),
			ObservedGeneration: h.Generation,
		})
		return true
	}); err != nil {
		log.Error(err, "Failed to update HomeAssistant status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, reconcileErr
}

// updateStatusFromStatefulSet updates the status based on StatefulSet state
func (r *HomeAssistantReconciler) updateStatusFromStatefulSet(
	ctx context.Context,
	ha *hav1.HomeAssistant,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	sts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, types.NamespacedName{Name: ha.Name, Namespace: ha.Namespace}, sts); err != nil {
		return ctrl.Result{}, err
	}

	// Determine version from image
	version := defaultVersion
	if ha.Spec.Version != "" {
		version = ha.Spec.Version
	}

	stsReady := sts.Status.ReadyReplicas > 0 && sts.Status.ReadyReplicas == sts.Status.Replicas

	// Fetched once and shared by both condition builders below (rather than
	// each independently re-fetching the same object) whenever either might
	// need it — skipped entirely once the StatefulSet is ready, since both
	// conditions short-circuit to their "healthy" state in that case without
	// consulting the pod.
	var pod *corev1.Pod
	devicesDeclared := ha.Spec.Alpha != nil && len(ha.Spec.Alpha.Devices) > 0
	schedulingDeclared := schedulingConstraintsDeclared(ha.Spec.Scheduling)
	if !stsReady && (devicesDeclared || schedulingDeclared) {
		p := &corev1.Pod{}
		if err := r.Get(ctx, types.NamespacedName{Name: ha.Name + "-0", Namespace: ha.Namespace}, p); err != nil {
			if !errors.IsNotFound(err) {
				log.V(1).Info("updateStatusFromStatefulSet: failed to get pod", "error", err)
			}
		} else {
			pod = p
		}
	}

	devicesCondition := r.buildDevicesReadyCondition(ctx, ha, stsReady, pod)
	schedulingCondition := r.buildSchedulingReadyCondition(ha, stsReady, pod)
	// schedulingCondition was computed from ha/pod state as observed at this
	// generation. If a conflict forces updateHAStatusWithRetry to re-fetch ha
	// at a newer generation, that computation is stale for the new spec — skip
	// publishing it under the newer generation and let the next reconcile
	// (triggered by the spec change) recompute it correctly instead.
	schedulingEvaluatedGeneration := ha.Generation

	if err := r.updateHAStatusWithRetry(ctx, ha, func(h *hav1.HomeAssistant) bool {
		h.Status.Version = version
		h.Status.ObservedGeneration = h.Generation

		// Check if StatefulSet is ready
		if stsReady {
			h.Status.Phase = hav1.PhaseRunning
			h.Status.Ready = true

			meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{
				Type:               conditionTypeReady,
				Status:             metav1.ConditionTrue,
				Reason:             "StatefulSetReady",
				Message:            "Home Assistant is running",
				ObservedGeneration: h.Generation,
			})
		} else {
			h.Status.Phase = hav1.PhasePending
			h.Status.Ready = false

			meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{
				Type:   conditionTypeReady,
				Status: metav1.ConditionFalse,
				Reason: "StatefulSetNotReady",
				Message: fmt.Sprintf(
					"Waiting for StatefulSet to be ready (%d/%d)",
					sts.Status.ReadyReplicas, sts.Status.Replicas,
				),
				ObservedGeneration: h.Generation,
			})
		}

		devicesCondition.ObservedGeneration = h.Generation
		meta.SetStatusCondition(&h.Status.Conditions, devicesCondition)

		if h.Generation == schedulingEvaluatedGeneration {
			schedulingCondition.ObservedGeneration = h.Generation
			meta.SetStatusCondition(&h.Status.Conditions, schedulingCondition)
		}
		return true
	}); err != nil {
		log.Error(err, "Failed to update HomeAssistant status")
		return ctrl.Result{}, err
	}

	// Requeue if not ready yet
	if !ha.Status.Ready {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

// buildDevicesReadyCondition reports whether spec.alpha.devices entries are
// usable, so a device missing on the scheduled node is diagnosable straight
// from `kubectl describe homeassistant` rather than requiring the user to
// inspect raw pod events. StatefulSet-level readiness alone never surfaces
// *why* a pod isn't Ready — a failed hostPath mount only shows up as a
// kubelet-emitted "FailedMount" Event on the pod, which is why this reads
// Events directly (see internal/controller RBAC: core/events get;list;watch).
func (r *HomeAssistantReconciler) buildDevicesReadyCondition(
	ctx context.Context, ha *hav1.HomeAssistant, stsReady bool, pod *corev1.Pod,
) metav1.Condition {
	log := logf.FromContext(ctx)

	if ha.Spec.Alpha == nil || len(ha.Spec.Alpha.Devices) == 0 {
		return metav1.Condition{
			Type:    conditionTypeDevicesReady,
			Status:  metav1.ConditionTrue,
			Reason:  reasonNoDevicesDeclared,
			Message: "No devices declared in spec.alpha.devices",
		}
	}

	if stsReady {
		return metav1.Condition{
			Type:    conditionTypeDevicesReady,
			Status:  metav1.ConditionTrue,
			Reason:  reasonDevicesMounted,
			Message: "All declared devices are mounted",
		}
	}

	podName := ha.Name + "-0"

	// No pod yet (or the caller's fetch failed) means no FailedMount events to
	// explain either; skip straight to Pending rather than paying for an
	// event lookup that can't find anything.
	if pod == nil {
		return metav1.Condition{
			Type:    conditionTypeDevicesReady,
			Status:  metav1.ConditionUnknown,
			Reason:  reasonDevicesPending,
			Message: "Waiting for the pod to determine device availability",
		}
	}
	// Events older than the current pod incarnation are from a previous
	// generation (e.g. a since-fixed device) and must not be reported as
	// current — Events have no owner reference to the pod and can outlive it.
	podStart := pod.CreationTimestamp.Time

	eventList := &corev1.EventList{}
	listErr := r.eventReader().List(ctx, eventList,
		client.InNamespace(ha.Namespace), client.MatchingFields{"involvedObject.name": podName})
	if listErr != nil {
		log.V(1).Info("buildDevicesReadyCondition: failed to list events", "error", listErr)
	} else {
		for _, ev := range eventList.Items {
			if ev.Reason != failedMountEventReason || ev.LastTimestamp.Time.Before(podStart) {
				continue
			}
			for _, dev := range ha.Spec.Alpha.Devices {
				if hostPathReferencedIn(ev.Message, dev.HostPath) {
					return metav1.Condition{
						Type:   conditionTypeDevicesReady,
						Status: metav1.ConditionFalse,
						Reason: reasonDeviceUnavailable,
						Message: fmt.Sprintf(
							"Device %q unavailable: %s", dev.HostPath, ev.Message,
						),
					}
				}
			}
		}
	}

	return metav1.Condition{
		Type:    conditionTypeDevicesReady,
		Status:  metav1.ConditionUnknown,
		Reason:  reasonDevicesPending,
		Message: "Waiting for the pod to determine device availability",
	}
}

// schedulingConstraintsDeclared reports whether spec.scheduling declares any
// actual constraint — a non-nil but all-zero-value SchedulingSpec (e.g.
// `spec: {scheduling: {}}`) is equivalent to it being unset.
func schedulingConstraintsDeclared(s *hav1.SchedulingSpec) bool {
	if s == nil {
		return false
	}
	return len(s.NodeSelector) > 0 || s.Affinity != nil || len(s.Tolerations) > 0 || s.PriorityClassName != ""
}

// publishSchedulingReadyEarly best-effort publishes SchedulingReady ahead of
// reconcileBootstrap. Bootstrap's own health-check loop keeps requeuing
// (never reaching updateStatusFromStatefulSet, which republishes this same
// condition as part of the full status update) as long as HA never becomes
// ready — which never happens for a pod stuck Pending on an unsatisfiable
// nodeSelector/affinity/toleration. Without this early publish, that state
// would never surface on status at all.
func (r *HomeAssistantReconciler) publishSchedulingReadyEarly(ctx context.Context, ha *hav1.HomeAssistant) error {
	if !schedulingConstraintsDeclared(ha.Spec.Scheduling) {
		return nil
	}

	sts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, types.NamespacedName{Name: ha.Name, Namespace: ha.Namespace}, sts); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	stsReady := sts.Status.ReadyReplicas > 0 && sts.Status.ReadyReplicas == sts.Status.Replicas

	var pod *corev1.Pod
	if !stsReady {
		p := &corev1.Pod{}
		if err := r.Get(ctx, types.NamespacedName{Name: ha.Name + "-0", Namespace: ha.Namespace}, p); err != nil {
			if !errors.IsNotFound(err) {
				logf.FromContext(ctx).V(1).Info("publishSchedulingReadyEarly: failed to get pod", "error", err)
			}
		} else {
			pod = p
		}
	}

	cond := r.buildSchedulingReadyCondition(ha, stsReady, pod)
	evaluatedGeneration := ha.Generation
	return r.updateHAStatusWithRetry(ctx, ha, func(h *hav1.HomeAssistant) bool {
		if h.Generation != evaluatedGeneration {
			return false
		}
		cond.ObservedGeneration = h.Generation
		return meta.SetStatusCondition(&h.Status.Conditions, cond)
	})
}

// buildSchedulingReadyCondition reports whether spec.scheduling's declared
// constraints are currently satisfiable, so an unschedulable pod is
// diagnosable straight from `kubectl describe homeassistant` instead of a
// generic "not ready". Unlike buildDevicesReadyCondition, this needs no new
// event-parsing logic: Kubernetes itself already maintains a structured
// PodScheduled condition on every pod the moment the scheduler can't place
// it — this only reads and mirrors that.
func (r *HomeAssistantReconciler) buildSchedulingReadyCondition(
	ha *hav1.HomeAssistant, stsReady bool, pod *corev1.Pod,
) metav1.Condition {
	if !schedulingConstraintsDeclared(ha.Spec.Scheduling) {
		return metav1.Condition{
			Type:    conditionTypeSchedulingReady,
			Status:  metav1.ConditionTrue,
			Reason:  reasonNoConstraintsDeclared,
			Message: "No scheduling constraints declared in spec.scheduling",
		}
	}

	if stsReady {
		return metav1.Condition{
			Type:    conditionTypeSchedulingReady,
			Status:  metav1.ConditionTrue,
			Reason:  reasonScheduled,
			Message: "Pod satisfies all declared scheduling constraints",
		}
	}

	if pod == nil {
		return metav1.Condition{
			Type:    conditionTypeSchedulingReady,
			Status:  metav1.ConditionUnknown,
			Reason:  reasonSchedulingPending,
			Message: "Waiting for the pod to be scheduled",
		}
	}

	for _, cond := range pod.Status.Conditions {
		if cond.Type != corev1.PodScheduled {
			continue
		}
		switch cond.Status {
		case corev1.ConditionTrue:
			return metav1.Condition{
				Type:    conditionTypeSchedulingReady,
				Status:  metav1.ConditionTrue,
				Reason:  reasonScheduled,
				Message: "Pod satisfies all declared scheduling constraints",
			}
		case corev1.ConditionFalse:
			return metav1.Condition{
				Type:    conditionTypeSchedulingReady,
				Status:  metav1.ConditionFalse,
				Reason:  reasonUnschedulable,
				Message: cond.Message,
			}
		default:
			// ConditionUnknown falls through to the Pending default below.
		}
	}

	return metav1.Condition{
		Type:    conditionTypeSchedulingReady,
		Status:  metav1.ConditionUnknown,
		Reason:  reasonSchedulingPending,
		Message: "Waiting for the pod to be scheduled",
	}
}

// eventReader returns the uncached, direct-to-API-server reader for Event
// lookups so the controller-runtime cache never has to hold an informer over
// every Event in the namespace (a high-churn resource) just for this
// occasional diagnostic read. Falls back to the regular (cached) client if
// APIReader was not wired up, e.g. in tests that construct the reconciler
// directly against an uncached envtest client.
func (r *HomeAssistantReconciler) eventReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// hostPathReferencedIn reports whether hostPath appears in message as a
// standalone path rather than as a substring of a longer, unrelated path
// (e.g. declared device "/dev/ttyACM1" must not match a message referencing
// "/dev/ttyACM10").
func hostPathReferencedIn(message, hostPath string) bool {
	searchFrom := 0
	for {
		idx := strings.Index(message[searchFrom:], hostPath)
		if idx < 0 {
			return false
		}
		start := searchFrom + idx
		end := start + len(hostPath)
		before := byte(0)
		if start > 0 {
			before = message[start-1]
		}
		after := byte(0)
		if end < len(message) {
			after = message[end]
		}
		if !isPathBoundaryByte(before) && !isPathBoundaryByte(after) {
			return true
		}
		searchFrom = start + 1
	}
}

// isPathBoundaryByte reports whether b could be part of the same filesystem
// path segment as its neighbor, i.e. it is NOT a boundary between the
// matched hostPath and surrounding text.
func isPathBoundaryByte(b byte) bool {
	return b == '/' || b == '.' || b == '-' || b == '_' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// needsUpdate checks if the StatefulSet needs to be updated
func needsUpdate(current, desired *appsv1.StatefulSet) bool {
	log := logf.Log.WithName("needsUpdate")

	// Ensure containers exist, and catch a changed container count up front (e.g.
	// the community-repository sidecar being added/removed) — the field-by-field
	// comparisons below only ever look at Containers[0] plus any extra containers
	// past it, so a bare count mismatch would otherwise slip through undetected.
	if len(current.Spec.Template.Spec.Containers) != len(desired.Spec.Template.Spec.Containers) {
		log.V(1).Info("Container count differs",
			"current", len(current.Spec.Template.Spec.Containers),
			"desired", len(desired.Spec.Template.Spec.Containers))
		return true
	}
	if len(desired.Spec.Template.Spec.Containers) == 0 {
		return false
	}

	currentContainer := current.Spec.Template.Spec.Containers[0]
	desiredContainer := desired.Spec.Template.Spec.Containers[0]

	// Check pod template annotations (Faza 2: for config hash changes)
	// This triggers pod restart when configuration changes
	currentAnnotations := current.Spec.Template.Annotations
	desiredAnnotations := desired.Spec.Template.Annotations

	// Compare config-hash annotation specifically
	currentHash := ""
	desiredHash := ""
	if currentAnnotations != nil {
		currentHash = currentAnnotations[configHashAnnotationKey]
	}
	if desiredAnnotations != nil {
		desiredHash = desiredAnnotations[configHashAnnotationKey]
	}
	if currentHash != desiredHash {
		log.V(1).Info("Config hash differs",
			"current", currentHash, "desired", desiredHash)
		return true
	}

	// Check image
	if currentContainer.Image != desiredContainer.Image {
		log.V(1).Info("Image differs",
			"current", currentContainer.Image, "desired", desiredContainer.Image)
		return true
	}

	// Check spec.scheduling.* (NodeSelector/Affinity/Tolerations/PriorityClassName).
	// Extracted to its own function to keep this function's cyclomatic
	// complexity in check, matching the volumeContentDiffers precedent.
	if schedulingFieldsDiffer(current, desired) {
		log.V(1).Info("Scheduling fields differ")
		return true
	}

	// Check security context (e.g. spec.alpha.devices toggling Privileged).
	// Count-only checks below wouldn't catch this: the device count can stay
	// the same while this flips (or vice versa isn't possible today, but
	// don't rely on that).
	if !securityContextsEqual(currentContainer.SecurityContext, desiredContainer.SecurityContext) {
		log.V(1).Info("SecurityContext differs")
		return true
	}

	// Check volumes count (ConfigMap/Secret added or removed)
	if len(current.Spec.Template.Spec.Volumes) != len(desired.Spec.Template.Spec.Volumes) {
		log.V(1).Info("Volume count differs",
			"current", len(current.Spec.Template.Spec.Volumes),
			"desired", len(desired.Spec.Template.Spec.Volumes))
		return true
	}

	// Check volume mounts count
	if len(currentContainer.VolumeMounts) != len(desiredContainer.VolumeMounts) {
		log.V(1).Info("VolumeMount count differs",
			"current", len(currentContainer.VolumeMounts),
			"desired", len(desiredContainer.VolumeMounts))
		return true
	}

	// Check volume/mount content (e.g. a spec.alpha.devices entry's hostPath
	// or containerPath edited in place — the counts above stay unchanged, so
	// this must be compared separately). Extracted to its own function to
	// keep this function's cyclomatic complexity in check.
	if volumeContentDiffers(current, desired, currentContainer, desiredContainer) {
		return true
	}

	// Check environment variables
	if len(currentContainer.Env) != len(desiredContainer.Env) {
		log.V(1).Info("Env count differs",
			"current", len(currentContainer.Env),
			"desired", len(desiredContainer.Env))
		return true
	}
	for i, env := range currentContainer.Env {
		if i >= len(desiredContainer.Env) {
			return true
		}
		if env.Name != desiredContainer.Env[i].Name || env.Value != desiredContainer.Env[i].Value {
			log.V(1).Info("Env differs",
				"index", i,
				"currentName", env.Name, "desiredName", desiredContainer.Env[i].Name,
				"currentVal", env.Value, "desiredVal", desiredContainer.Env[i].Value)
			return true
		}
	}

	// Check resource limits and requests
	if !resourcesEqual(currentContainer.Resources, desiredContainer.Resources) {
		log.V(1).Info("Resources differ",
			"currentRequests", fmt.Sprintf("%v", currentContainer.Resources.Requests),
			"desiredRequests", fmt.Sprintf("%v", desiredContainer.Resources.Requests),
			"currentLimits", fmt.Sprintf("%v", currentContainer.Resources.Limits),
			"desiredLimits", fmt.Sprintf("%v", desiredContainer.Resources.Limits))
		return true
	}

	// Check liveness probe
	if !probesEqual(currentContainer.LivenessProbe, desiredContainer.LivenessProbe) {
		log.V(1).Info("LivenessProbe differs")
		return true
	}

	// Check readiness probe
	if !probesEqual(currentContainer.ReadinessProbe, desiredContainer.ReadinessProbe) {
		log.V(1).Info("ReadinessProbe differs")
		return true
	}

	// Check init containers (e.g. config-init image/tag changes). Compared
	// field-by-field like Resources/Probes above rather than via
	// reflect.DeepEqual: the API server defaults TerminationMessagePath/Policy
	// (and similar fields) on read-back, which a freshly built in-memory
	// container never sets — a raw DeepEqual would report a spurious diff on
	// every single reconcile, forever, never converging.
	if !initContainersEqual(current.Spec.Template.Spec.InitContainers, desired.Spec.Template.Spec.InitContainers) {
		log.V(1).Info("InitContainers differ",
			"current", len(current.Spec.Template.Spec.InitContainers),
			"desired", len(desired.Spec.Template.Spec.InitContainers))
		return true
	}

	// Check any containers beyond the main "home-assistant" one (currently just
	// the optional community-repository sidecar). The count check above already
	// catches it being added/removed; this catches its image/command/env/mounts
	// changing in place. initContainersEqual's comparison isn't init-container
	// specific — it just compares corev1.Container fields our builders set.
	if !initContainersEqual(current.Spec.Template.Spec.Containers[1:], desired.Spec.Template.Spec.Containers[1:]) {
		log.V(1).Info("Additional containers (e.g. community-repository sidecar) differ")
		return true
	}

	// Check host networking, DNS policy, and service account token automount.
	// Extracted to its own function to keep this function's cyclomatic
	// complexity in check, matching the volumeContentDiffers precedent.
	if podLevelFieldsDiffer(current, desired) {
		log.V(1).Info("Pod-level fields (HostNetwork/DNSPolicy/AutomountServiceAccountToken) differ")
		return true
	}

	return false
}

// podLevelFieldsDiffer compares HostNetwork, DNSPolicy, and
// AutomountServiceAccountToken between the current and desired pod
// templates. Split out of needsUpdate to keep its cyclomatic complexity in
// check, matching the volumeContentDiffers precedent.
func podLevelFieldsDiffer(current, desired *appsv1.StatefulSet) bool {
	currentSpec := current.Spec.Template.Spec
	desiredSpec := desired.Spec.Template.Spec

	if currentSpec.HostNetwork != desiredSpec.HostNetwork {
		return true
	}
	if currentSpec.DNSPolicy != desiredSpec.DNSPolicy {
		return true
	}

	currentAutomount := currentSpec.AutomountServiceAccountToken
	desiredAutomount := desiredSpec.AutomountServiceAccountToken
	return (currentAutomount == nil) != (desiredAutomount == nil) ||
		(currentAutomount != nil && desiredAutomount != nil && *currentAutomount != *desiredAutomount)
}

// initContainersEqual compares init containers on the fields our builders
// (buildInitContainers/buildUnbanInitContainer) actually set, ignoring fields
// the API server defaults on read-back (TerminationMessagePath/Policy, etc.)
// that a freshly built in-memory container never populates.
func initContainersEqual(current, desired []corev1.Container) bool {
	if len(current) != len(desired) {
		return false
	}
	for i := range desired {
		c, d := current[i], desired[i]
		if c.Name != d.Name || c.Image != d.Image || c.ImagePullPolicy != d.ImagePullPolicy {
			return false
		}
		if !reflect.DeepEqual(c.Command, d.Command) || !reflect.DeepEqual(c.Args, d.Args) {
			return false
		}
		if !reflect.DeepEqual(c.Env, d.Env) {
			return false
		}
		if !reflect.DeepEqual(c.VolumeMounts, d.VolumeMounts) {
			return false
		}
		if !resourcesEqual(c.Resources, d.Resources) {
			return false
		}
	}
	return true
}

// resourcesEqual compares two ResourceRequirements
func resourcesEqual(current, desired corev1.ResourceRequirements) bool {
	return limitsEqual(current.Limits, desired.Limits) && limitsEqual(current.Requests, desired.Requests)
}

// limitsEqual compares two ResourceLists
func limitsEqual(current, desired corev1.ResourceList) bool {
	if len(current) != len(desired) {
		return false
	}
	for key, val := range current {
		if desiredVal, ok := desired[key]; !ok || val.Cmp(desiredVal) != 0 {
			return false
		}
	}
	return true
}

// probesEqual compares two Probe pointers
func probesEqual(current, desired *corev1.Probe) bool {
	if (current == nil) != (desired == nil) {
		return false
	}
	if current == nil {
		return true
	}

	// Compare probe settings
	if current.InitialDelaySeconds != desired.InitialDelaySeconds ||
		current.TimeoutSeconds != desired.TimeoutSeconds ||
		current.PeriodSeconds != desired.PeriodSeconds ||
		current.SuccessThreshold != desired.SuccessThreshold ||
		current.FailureThreshold != desired.FailureThreshold {
		return false
	}

	// Compare HTTPGet handler
	if (current.HTTPGet == nil) != (desired.HTTPGet == nil) {
		return false
	}
	if current.HTTPGet != nil && desired.HTTPGet != nil {
		if current.HTTPGet.Path != desired.HTTPGet.Path ||
			current.HTTPGet.Port != desired.HTTPGet.Port ||
			current.HTTPGet.Scheme != desired.HTTPGet.Scheme {
			return false
		}
	}

	return true
}

// securityContextsEqual compares the fields buildStatefulSet actually sets
// (currently just Privileged, for spec.alpha.devices) rather than doing a
// raw reflect.DeepEqual, matching the style of resourcesEqual/probesEqual.
func securityContextsEqual(current, desired *corev1.SecurityContext) bool {
	if (current == nil) != (desired == nil) {
		return false
	}
	if current == nil {
		return true
	}
	currentPrivileged := current.Privileged != nil && *current.Privileged
	desiredPrivileged := desired.Privileged != nil && *desired.Privileged
	return currentPrivileged == desiredPrivileged
}

// volumeContentDiffers compares Volume and VolumeMount content index-by-index
// across all of the pod template's volumes/mount. Callers must already have
// confirmed the volume and mount counts match. Split out of needsUpdate to keep
// its cyclomatic complexity in check.
func volumeContentDiffers(
	current, desired *appsv1.StatefulSet,
	currentContainer, desiredContainer corev1.Container,
) bool {
	log := logf.Log.WithName("needsUpdate")
	for i, cv := range current.Spec.Template.Spec.Volumes {
		dv := desired.Spec.Template.Spec.Volumes[i]
		if !equality.Semantic.DeepDerivative(cv, dv) {
			log.V(1).Info("Volume differs", "index", i, "name", dv.Name)
			return true
		}
	}

	for i, cm := range currentContainer.VolumeMounts {
		dm := desiredContainer.VolumeMounts[i]
		if !equality.Semantic.DeepDerivative(cm, dm) {
			log.V(1).Info("VolumeMount differs", "index", i)
			return true
		}
	}
	return false
}

// schedulingFieldsDiffer compares spec.scheduling's four fields
// (NodeSelector/Affinity/Tolerations/PriorityClassName) between the current
// and desired pod templates. Kubernetes only evaluates these at pod
// creation, so an edit here must trigger a rollout or it silently has no
// effect on the already-running pod. Split out of needsUpdate to keep its
// cyclomatic complexity in check, matching the volumeContentDiffers
// precedent.
func schedulingFieldsDiffer(current, desired *appsv1.StatefulSet) bool {
	currentSpec := current.Spec.Template.Spec
	desiredSpec := desired.Spec.Template.Spec

	if !maps.Equal(currentSpec.NodeSelector, desiredSpec.NodeSelector) {
		return true
	}
	// Affinity/Tolerations: DeepEqual, not a length+index loop — Toleration's
	// TolerationSeconds is a *int64, so comparing elements with == would
	// compare pointer identity, not the pointed-to value. Safe to DeepEqual
	// directly: unlike SchedulerName/container defaulting fields, neither the
	// API server nor any admission plugin mutates Affinity/Tolerations at
	// StatefulSet-persistence time, only at Pod-realization time.
	if !reflect.DeepEqual(currentSpec.Affinity, desiredSpec.Affinity) {
		return true
	}
	if !reflect.DeepEqual(currentSpec.Tolerations, desiredSpec.Tolerations) {
		return true
	}
	return currentSpec.PriorityClassName != desiredSpec.PriorityClassName
}

// SetupWithManager sets up the controller with the Manager.
func (r *HomeAssistantReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hav1.HomeAssistant{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Watches(
			&hav1.HomeAssistantConfiguration{},
			handler.EnqueueRequestsFromMapFunc(r.findHomeAssistantForConfiguration),
		).
		Watches(
			&hav1.HomeAssistantSecrets{},
			handler.EnqueueRequestsFromMapFunc(r.findHomeAssistantForSecrets),
		).
		Watches(
			&corev1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(r.findHomeAssistantForConfigMap),
		).
		Named("homeassistant").
		Complete(r)
}

// findHomeAssistantForConfiguration finds the HomeAssistant that is referenced by a
// HomeAssistantConfiguration
func (r *HomeAssistantReconciler) findHomeAssistantForConfiguration(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	haConfig := obj.(*hav1.HomeAssistantConfiguration)

	// Return reconcile request for the referenced HomeAssistant
	return []reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Name:      haConfig.Spec.HomeAssistantRef.Name,
				Namespace: haConfig.Namespace,
			},
		},
	}
}

// findHomeAssistantForSecrets finds the HomeAssistant that is referenced by a
// HomeAssistantSecrets
func (r *HomeAssistantReconciler) findHomeAssistantForSecrets(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	haSecrets := obj.(*hav1.HomeAssistantSecrets)

	// Return reconcile request for the referenced HomeAssistant
	return []reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Name:      haSecrets.Spec.HomeAssistantRef.Name,
				Namespace: haSecrets.Namespace,
			},
		},
	}
}

// findHomeAssistantForConfigMap finds the HomeAssistant associated with a ConfigMap
// This triggers reconciliation when configuration ConfigMap is updated (e.g., config-hash annotation)
func (r *HomeAssistantReconciler) findHomeAssistantForConfigMap(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	configMap := obj.(*corev1.ConfigMap)

	// Only watch ConfigMaps with the configuration suffix (generated by HomeAssistantConfiguration)
	if !strings.HasSuffix(configMap.Name, "-configuration") {
		return nil
	}

	// Extract HomeAssistant name from ConfigMap name (remove "-configuration" suffix)
	haName := strings.TrimSuffix(configMap.Name, "-configuration")

	return []reconcile.Request{
		{
			NamespacedName: types.NamespacedName{
				Name:      haName,
				Namespace: configMap.Namespace,
			},
		},
	}
}

// buildInitContainers returns the init containers for the Home Assistant pod.
// Order matters: config-init runs first, then unban-operator-ip (if POD_IP is set),
// then community-repository-init (if any HomeAssistantCommunityRepository targets
// this instance — the "integration" category needs its files in place before HA's
// Python process starts importing components).
func (r *HomeAssistantReconciler) buildInitContainers(
	ctx context.Context, ha *hav1.HomeAssistant,
) ([]corev1.Container, error) {
	repo := defaultInitRepository
	img := defaultInitImage
	tag := defaultInitTag

	if ha.Spec.Storage != nil && ha.Spec.Storage.InitContainer != nil {
		ic := ha.Spec.Storage.InitContainer
		if ic.Repository != "" {
			repo = ic.Repository
		}
		if ic.Image != "" {
			img = ic.Image
		}
		if ic.Tag != "" {
			tag = ic.Tag
		}
	}

	fullImage := fmt.Sprintf("%s/%s:%s", repo, img, tag)

	containers := []corev1.Container{
		{
			Name:            "config-init",
			Image:           fullImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         []string{"sh", "-c"},
			Args: []string{
				"set -e; " +
					"for f in automations.yaml scenes.yaml scripts.yaml; do " +
					"[ -f /config/$f ] || echo '[]' > /config/$f; " +
					"done; " +
					// Home Assistant's python_script component aborts its own setup
					// (never registering the python_script.reload service) if
					// /config/python_scripts doesn't exist at HA's very first start —
					// see homeassistant/components/python_script/__init__.py's setup().
					// A HomeAssistantCommunityRepository of that category only creates
					// the directory later, well after HA has already booted (that
					// category never restarts the pod), so without this the reload
					// service would never exist for the process's lifetime. Pre-create
					// it unconditionally, independent of whether any community
					// repository CR exists, so "python_script:" in the user's own
					// configuration.yaml always works.
					"mkdir -p /config/python_scripts",
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "config",
					MountPath: "/config",
				},
			},
		},
	}

	if os.Getenv("POD_IP") != "" {
		containers = append(containers, r.buildUnbanInitContainer(ha))
	}

	hasCR, err := hasCommunityRepositories(ctx, r.Client, ha)
	if err != nil {
		return nil, fmt.Errorf("failed to check for HomeAssistantCommunityRepository resources: %w", err)
	}
	if hasCR {
		containers = append(containers, r.buildCommunityRepositoryInitContainer(ha))
	}

	return containers, nil
}

// buildUnbanInitContainer returns an init-container that removes the operator's IP
// from /config/ip_bans.yaml before HA starts. It uses the same HA image (already
// cached on the node) so no additional image pull is required.
// The script is idempotent: if the file is absent or the IP is not listed, it exits 0.
//
// The IP is injected at runtime via the OPERATOR_IP env var (sourced from the
// <ha-name>-operator-ip ConfigMap) rather than being embedded in the pod template.
// This keeps the StatefulSet spec stable across operator pod restarts so that a new
// operator IP triggers only a ConfigMap update, not an HA rolling restart.
//
// Requirement: the image used must provide python3 and PyYAML. All official Home
// Assistant images satisfy this (HA is Python-based). If spec.image is set to a
// custom image it must also include these dependencies, otherwise the init-container
// will fail and the HA pod will not start.
func (r *HomeAssistantReconciler) buildUnbanInitContainer(ha *hav1.HomeAssistant) corev1.Container {
	image := defaultImage
	if ha.Spec.Image != "" {
		image = ha.Spec.Image
	}
	version := defaultVersion
	if ha.Spec.Version != "" {
		version = ha.Spec.Version
	}
	haImage := fmt.Sprintf("%s:%s", image, version)

	// OPERATOR_IP is resolved at pod-start time from the ConfigMap, so the
	// StatefulSet template is stable regardless of operator IP changes.
	script := `
import yaml, os, sys
ip = os.environ.get('OPERATOR_IP', '')
if not ip:
    sys.exit(0)
path = os.environ.get('UNBAN_IP_BANS_PATH', '/config/ip_bans.yaml')
if not os.path.exists(path):
    sys.exit(0)
try:
    with open(path) as f:
        content = f.read()
except OSError:
    sys.exit(0)
content = content.strip()
if not content or content == '{}':
    sys.exit(0)
while content.startswith('{}'):
    content = content[2:].strip()
if not content:
    sys.exit(0)
try:
    d = yaml.safe_load(content) or {}
except yaml.YAMLError:
    sys.exit(0)
if ip not in d:
    sys.exit(0)
del d[ip]
with open(path, 'w') as f:
    yaml.dump(d, f, default_flow_style=False)
print('removed operator IP from ip_bans.yaml')
`

	return corev1.Container{
		Name:            "unban-operator-ip",
		Image:           haImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"python3", "-c", script},
		Env: []corev1.EnvVar{
			{
				Name: "OPERATOR_IP",
				ValueFrom: &corev1.EnvVarSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: ha.Name + operatorIPConfigMapSuffix,
						},
						Key:      operatorIPConfigMapKey,
						Optional: func() *bool { v := true; return &v }(), // pod starts even if CM absent
					},
				},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "config",
				MountPath: "/config",
			},
		},
	}
}
