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
	"reflect"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
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

	// Labels
	labelAppName      = "app.kubernetes.io/name"
	labelAppInstance  = "app.kubernetes.io/instance"
	labelAppManagedBy = "app.kubernetes.io/managed-by"

	// Condition types
	conditionTypeReady = "Ready"

	// Self-unban limits
	selfUnbanMaxCount    = 5
	selfUnbanCooldown    = 5 * time.Minute
	selfUnbanRequeueWait = 2 * time.Minute
)

// HomeAssistantReconciler reconciles a HomeAssistant object
type HomeAssistantReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Recorder   events.EventRecorder
	RestConfig *rest.Config

	// NewHAClient overrides the default haclient constructor (for testing)
	NewHAClient func(baseURL string) *haclient.Client

	// lastConfigHashSync tracks last time config hash was synced from ConfigMap
	// Used for debouncing to avoid rapid StatefulSet updates
	// sync.Map is used because reconcilers run concurrently for different resources
	lastConfigHashSync sync.Map // map[string]time.Time
}

// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistants,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistants/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistants/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=core,resources=pods/exec,verbs=create
// NOTE: pods/exec is granted cluster-wide because Kubebuilder generates a
// single ClusterRole. Users with strict security requirements should manually
// replace the generated ClusterRoleBinding with a RoleBinding scoped to the
// Home Assistant namespace so exec is only permitted on HA pods.
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *HomeAssistantReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the HomeAssistant instance
	ha := &hav1alpha1.HomeAssistant{}
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
		ha.Status.Phase = hav1alpha1.PhasePending
		if err := r.Status().Update(ctx, ha); err != nil {
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
		ha.Status.Phase = hav1alpha1.PhasePending
		ha.Status.Ready = false
		// Update status to indicate we're waiting
		condition := metav1.Condition{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: ha.Generation,
			Reason:             "WaitingForConfiguration",
			Message:            "Waiting for HomeAssistantConfiguration to be created",
		}
		meta.SetStatusCondition(&ha.Status.Conditions, condition)
		if err := r.Status().Update(ctx, ha); err != nil {
			log.Error(err, "Failed to update status")
		}
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}

	// Reconcile PVC
	if err := r.reconcilePVC(ctx, ha); err != nil {
		log.Error(err, "Failed to reconcile PVC")
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
	return r.updateStatusFromStatefulSet(ctx, ha)
}

// reconcilePVC ensures the PVC exists for Home Assistant data
func (r *HomeAssistantReconciler) reconcilePVC(ctx context.Context, ha *hav1alpha1.HomeAssistant) error {
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
func (r *HomeAssistantReconciler) buildPVC(ha *hav1alpha1.HomeAssistant, name string) *corev1.PersistentVolumeClaim {
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
func (r *HomeAssistantReconciler) reconcileStatefulSet(ctx context.Context, ha *hav1alpha1.HomeAssistant) error {
	log := logf.FromContext(ctx)

	sts := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: ha.Name, Namespace: ha.Namespace}, sts)

	if err != nil && errors.IsNotFound(err) {
		// Create new StatefulSet
		sts = r.buildStatefulSet(ctx, ha)

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
	desired := r.buildStatefulSet(ctx, ha)

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
	ha *hav1alpha1.HomeAssistant,
) (string, error) {
	log := logf.FromContext(ctx)

	// List all HomeAssistantConfigurations in the same namespace
	haConfigList := &hav1alpha1.HomeAssistantConfigurationList{}
	if err := r.List(ctx, haConfigList, client.InNamespace(ha.Namespace)); err != nil {
		return "", err
	}

	// Find HomeAssistantConfiguration that references this HomeAssistant
	for _, haConfig := range haConfigList.Items {
		if haConfig.Spec.HomeAssistantRef.Name == ha.Name {
			// Found a HomeAssistantConfiguration for this HA
			generatedConfigMapName := ha.Name + "-configuration"
			log.V(1).Info("Found HomeAssistantConfiguration, using generated ConfigMap", "configmap", generatedConfigMapName)
			return generatedConfigMapName, nil
		}
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
	ha *hav1alpha1.HomeAssistant,
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
	ha *hav1alpha1.HomeAssistant,
) (string, error) {
	log := logf.FromContext(ctx)

	// List all HomeAssistantSecrets in the same namespace
	haSecretsList := &hav1alpha1.HomeAssistantSecretsList{}
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
	ha *hav1alpha1.HomeAssistant,
) *appsv1.StatefulSet {
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
					InitContainers: r.buildInitContainers(ha),
					Containers: []corev1.Container{
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
										Scheme: corev1.URISchemeHTTP,
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
										Scheme: corev1.URISchemeHTTP,
									},
								},
								InitialDelaySeconds: 10,
								PeriodSeconds:       5,
								TimeoutSeconds:      3,
								SuccessThreshold:    1,
								FailureThreshold:    3,
							},
						},
					},
					Volumes: volumes,
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

	return sts
}

