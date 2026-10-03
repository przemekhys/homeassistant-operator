package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

const (
	dashboardFinalizer = "ha.homeassistant.io/dashboard-finalizer"
	dashboardRetry     = 30 * time.Second
)

// HomeAssistantDashboardReconciler manages storage-mode Lovelace dashboards.
type HomeAssistantDashboardReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	NewHAClient func(baseURL string) *haclient.Client
}

// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantdashboards,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantdashboards/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistantdashboards/finalizers,verbs=update
// +kubebuilder:rbac:groups=ha.homeassistant.io,resources=homeassistants,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

func (r *HomeAssistantDashboardReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	dashboard := &hav1alpha1.HomeAssistantDashboard{}
	if err := r.Get(ctx, req.NamespacedName, dashboard); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !dashboard.DeletionTimestamp.IsZero() {
		return r.reconcileDeletion(ctx, dashboard)
	}
	if !controllerutil.ContainsFinalizer(dashboard, dashboardFinalizer) {
		controllerutil.AddFinalizer(dashboard, dashboardFinalizer)
		return ctrl.Result{}, r.Update(ctx, dashboard)
	}

	config, err := r.resolveConfig(ctx, dashboard)
	if err != nil {
		return r.failed(ctx, dashboard, "SourceInvalid", err.Error())
	}
	haRef := types.NamespacedName{Name: dashboard.Spec.HomeAssistantRef.Name, Namespace: dashboard.Namespace}
	ha, err := getHomeAssistant(ctx, r.Client, haRef)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.failed(ctx, dashboard, "HomeAssistantNotFound", "referenced HomeAssistant was not found")
		}
		return ctrl.Result{}, err
	}
	token, err := getAPIToken(ctx, r.Client, ha)
	if err != nil {
		return r.failed(ctx, dashboard, "TokenNotAvailable", "Home Assistant API token is not available")
	}

	path := dashboard.Spec.URLPath
	if dashboard.Spec.DefaultDashboard {
		path = "lovelace"
	}
	metadata := haclient.Dashboard{
		URLPath: path, Title: dashboard.Spec.Title, Icon: dashboard.Spec.Icon,
		ShowInSidebar:   dashboard.Spec.ShowInSidebar == nil || *dashboard.Spec.ShowInSidebar,
		RequireAdmin:    dashboard.Spec.RequireAdmin != nil && *dashboard.Spec.RequireAdmin,
		AllowSingleWord: dashboard.Spec.DefaultDashboard,
	}
	hash := dashboardHash(config, metadata)
	haClient := r.haClientFor(ha)
	dashboards, err := haClient.ListDashboards(ctx, token)
	if err != nil {
		return r.failed(ctx, dashboard, "HomeAssistantUnavailable", fmt.Sprintf("failed to list dashboards: %v", err))
	}
	var existing *haclient.Dashboard
	for i := range dashboards {
		if dashboards[i].URLPath == path {
			existing = &dashboards[i]
			break
		}
	}
	created := false
	if existing == nil {
		existing, err = haClient.CreateDashboard(ctx, token, metadata)
		if err != nil {
			return r.failed(ctx, dashboard, "DashboardWriteFailed", fmt.Sprintf("failed to create dashboard: %v", err))
		}
		created = true
	} else if existing.Mode != haclient.DashboardModeStorage {
		return r.failed(ctx, dashboard, "DashboardNotStorageManaged", "dashboard is not storage-managed")
	} else if dashboard.Status.SourceHash != hash {
		existing, err = haClient.UpdateDashboard(ctx, token, existing.ID, metadata)
		if err != nil {
			return r.failed(ctx, dashboard, "DashboardWriteFailed", fmt.Sprintf("failed to update dashboard: %v", err))
		}
	}
	if created {
		if err := haClient.SaveDashboardConfig(ctx, token, path, config); err != nil {
			return r.failed(ctx, dashboard, "DashboardWriteFailed", fmt.Sprintf("failed to save dashboard: %v", err))
		}
	} else {
		actualConfig, err := haClient.GetDashboardConfig(ctx, token, path)
		if err != nil {
			return r.failed(ctx, dashboard, "HomeAssistantUnavailable", fmt.Sprintf("failed to get dashboard config: %v", err))
		}
		configMatches, err := dashboardConfigsEqual(config, actualConfig)
		if err != nil {
			return r.failed(ctx, dashboard, "HomeAssistantUnavailable",
				fmt.Sprintf("failed to compare dashboard config: %v", err))
		}
		if !configMatches {
			if err := haClient.SaveDashboardConfig(ctx, token, path, config); err != nil {
				return r.failed(ctx, dashboard, "DashboardWriteFailed", fmt.Sprintf("failed to save dashboard: %v", err))
			}
		}
	}
	dashboard.Status.DashboardID = existing.ID
	dashboard.Status.URLPath = path
	dashboard.Status.SourceHash = hash
	dashboard.Status.LastError = ""
	dashboard.Status.ObservedGeneration = dashboard.Generation
	meta.SetStatusCondition(&dashboard.Status.Conditions, metav1.Condition{
		Type: conditionTypeReady, Status: metav1.ConditionTrue, Reason: "DashboardConfigured",
		Message: "Dashboard is configured", ObservedGeneration: dashboard.Generation,
	})
	return ctrl.Result{}, r.Status().Update(ctx, dashboard)
}

