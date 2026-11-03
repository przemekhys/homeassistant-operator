package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/coordination"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

// Each denied case has an Open counterpart that must actually reach a writer.
// This prevents missing credentials, invalid fixtures or finalizer-only passes
// from being mistaken for proof that synchronization is gated.
func TestSynchronizationAPIWriteMatrix(t *testing.T) {
	for _, kind := range []string{"automation", "scene", "script", "integration", "floor", "label", "area", "dashboard"} {
		for _, operation := range []string{"normal", "rename", "delete"} {
			for _, gate := range []string{"Open", "ManualPause", "Paused", "RestoreHold", "UnknownPermit", "MissingEvidence"} {
				t.Run(kind+"/"+operation+"/"+gate, func(t *testing.T) {
					runSynchronizationAPICase(t, kind, operation, gate)
				})
			}
		}
	}
}

// Configuration and generated-secret delivery write Kubernetes objects rather
// than the HA API. They must therefore use the same durable admission boundary
// as API-backed children; a cached paused condition alone is not sufficient.
func TestSynchronizationGeneratedSourceWriteMatrix(t *testing.T) {
	for _, kind := range []string{"configuration", "secrets"} {
		for _, gate := range []string{"Open", "ManualPause", "Paused", "RestoreHold", "UnknownPermit", "MissingEvidence"} {
			t.Run(kind+"/normal/"+gate, func(t *testing.T) {
				runSynchronizationGeneratedSourceCase(t, kind, gate)
			})
		}
	}
}

// Dedicated-account provisioning begins by creating its durable token ledger.
// The ledger is an ordinary write and must not be created while a pause, restore
// hold, unknown writer, or missing coordinator prevents ordinary work.
func TestSynchronizationBootstrapAccountWriteMatrix(t *testing.T) {
	for _, gate := range []string{"Open", "ManualPause", "Paused", "RestoreHold", "UnknownPermit", "MissingEvidence"} {
		t.Run("bootstrap-account/normal/"+gate, func(t *testing.T) {
			runSynchronizationBootstrapAccountCase(t, gate)
		})
	}
}

func TestSynchronizationHTTPConfigWriteMatrix(t *testing.T) {
	for _, gate := range []string{"Open", "ManualPause", "Paused", "RestoreHold", "UnknownPermit", "MissingEvidence"} {
		t.Run("http-config/normal/"+gate, func(t *testing.T) {
			runSynchronizationHTTPConfigCase(t, gate)
		})
	}
}

func TestSynchronizationBackupWriteMatrix(t *testing.T) {
	for _, gate := range []string{"Open", "ManualPause", "Paused", "RestoreHold", "UnknownPermit", "MissingEvidence"} {
		t.Run("backup/normal/"+gate, func(t *testing.T) {
			runSynchronizationBackupCase(t, gate)
		})
	}
}