// reconcileService ensures the Service exists and is up to date
func (r *HomeAssistantReconciler) reconcileService(ctx context.Context, ha *hav1alpha1.HomeAssistant) error {
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
func (r *HomeAssistantReconciler) buildService(ha *hav1alpha1.HomeAssistant) *corev1.Service {
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

// labelsForHomeAssistant returns the labels for selecting resources belonging to the given HomeAssistant CR
func (r *HomeAssistantReconciler) labelsForHomeAssistant(ha *hav1alpha1.HomeAssistant) map[string]string {
	return map[string]string{
		labelAppName:      "homeassistant",
		labelAppInstance:  ha.Name,
		labelAppManagedBy: "homeassistant-operator",
	}
}

// updateStatusFailed updates the status when reconciliation fails
func (r *HomeAssistantReconciler) updateStatusFailed(
	ctx context.Context,
	ha *hav1alpha1.HomeAssistant,
	reconcileErr error,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	ha.Status.Phase = hav1alpha1.PhaseFailed
	ha.Status.Ready = false
	ha.Status.ObservedGeneration = ha.Generation

	meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             "ReconciliationFailed",
		Message:            reconcileErr.Error(),
		ObservedGeneration: ha.Generation,
	})

	if err := r.Status().Update(ctx, ha); err != nil {
		log.Error(err, "Failed to update HomeAssistant status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, reconcileErr
}

// updateStatusFromStatefulSet updates the status based on StatefulSet state
func (r *HomeAssistantReconciler) updateStatusFromStatefulSet(
	ctx context.Context,
	ha *hav1alpha1.HomeAssistant,
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
	ha.Status.Version = version
	ha.Status.ObservedGeneration = ha.Generation

	// Check if StatefulSet is ready
	if sts.Status.ReadyReplicas > 0 && sts.Status.ReadyReplicas == sts.Status.Replicas {
		ha.Status.Phase = hav1alpha1.PhaseRunning
		ha.Status.Ready = true

		meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
			Type:               conditionTypeReady,
			Status:             metav1.ConditionTrue,
			Reason:             "StatefulSetReady",
			Message:            "Home Assistant is running",
			ObservedGeneration: ha.Generation,
		})
	} else {
		ha.Status.Phase = hav1alpha1.PhasePending
		ha.Status.Ready = false

		meta.SetStatusCondition(&ha.Status.Conditions, metav1.Condition{
			Type:   conditionTypeReady,
			Status: metav1.ConditionFalse,
			Reason: "StatefulSetNotReady",
			Message: fmt.Sprintf(
				"Waiting for StatefulSet to be ready (%d/%d)",
				sts.Status.ReadyReplicas, sts.Status.Replicas,
			),
			ObservedGeneration: ha.Generation,
		})
	}

	if err := r.Status().Update(ctx, ha); err != nil {
		log.Error(err, "Failed to update HomeAssistant status")
		return ctrl.Result{}, err
	}

	// Requeue if not ready yet
	if !ha.Status.Ready {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

// needsUpdate checks if the StatefulSet needs to be updated
func needsUpdate(current, desired *appsv1.StatefulSet) bool {
	log := logf.Log.WithName("needsUpdate")

	// Ensure containers exist
	if len(current.Spec.Template.Spec.Containers) == 0 || len(desired.Spec.Template.Spec.Containers) == 0 {
		if len(current.Spec.Template.Spec.Containers) != len(desired.Spec.Template.Spec.Containers) {
			log.V(1).Info("Container count differs",
				"current", len(current.Spec.Template.Spec.Containers),
				"desired", len(desired.Spec.Template.Spec.Containers))
			return true
		}
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

	// Check init containers (e.g. config-init image/tag changes)
	currentInit := current.Spec.Template.Spec.InitContainers
	desiredInit := desired.Spec.Template.Spec.InitContainers
	if len(currentInit) == 0 {
		currentInit = nil
	}
	if len(desiredInit) == 0 {
		desiredInit = nil
	}
	if !reflect.DeepEqual(currentInit, desiredInit) {
		log.V(1).Info("InitContainers differ",
			"current", len(currentInit),
			"desired", len(desiredInit))
		return true
	}

	// Check host networking
	if current.Spec.Template.Spec.HostNetwork != desired.Spec.Template.Spec.HostNetwork {
		log.V(1).Info("HostNetwork differs",
			"current", current.Spec.Template.Spec.HostNetwork,
			"desired", desired.Spec.Template.Spec.HostNetwork)
		return true
	}
	if current.Spec.Template.Spec.DNSPolicy != desired.Spec.Template.Spec.DNSPolicy {
		log.V(1).Info("DNSPolicy differs",
			"current", current.Spec.Template.Spec.DNSPolicy,
			"desired", desired.Spec.Template.Spec.DNSPolicy)
		return true
	}

	return false
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

// SetupWithManager sets up the controller with the Manager.
func (r *HomeAssistantReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hav1alpha1.HomeAssistant{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Watches(
			&hav1alpha1.HomeAssistantConfiguration{},
			handler.EnqueueRequestsFromMapFunc(r.findHomeAssistantForConfiguration),
		).
		Watches(
			&hav1alpha1.HomeAssistantSecrets{},
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
	haConfig := obj.(*hav1alpha1.HomeAssistantConfiguration)

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
	haSecrets := obj.(*hav1alpha1.HomeAssistantSecrets)

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
// The config-init container pre-creates required YAML files on the PVC so that
// HA does not enter recovery mode on first start due to missing !include targets.
func (r *HomeAssistantReconciler) buildInitContainers(ha *hav1alpha1.HomeAssistant) []corev1.Container {
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

	return []corev1.Container{
		{
			Name:            "config-init",
			Image:           fullImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         []string{"sh", "-c"},
			Args: []string{
				"set -e; " +
					"for f in automations.yaml scenes.yaml scripts.yaml; do " +
					"[ -f /config/$f ] || echo '[]' > /config/$f; " +
					"done",
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "config",
					MountPath: "/config",
				},
			},
		},
	}
}