func (r *HomeAssistantDashboardReconciler) haClientFor(ha *hav1.HomeAssistant) *haclient.Client {
	return newHAClientForHA(ha, r.NewHAClient)
}

func (r *HomeAssistantDashboardReconciler) resolveConfig(
	ctx context.Context, dashboard *hav1alpha1.HomeAssistantDashboard,
) (json.RawMessage, error) {
	content := dashboard.Spec.Inline
	if dashboard.Spec.ConfigMapKeyRef != nil {
		cm := &corev1.ConfigMap{}
		ref := types.NamespacedName{Name: dashboard.Spec.ConfigMapKeyRef.Name, Namespace: dashboard.Namespace}
		if err := r.Get(ctx, ref, cm); err != nil {
			return nil, fmt.Errorf("failed to get ConfigMap %s: %w", ref.Name, err)
		}
		var ok bool
		content, ok = cm.Data[dashboard.Spec.ConfigMapKeyRef.Key]
		if !ok {
			return nil, fmt.Errorf("key %q not found in ConfigMap %s", dashboard.Spec.ConfigMapKeyRef.Key, cm.Name)
		}
	}
	config, err := yaml.YAMLToJSON([]byte(content))
	if err != nil {
		return nil, fmt.Errorf("invalid dashboard YAML: %w", err)
	}
	return config, nil
}

func dashboardHash(config json.RawMessage, metadata haclient.Dashboard) string {
	b, _ := json.Marshal(struct {
		Config    json.RawMessage
		Dashboard haclient.Dashboard
	}{config, metadata})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func dashboardConfigsEqual(desired, actual json.RawMessage) (bool, error) {
	var desiredValue, actualValue interface{}
	if err := json.Unmarshal(desired, &desiredValue); err != nil {
		return false, fmt.Errorf("decode desired config: %w", err)
	}
	if err := json.Unmarshal(actual, &actualValue); err != nil {
		return false, fmt.Errorf("decode actual config: %w", err)
	}
	return reflect.DeepEqual(desiredValue, actualValue), nil
}

func (r *HomeAssistantDashboardReconciler) failed(
	ctx context.Context, dashboard *hav1alpha1.HomeAssistantDashboard, reason, message string,
) (ctrl.Result, error) {
	dashboard.Status.LastError = message
	dashboard.Status.ObservedGeneration = dashboard.Generation
	meta.SetStatusCondition(&dashboard.Status.Conditions, metav1.Condition{
		Type: conditionTypeReady, Status: metav1.ConditionFalse, Reason: reason,
		Message: message, ObservedGeneration: dashboard.Generation,
	})
	if err := r.Status().Update(ctx, dashboard); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: dashboardRetry}, nil
}