func runSynchronizationBackupCase(t *testing.T, gate string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, hav1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{
		Name: "home", Namespace: "default", UID: types.UID("backup-config-instance"),
	}, Spec: hav1.HomeAssistantSpec{Backup: &hav1.BackupSpec{Enabled: true, Recurrence: "daily"}}, Status: hav1.HomeAssistantStatus{
		Bootstrap: &hav1.BootstrapStatus{APITokenSecretName: "matrix-token"},
	}}
	decodeSynchronizationFixture(t, []byte(`{"spec":{"alpha":{"coordinatedSynchronization":{"enabled":true}}}}`), ha)
	if gate == "ManualPause" {
		ha.Spec.Synchronization = &hav1.SynchronizationSpec{Paused: true}
	}
	token := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "matrix-token", Namespace: "default"}, Data: map[string][]byte{"token": []byte("synthetic-matrix-token")}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ha).WithObjects(ha, token).Build()
	store := coordination.Store{Reader: kube, Client: kube}
	if gate != "MissingEvidence" {
		if _, err := store.CreateRoot(ctx, ha.Namespace, ha.UID); err != nil {
			t.Fatal(err)
		}
		_, err := store.UpdateRoot(ctx, ha.Namespace, ha.UID, func(root *coordination.Root) error {
			switch gate {
			case "Paused":
				root.Mode = "Paused"
			case "RestoreHold":
				root.RestoreHold = &coordination.Hold{OperationUID: "restore-uid", Reason: "Verifying"}
			case "UnknownPermit":
				root.ActivePermit = &coordination.Permit{ID: "uncertain-writer", Owner: "old-process", Class: "Ordinary", State: coordination.PermitUnknown}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	api := &synchronizationMatrixAPI{operation: "normal"}
	server := httptest.NewServer(api)
	defer server.Close()
	r := &HomeAssistantReconciler{
		Client: kube, Scheme: scheme, Coordination: CoordinationDependencies{APIReader: kube, Store: store},
		NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
	}
	_, err := r.reconcileBackupConfig(ctx, ha)
	if gate == "MissingEvidence" {
		if err == nil {
			t.Fatal("missing durable coordination evidence did not block backup configuration")
		}
	} else if err != nil {
		t.Fatalf("backup reconciliation returned an unexpected error: %v", err)
	}
	writes := api.writtenCommands()
	if gate == "Open" {
		if !strings.Contains(strings.Join(writes, "\n"), "backup/config/update") {
			t.Fatalf("Open fixture did not update backup settings: %v", writes)
		}
		return
	}
	if strings.Contains(strings.Join(writes, "\n"), "backup/config/update") {
		t.Errorf("ordinary backup configuration write escaped %s gate: %v", gate, writes)
	}
}

func runSynchronizationHTTPConfigCase(t *testing.T, gate string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, hav1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{
		Name: "home", Namespace: "default", UID: types.UID("http-config-instance"),
	}}
	decodeSynchronizationFixture(t, []byte(`{"spec":{"alpha":{"coordinatedSynchronization":{"enabled":true}}}}`), ha)
	if gate == "ManualPause" {
		ha.Spec.Synchronization = &hav1.SynchronizationSpec{Paused: true}
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ha).Build()
	store := coordination.Store{Reader: kube, Client: kube}
	if gate != "MissingEvidence" {
		if _, err := store.CreateRoot(ctx, ha.Namespace, ha.UID); err != nil {
			t.Fatal(err)
		}
		_, err := store.UpdateRoot(ctx, ha.Namespace, ha.UID, func(root *coordination.Root) error {
			switch gate {
			case "Paused":
				root.Mode = "Paused"
			case "RestoreHold":
				root.RestoreHold = &coordination.Hold{OperationUID: "restore-uid", Reason: "Verifying"}
			case "UnknownPermit":
				root.ActivePermit = &coordination.Permit{ID: "uncertain-writer", Owner: "old-process", Class: "Ordinary", State: coordination.PermitUnknown}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	api := &synchronizationMatrixAPI{operation: "normal"}
	server := httptest.NewServer(api)
	defer server.Close()
	r := &HomeAssistantConfigurationReconciler{
		Client: kube, Scheme: scheme,
		Coordination: CoordinationDependencies{APIReader: kube, Store: store},
		NewHAClient:  func(string) *haclient.Client { return haclient.NewClient(server.URL) },
	}
	config := &hav1.HomeAssistantConfiguration{ObjectMeta: metav1.ObjectMeta{Generation: 1}}
	result := r.reconcileHTTPConfig(ctx, config, ha, httpConfigDecision{
		path: httpPathAPI, token: "synthetic-matrix-token",
		resp: &haclient.HTTPConfigResponse{Default: haclient.HTTPConfigData{}, Stable: haclient.HTTPConfigData{}},
	}, haclient.HTTPConfigData{"use_x_forwarded_for": true}, true)
	writes := api.writtenCommands()
	if gate == "Open" {
		if !strings.Contains(strings.Join(writes, "\n"), "http/config/configure") {
			t.Fatalf("Open fixture did not configure http settings: %v", writes)
		}
		return
	}
	if len(writes) != 0 {
		t.Errorf("ordinary HTTP configuration write escaped %s gate: %v", gate, writes)
	}
	if result.RequeueAfter <= 0 {
		t.Error("blocked HTTP configuration must schedule another observation")
	}
}

func runSynchronizationBootstrapAccountCase(t *testing.T, gate string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, hav1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{
		Name: "home", Namespace: "default", UID: types.UID("bootstrap-account-instance"),
	}, Spec: hav1.HomeAssistantSpec{Bootstrap: &hav1.BootstrapSpec{
		Enabled: true,
		OperatorAccount: &hav1.OperatorAccountSpec{
			Mode: "dedicated", TokenSecretName: "matrix-operator-token",
		},
	}}}
	decodeSynchronizationFixture(t, []byte(`{"spec":{"alpha":{"coordinatedSynchronization":{"enabled":true}}}}`), ha)
	if gate == "ManualPause" {
		ha.Spec.Synchronization = &hav1.SynchronizationSpec{Paused: true}
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ha).WithObjects(ha).Build()
	store := coordination.Store{Reader: kube, Client: kube}
	if gate != "MissingEvidence" {
		if _, err := store.CreateRoot(ctx, ha.Namespace, ha.UID); err != nil {
			t.Fatal(err)
		}
		_, err := store.UpdateRoot(ctx, ha.Namespace, ha.UID, func(root *coordination.Root) error {
			switch gate {
			case "Paused":
				root.Mode = "Paused"
			case "RestoreHold":
				root.RestoreHold = &coordination.Hold{OperationUID: "restore-uid", Reason: "Verifying"}
			case "UnknownPermit":
				root.ActivePermit = &coordination.Permit{ID: "uncertain-writer", Owner: "old-process", Class: "Ordinary", State: coordination.PermitUnknown}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	r := &HomeAssistantReconciler{
		Client: kube, Scheme: scheme, Coordination: CoordinationDependencies{APIReader: kube, Store: store},
		NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
	}
	_, err := r.reconcileBootstrap(ctx, ha)
	if err != nil && gate != "MissingEvidence" {
		t.Fatalf("bootstrap reconciliation returned an unexpected error: %v", err)
	}
	ledger := &corev1.Secret{}
	getErr := kube.Get(ctx, client.ObjectKey{Namespace: ha.Namespace, Name: ha.Spec.Bootstrap.OperatorAccount.TokenSecretName}, ledger)
	if gate == "Open" {
		if getErr != nil {
			t.Fatalf("Open fixture did not create the dedicated-account ledger: %v", getErr)
		}
		return
	}
	if !apierrors.IsNotFound(getErr) {
		t.Error("blocked synchronization created the dedicated-account ledger")
	}
}

func runSynchronizationGeneratedSourceCase(t *testing.T, kind, gate string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, hav1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{
		Name: "home", Namespace: "default", UID: types.UID("generated-source-instance"),
	}}
	decodeSynchronizationFixture(t, []byte(`{"spec":{"alpha":{"coordinatedSynchronization":{"enabled":true}}}}`), ha)
	if gate == "ManualPause" {
		ha.Spec.Synchronization = &hav1.SynchronizationSpec{Paused: true}
	}
	var child client.Object
	var expected client.Object
	objects := []client.Object{ha}
	switch kind {
	case "configuration":
		source := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "matrix-recorder-source", Namespace: "default"},
			Data:       map[string][]byte{"url": []byte("postgres://matrix")},
		}
		child = &hav1.HomeAssistantConfiguration{
			ObjectMeta: metav1.ObjectMeta{Name: "matrix-configuration", Namespace: "default"},
			Spec: hav1.HomeAssistantConfigurationSpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: ha.Name},
				Configuration:    "homeassistant:\n  name: Matrix\n",
				Recorder: &hav1.RecorderConfig{DatabaseSecretRef: &hav1.SecretKeySelector{
					Name: source.Name, Key: "url",
				}},
			},
		}
		objects = append(objects, source)
		expected = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
			Name: ha.Name + generatedConfigmapSuffix, Namespace: ha.Namespace,
		}}
	case "secrets":
		child = &hav1.HomeAssistantSecrets{
			ObjectMeta: metav1.ObjectMeta{Name: "matrix-secrets", Namespace: "default"},
			Spec:       hav1.HomeAssistantSecretsSpec{HomeAssistantRef: hav1.HomeAssistantReference{Name: ha.Name}},
		}
		expected = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: ha.Name + generatedSecretSuffix, Namespace: ha.Namespace,
		}}
	default:
		t.Fatal("unknown generated source kind")
	}
	objects = append(objects, child)
	kube := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(ha, child).WithObjects(objects...).Build()
	store := coordination.Store{Reader: kube, Client: kube}
	if gate != "MissingEvidence" {
		if _, err := store.CreateRoot(ctx, ha.Namespace, ha.UID); err != nil {
			t.Fatal(err)
		}
		_, err := store.UpdateRoot(ctx, ha.Namespace, ha.UID, func(root *coordination.Root) error {
			switch gate {
			case "Paused":
				root.Mode = "Paused"
			case "RestoreHold":
				root.RestoreHold = &coordination.Hold{OperationUID: "restore-uid", Reason: "Verifying"}
			case "UnknownPermit":
				root.ActivePermit = &coordination.Permit{ID: "uncertain-writer", Owner: "old-process", Class: "Ordinary", State: coordination.PermitUnknown}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	deps := CoordinationDependencies{APIReader: kube, Store: store}
	var result ctrl.Result
	var err error
	switch kind {
	case "configuration":
		result, err = (&HomeAssistantConfigurationReconciler{
			Client: kube, Scheme: scheme, Coordination: deps,
		}).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(child)})
	case "secrets":
		result, err = (&HomeAssistantSecretsReconciler{
			Client: kube, Scheme: scheme, Coordination: deps,
		}).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(child)})
	}
	actual := expected.DeepCopyObject().(client.Object)
	getErr := kube.Get(ctx, client.ObjectKeyFromObject(expected), actual)
	if gate == "Open" {
		if err != nil {
			t.Fatalf("Open fixture reconcile failed: %v", err)
		}
		if getErr != nil {
			t.Fatalf("Open fixture did not create its generated source: %v", getErr)
		}
		if kind == "configuration" {
			recorder := &corev1.Secret{}
			if err := kube.Get(ctx, client.ObjectKey{Namespace: ha.Namespace, Name: ha.Name + recorderDBSecretSuffix}, recorder); err != nil {
				t.Fatalf("Open fixture did not create its recorder Secret: %v", err)
			}
		}
		return
	}
	if err != nil && gate != "MissingEvidence" && !errors.Is(err, errSynchronizationWriteBlocked) {
		t.Fatalf("blocked synchronization returned an unexpected error: %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Error("blocked synchronization must schedule another observation")
	}
	if !apierrors.IsNotFound(getErr) {
		t.Error("blocked synchronization created or updated its generated source")
	}
	if kind == "configuration" {
		recorder := &corev1.Secret{}
		if err := kube.Get(ctx, client.ObjectKey{Namespace: ha.Namespace, Name: ha.Name + recorderDBSecretSuffix}, recorder); !apierrors.IsNotFound(err) {
			t.Error("blocked synchronization created or updated its recorder Secret")
		}
	}
}

func runSynchronizationAPICase(t *testing.T, kind, operation, gate string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, hav1.AddToScheme, hav1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{
		Name: "home", Namespace: "default", UID: types.UID("matrix-instance"),
	}, Status: hav1.HomeAssistantStatus{Phase: "Running", Bootstrap: &hav1.BootstrapStatus{
		APITokenSecretName: "matrix-token",
	}}}
	// All cases opt into the coordinated protocol. Missing durable state must
	// not silently fall back to legacy uncoordinated writes.
	decodeSynchronizationFixture(t, []byte(`{"spec":{"alpha":{"coordinatedSynchronization":{"enabled":true}}}}`), ha)
	if gate == "ManualPause" {
		ha.Spec.Synchronization = &hav1.SynchronizationSpec{Paused: true}
	}
	child := synchronizationAPIObject(t, kind, operation)
	key := client.ObjectKeyFromObject(child)
	kube := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(ha, child).WithObjects(ha, child, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "matrix-token", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("synthetic-matrix-token")},
	}).Build()
	store := coordination.Store{Reader: kube, Client: kube}
	if gate != "MissingEvidence" {
		if _, err := store.CreateRoot(ctx, ha.Namespace, ha.UID); err != nil {
			t.Fatal(err)
		}
		_, err := store.UpdateRoot(ctx, ha.Namespace, ha.UID, func(root *coordination.Root) error {
			switch gate {
			case "Paused":
				root.Mode = "Paused"
			case "RestoreHold":
				// Even an otherwise Open document must respect its independent hold.
				root.RestoreHold = &coordination.Hold{OperationUID: "restore-uid", Reason: "Verifying"}
			case "UnknownPermit":
				root.ActivePermit = &coordination.Permit{
					ID: "uncertain-writer", Owner: "old-process", Class: "Ordinary", State: coordination.PermitUnknown,
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	api := &synchronizationMatrixAPI{operation: operation}
	server := httptest.NewServer(api)
	defer server.Close()
	newHAClient := func(string) *haclient.Client { return haclient.NewClient(server.URL) }
	r := synchronizationAPIReconciler(kind, kube, scheme, newHAClient, CoordinationDependencies{
		APIReader: kube, Store: store,
	})
	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	writes := api.writtenCommands()
	if gate == "Open" {
		if err != nil {
			t.Fatalf("Open fixture reconcile failed: %v", err)
		}
		if len(writes) == 0 {
			t.Fatal("Open fixture did not exercise the intended write path")
		}
		assertSynchronizationOpenPath(t, kind, operation, writes)
		return
	}
	if len(writes) != 0 {
		t.Errorf("ordinary writes escaped %s gate: %v", gate, writes)
	}
	if err == nil && result.RequeueAfter <= 0 {
		t.Error("blocked synchronization must schedule another observation, not report completion")
	}
	remaining := child.DeepCopyObject().(client.Object)
	if getErr := kube.Get(ctx, key, remaining); getErr != nil {
		if apierrors.IsNotFound(getErr) {
			t.Error("blocked cleanup removed its finalizer and lost the pending object")
		} else {
			t.Fatal(getErr)
		}
		return
	}
	if len(remaining.GetFinalizers()) == 0 {
		t.Error("blocked cleanup must retain its finalizer")
	}
	if remaining.GetAnnotations()[lastAppliedIDAnnotationKey] != child.GetAnnotations()[lastAppliedIDAnnotationKey] {
		t.Error("blocked reconciliation advanced the applied identity")
	}
}

func synchronizationAPIObject(t *testing.T, kind, operation string) client.Object {
	t.Helper()
	var object client.Object
	spec := map[string]any{"homeAssistantRef": map[string]string{"name": "home"}}
	status := map[string]any{}
	finalizer := "ha.homeassistant.io/" + kind + "-finalizer"
	switch kind {
	case "automation":
		object = &hav1.HomeAssistantAutomation{}
		spec["id"], spec["alias"], spec["autoReload"] = "new_id", "New", false
		spec["triggers"] = []any{map[string]string{"trigger": "event", "event_type": "synthetic_matrix_event"}}
		spec["actions"] = []any{map[string]string{"delay": "00:00:01"}}
	case "scene":
		object = &hav1.HomeAssistantScene{}
		spec["id"], spec["name"], spec["autoReload"] = "new_id", "New", false
		spec["entities"] = []any{map[string]any{"entity_id": "light.synthetic", "state": "off"}}
	case "script":
		object = &hav1.HomeAssistantScript{}
		spec["id"], spec["alias"], spec["autoReload"] = "new_id", "New", false
		spec["sequence"] = []any{map[string]string{"delay": "00:00:01"}}
	case "integration":
		object = &hav1.HomeAssistantIntegration{}
		spec["domain"] = "synthetic_matrix"
		if operation != "normal" {
			status["entryID"], status["configHash"] = "old-entry", "old-config"
		}
	case "floor":
		object = &hav1.HomeAssistantFloor{}
		spec["name"] = "New"
		if operation != "normal" {
			status["floorID"] = "existing-id"
		}
	case "label":
		object = &hav1.HomeAssistantLabel{}
		spec["name"] = "New"
		if operation != "normal" {
			status["labelID"] = "existing-id"
		}
	case "area":
		object = &hav1.HomeAssistantArea{}
		spec["name"] = "New"
		if operation != "normal" {
			status["areaID"] = "existing-id"
		}
	case "dashboard":
		object = &hav1alpha1.HomeAssistantDashboard{}
		spec["title"], spec["urlPath"], spec["inline"] = "New", "matrix-dashboard", "views: []"
		if operation != "normal" {
			status["dashboardID"], status["urlPath"] = "existing-id", "matrix-dashboard"
		}
	}
	raw, err := json.Marshal(map[string]any{"spec": spec, "status": status})
	if err != nil {
		t.Fatal(err)
	}
	decodeSynchronizationFixture(t, raw, object)
	object.SetName("matrix-child")
	object.SetNamespace("default")
	object.SetUID("matrix-child-uid")
	object.SetGeneration(2)
	object.SetFinalizers([]string{finalizer})
	if operation == "rename" {
		object.SetAnnotations(map[string]string{lastAppliedIDAnnotationKey: "old_id"})
	}
	if operation == "delete" {
		now := metav1.Now()
		object.SetDeletionTimestamp(&now)
	}
	return object
}

func decodeSynchronizationFixture(t *testing.T, raw []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}

func synchronizationAPIReconciler(
	kind string, kube client.Client, scheme *runtime.Scheme, newClient func(string) *haclient.Client,
	dependencies CoordinationDependencies,
) reconcile.Reconciler {
	switch kind {
	case "automation":
		return &HomeAssistantAutomationReconciler{
			Client: kube, Scheme: scheme, NewHAClient: newClient, Coordination: dependencies,
		}
	case "scene":
		return &HomeAssistantSceneReconciler{
			Client: kube, Scheme: scheme, NewHAClient: newClient, Coordination: dependencies,
		}
	case "script":
		return &HomeAssistantScriptReconciler{
			Client: kube, Scheme: scheme, NewHAClient: newClient, Coordination: dependencies,
		}
	case "integration":
		return &HomeAssistantIntegrationReconciler{
			Client: kube, Scheme: scheme, NewHAClient: newClient, Coordination: dependencies,
		}
	case "floor":
		return &HomeAssistantFloorReconciler{
			Client: kube, Scheme: scheme, NewHAClient: newClient, Coordination: dependencies,
		}
	case "label":
		return &HomeAssistantLabelReconciler{
			Client: kube, Scheme: scheme, NewHAClient: newClient, Coordination: dependencies,
		}
	case "area":
		return &HomeAssistantAreaReconciler{
			Client: kube, Scheme: scheme, NewHAClient: newClient, Coordination: dependencies,
		}
	case "dashboard":
		return &HomeAssistantDashboardReconciler{
			Client: kube, Scheme: scheme, NewHAClient: newClient, Coordination: dependencies,
		}
	default:
		panic("unknown synchronization fixture kind")
	}
}

// Record command identifiers only: never authentication, request bodies or raw
// response data. The mutex also keeps race runs meaningful across HTTP handlers.
type synchronizationMatrixAPI struct {
	mu        sync.Mutex
	writes    []string
	operation string
}

func (a *synchronizationMatrixAPI) record(command string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.writes = append(a.writes, command)
}

func (a *synchronizationMatrixAPI) writtenCommands() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.writes...)
}

func (a *synchronizationMatrixAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if websocket.IsWebSocketUpgrade(r) {
		a.serveWebSocket(w, r)
		return
	}
	if r.Method != http.MethodGet {
		a.record(r.Method + " " + r.URL.Path)
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet:
		_, _ = w.Write([]byte(`[]`))
	case strings.HasSuffix(r.URL.Path, "/flow"):
		_, _ = w.Write([]byte(`{"type":"create_entry","result":{"entry_id":"new-entry"}}`))
	default:
		_, _ = w.Write([]byte(`{"result":"ok","require_restart":false}`))
	}
}

func (a *synchronizationMatrixAPI) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if conn.WriteJSON(map[string]any{"type": "auth_required"}) != nil {
		return
	}
	var auth map[string]any
	if conn.ReadJSON(&auth) != nil || conn.WriteJSON(map[string]any{"type": "auth_ok"}) != nil {
		return
	}
	var command map[string]any
	if conn.ReadJSON(&command) != nil {
		return
	}
	name, _ := command["type"].(string)
	var result any
	switch {
	case name == "backup/config/info":
		result = map[string]any{"config": map[string]any{
			"schedule":      map[string]any{"recurrence": "never"},
			"retention":     map[string]any{},
			"create_backup": map[string]any{"include_database": false, "password": "synthetic-matrix-key"},
		}}
	case name == "backup/agents/info":
		result = map[string]any{"agents": []any{map[string]any{"agent_id": "backup.local", "name": "Local"}}}
	case strings.HasSuffix(name, "/list"):
		result = []any{}
		if a.operation == "rename" {
			result = []any{map[string]any{
				"floor_id": "existing-id", "label_id": "existing-id", "area_id": "existing-id",
				"id": "existing-id", "name": "Old", "title": "Old", "url_path": "matrix-dashboard", "mode": "storage",
			}}
		}
	case name == "lovelace/config":
		result = map[string]any{"views": []any{map[string]any{"title": "Old"}}}
	default:
		a.record(name)
		result = map[string]any{
			"floor_id": "existing-id", "label_id": "existing-id", "area_id": "existing-id",
			"id": "existing-id", "name": "New", "title": "New", "url_path": "matrix-dashboard", "mode": "storage",
		}
	}
	_ = conn.WriteJSON(map[string]any{"id": command["id"], "type": "result", "success": true, "result": result})
}

func assertSynchronizationOpenPath(t *testing.T, kind, operation string, writes []string) {
	t.Helper()
	joined := strings.Join(writes, "\n")
	if operation == "delete" && !strings.Contains(joined, "delete") && !strings.Contains(joined, "DELETE") {
		t.Fatal("Open deletion fixture did not exercise cleanup")
	}
	if operation == "rename" {
		switch kind {
		case "automation", "scene", "script":
			if !strings.Contains(joined, "DELETE /api/config/"+kind+"/config/old_id") ||
				!strings.Contains(joined, "POST /api/config/"+kind+"/config/new_id") {
				t.Fatal("Open rename fixture did not exercise old-ID cleanup and replacement")
			}
		case "integration":
			if !strings.Contains(joined, "DELETE") || !strings.Contains(joined, "POST") {
				t.Fatal("Open integration fixture did not exercise removal and replacement flow")
			}
		default:
			if !strings.Contains(joined, "/update") {
				t.Fatal("Open registry rename fixture did not exercise an update")
			}
		}
	}
}