func (r *HomeAssistantDashboardReconciler) reconcileDeletion(
	ctx context.Context, dashboard *hav1alpha1.HomeAssistantDashboard,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(dashboard, dashboardFinalizer) || dashboard.Status.DashboardID == "" {
		controllerutil.RemoveFinalizer(dashboard, dashboardFinalizer)
		return ctrl.Result{}, r.Update(ctx, dashboard)
	}
	haRef := types.NamespacedName{Name: dashboard.Spec.HomeAssistantRef.Name, Namespace: dashboard.Namespace}
	ha, err := getHomeAssistant(ctx, r.Client, haRef)
	if err != nil {
		return ctrl.Result{RequeueAfter: dashboardRetry}, nil
	}
	token, err := getAPIToken(ctx, r.Client, ha)
	if err != nil {
		return ctrl.Result{RequeueAfter: dashboardRetry}, nil
	}
	if err := r.haClientFor(ha).DeleteDashboard(ctx, token, dashboard.Status.DashboardID); err != nil {
		return ctrl.Result{RequeueAfter: dashboardRetry}, nil
	}
	controllerutil.RemoveFinalizer(dashboard, dashboardFinalizer)
	return ctrl.Result{}, r.Update(ctx, dashboard)
}

func (r *HomeAssistantDashboardReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hav1alpha1.HomeAssistantDashboard{}).
		Watches(&corev1.ConfigMap{}, handler.Funcs{
			CreateFunc: func(
				ctx context.Context,
				e event.TypedCreateEvent[client.Object],
				q workqueue.TypedRateLimitingInterface[reconcile.Request],
			) {
				for _, request := range r.findDashboardsForConfigMap(ctx, e.Object) {
					q.Add(request)
				}
			},
			UpdateFunc: func(
				ctx context.Context,
				e event.TypedUpdateEvent[client.Object],
				q workqueue.TypedRateLimitingInterface[reconcile.Request],
			) {
				for _, request := range r.findDashboardsForChangedConfigMap(ctx, e.ObjectOld, e.ObjectNew) {
					q.Add(request)
				}
			},
			DeleteFunc: func(
				ctx context.Context,
				e event.TypedDeleteEvent[client.Object],
				q workqueue.TypedRateLimitingInterface[reconcile.Request],
			) {
				for _, request := range r.findDashboardsForConfigMap(ctx, e.Object) {
					q.Add(request)
				}
			},
			GenericFunc: func(
				ctx context.Context,
				e event.TypedGenericEvent[client.Object],
				q workqueue.TypedRateLimitingInterface[reconcile.Request],
			) {
				for _, request := range r.findDashboardsForConfigMap(ctx, e.Object) {
					q.Add(request)
				}
			},
		}).
		Named("homeassistantdashboard").
		Complete(r)
}

func (r *HomeAssistantDashboardReconciler) findDashboardsForConfigMap(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	cm := obj.(*corev1.ConfigMap)
	list := &hav1alpha1.HomeAssistantDashboardList{}
	if err := r.List(ctx, list, client.InNamespace(cm.Namespace)); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for _, dashboard := range list.Items {
		if ref := dashboard.Spec.ConfigMapKeyRef; ref != nil && ref.Name == cm.Name {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&dashboard)})
		}
	}
	return requests
}

func (r *HomeAssistantDashboardReconciler) findDashboardsForChangedConfigMap(
	ctx context.Context, oldObj, newObj client.Object,
) []reconcile.Request {
	oldCM, ok := oldObj.(*corev1.ConfigMap)
	if !ok {
		return r.findDashboardsForConfigMap(ctx, newObj)
	}
	newCM, ok := newObj.(*corev1.ConfigMap)
	if !ok {
		return r.findDashboardsForConfigMap(ctx, oldObj)
	}
	list := &hav1alpha1.HomeAssistantDashboardList{}
	if err := r.List(ctx, list, client.InNamespace(newCM.Namespace)); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for _, dashboard := range list.Items {
		ref := dashboard.Spec.ConfigMapKeyRef
		if ref == nil || ref.Name != newCM.Name {
			continue
		}
		oldValue, oldOK := oldCM.Data[ref.Key]
		newValue, newOK := newCM.Data[ref.Key]
		if oldOK != newOK || oldValue != newValue {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&dashboard)})
		}
	}
	return requests
}
