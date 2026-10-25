package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestDedicatedCredentialHealth(t *testing.T) {
	for _, tc := range []struct {
		name              string
		code              int
		token             string
		missing, conflict bool
		reason            string
	}{
		{"healthy", 200, "sentinel-token", false, false, "Verified"},
		{"missing", 200, "", true, false, reasonCredentialMissing},
		{"empty", 200, "", false, false, reasonCredentialMalformed},
		{"rejected", 401, "sentinel-token", false, false, reasonCredentialRejected},
		{"unavailable", 503, "sentinel-token", false, false, reasonHomeAssistantUnavailable},
		{"conflict", 200, "unrelated", false, true, reasonCredentialMalformed},
		{"invalid metadata", 200, "sentinel-token", false, false, reasonCredentialMalformed},
		{"unexpected data", 200, "sentinel-token", false, false, reasonCredentialMalformed},
		{"missing source identity", 200, "sentinel-token", false, false, reasonCredentialMalformed},
		{"human owner", 200, "sentinel-token", false, false, reasonCredentialRejected},
		{"insufficient role", 200, "sentinel-token", false, false, reasonRequiresElevatedRole},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			_ = hav1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "home", UID: types.UID("ha-uid")},
				Spec: hav1.HomeAssistantSpec{
					Bootstrap: &hav1.BootstrapSpec{
						Enabled: true,
						OperatorAccount: &hav1.OperatorAccountSpec{
							Mode:            "dedicated",
							TokenSecretName: "operator-token",
						},
					},
				},
				Status: hav1.HomeAssistantStatus{
					Bootstrap: &hav1.BootstrapStatus{
						Completed:       true,
						OperatorAccount: &hav1.OperatorAccountStatus{UserID: "operator", Phase: "Verified"},
					},
				},
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ha).WithObjects(ha).Build()
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "operator-token",
					Namespace: ha.Namespace,
					Labels: map[string]string{
						"app.kubernetes.io/managed-by": "homeassistant-operator",
						operatorPurposeLabel:           "operator-api-token",
					},
					Annotations: map[string]string{
						operatorUserAnnotation:  "operator",
						operatorStageAnnotation: "Verified",
					},
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{"token": []byte(tc.token)},
			}
			if !tc.conflict {
				_ = controllerutil.SetControllerReference(ha, secret, scheme)
			}
			switch tc.name {
			case "invalid metadata":
				delete(secret.Annotations, operatorStageAnnotation)
			case "unexpected data":
				secret.Data["password"] = []byte("unrelated-sensitive-data")
			case "missing source identity":
				delete(secret.Annotations, operatorUserAnnotation)
			}
			if !tc.missing {
				if err := c.Create(ctx, secret); err != nil {
					t.Fatal(err)
				}
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/api/websocket" {
					t.Error("unexpected account operation")
				}
				if tc.code == 503 {
					w.WriteHeader(tc.code)
					return
				}
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				_ = conn.WriteJSON(map[string]string{"type": "auth_required"})
				var auth map[string]interface{}
				_ = conn.ReadJSON(&auth)
				if auth["access_token"] != "sentinel-token" {
					t.Error("wrong runtime identity")
				}
				if tc.code == 401 {
					_ = conn.WriteJSON(map[string]string{"type": "auth_invalid"})
					return
				}
				_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
				var command map[string]interface{}
				_ = conn.ReadJSON(&command)
				if command["type"] != "auth/current_user" {
					t.Error("health probe mutated an account")
				}
				_ = conn.WriteJSON(
					map[string]interface{}{
						"id":      1,
						"type":    "result",
						"success": true,
						"result": haclient.AuthUser{
							ID:      "operator",
							IsOwner: tc.name == "human owner",
							IsAdmin: tc.name != "insufficient role",
						},
					},
				)
			}))
			defer server.Close()
			r := &HomeAssistantReconciler{
				Client:      c,
				Scheme:      scheme,
				NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
			}
			_, err := r.reconcileBootstrap(ctx, ha)
			if err != nil {
				t.Fatal(err)
			}
			updated := &hav1.HomeAssistant{}
			if err := c.Get(ctx, types.NamespacedName{Name: ha.Name, Namespace: ha.Namespace}, updated); err != nil {
				t.Fatal(err)
			}
			cond := meta.FindStatusCondition(updated.Status.Conditions, conditionOperatorCredentialReady)
			if cond == nil || cond.Reason != tc.reason {
				t.Fatalf("condition = %+v, want %s", cond, tc.reason)
			}
			if requests > 1 {
				t.Fatal("routine reconciliation made multiple API calls")
			}
		})
	}
}

// exercise the full external identity boundary, including restart reconstruction,
// without requiring a live Home Assistant or Kubernetes API server.
//
//nolint:gocyclo // This sequential lifecycle scenario keeps its external API fixture and restart assertions together.
func TestDedicatedProvisionAndRecovery(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = hav1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "home", UID: "ha-uid"},
		Spec: hav1.HomeAssistantSpec{
			Bootstrap: &hav1.BootstrapSpec{
				Enabled:         true,
				Credentials:     &hav1.BootstrapCredentials{SecretRef: &hav1.CredentialsSecretRef{Name: "bootstrap"}},
				OperatorAccount: &hav1.OperatorAccountSpec{Mode: "dedicated", TokenSecretName: "operator"},
			},
		},
	}
	bootstrap := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "bootstrap", Namespace: "home"},
		Data:       map[string][]byte{"username": []byte("owner"), "password": []byte("owner-password")},
	}
	legacy := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "home"},
		Data:       map[string][]byte{"token": []byte("legacy-token")},
	}
	ha.Status.Bootstrap = &hav1.BootstrapStatus{Completed: true, APITokenSecretName: "legacy"}
	ha.Status.BanRestartWindowCount = 1
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ha).WithObjects(ha, bootstrap, legacy).Build()
	var mu sync.Mutex
	var users []haclient.AuthUser
	created, issued, resets, ownerLogins := 0, 0, 0, 0
	loginUser := ""
	passwords := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		write := func(v interface{}) { _ = json.NewEncoder(w).Encode(v) }
		switch req.URL.Path {
		case "/api/":
			if req.Header.Get("Authorization") != "" &&
				!strings.HasPrefix(req.Header.Get("Authorization"), "Bearer operator-token-") {
				t.Error("runtime probe used owner")
			}
			write(map[string]string{"message": "ok"})
		case "/api/config":
			w.WriteHeader(http.StatusUnauthorized)
		case "/api/onboarding":
			w.WriteHeader(http.StatusNotFound)
		case "/auth/login_flow":
			write(map[string]string{"flow_id": "flow", "type": "form"})
		case "/auth/login_flow/flow":
			var credentials map[string]string
			_ = json.NewDecoder(req.Body).Decode(&credentials)
			loginUser = credentials["username"]
			if loginUser == "owner" {
				ownerLogins++
			} else if credentials["password"] == "owner-password" {
				t.Error("operator reused owner password")
			}
			write(map[string]string{"type": "create_entry", "result": "code"})
		case "/auth/token":
			token := "operator-session"
			if loginUser == "owner" {
				token = "owner-session"
			}
			write(map[string]string{"access_token": token})
		case "/api/websocket":
			conn, err := (&websocket.Upgrader{}).Upgrade(w, req, nil)
			if err != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.WriteJSON(map[string]string{"type": "auth_required"})
			var auth map[string]interface{}
			_ = conn.ReadJSON(&auth)
			_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
			var msg map[string]interface{}
			_ = conn.ReadJSON(&msg)
			var result interface{}
			switch msg["type"] {
			case "config/auth/list":
				result = users
			case "config/auth/create":
				created++
				groups, _ := msg["group_ids"].([]interface{})
				if len(groups) != 1 || groups[0] != operatorAdminGroup {
					t.Error("missing explicit administrator role")
				}
				users = append(
					users,
					haclient.AuthUser{
						ID:       "operator-id",
						Name:     msg["name"].(string),
						GroupIDs: []string{operatorAdminGroup},
						IsActive: true,
					},
				)
				result = map[string]interface{}{"user": users[0]}
			case "config/auth_provider/homeassistant/create":
				users[0].Username = msg["username"].(string)
				passwords = append(passwords, msg["password"].(string))
			case "config/auth_provider/homeassistant/admin_change_password":
				if msg["user_id"] != "operator-id" {
					t.Error("reset targeted a different user")
				}
				resets++
				passwords = append(passwords, msg["password"].(string))
			case "auth/long_lived_access_token":
				if auth["access_token"] != "operator-session" {
					t.Error("issued owner-derived token")
				}
				issued++
				result = fmt.Sprintf("operator-token-%d", issued)
			case "auth/current_user":
				principal := users[0]
				principal.IsAdmin = true
				result = principal
			default:
				t.Errorf("unexpected command: %v", msg["type"])
			}
			_ = conn.WriteJSON(map[string]interface{}{"id": 1, "type": "result", "success": true, "result": result})
		default:
			t.Errorf("unexpected path %s", req.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	r := &HomeAssistantReconciler{
		Client:      c,
		Scheme:      scheme,
		NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
	}
	reconcile := func() {
		t.Helper()
		if err := c.Get(ctx, client.ObjectKeyFromObject(ha), ha); err != nil {
			t.Fatal(err)
		}
		if _, err := r.reconcileBootstrap(ctx, ha); err != nil {
			t.Fatal(err)
		}
	}
	if token, err := getAPIToken(ctx, c, ha); err != nil || token != "legacy-token" {
		t.Fatal("migration prematurely removed legacy access")
	}
	reconcile()
	secret := &corev1.Secret{}
	if ha.Status.BanRestartWindowCount != 0 {
		t.Fatal("successful full provisioning did not reset ban recovery")
	}
	if err := c.Get(ctx, types.NamespacedName{Name: "operator", Namespace: "home"}, secret); err != nil {
		t.Fatal(err)
	}
	if metav1.GetControllerOf(secret) == nil || metav1.GetControllerOf(secret).UID != ha.UID {
		t.Fatal("missing same-namespace ownership")
	}
	if len(secret.Data) != 1 || len(secret.Data["token"]) == 0 {
		t.Fatal("unexpected credential payload")
	}
	if ha.Status.Bootstrap.OperatorAccount.UserID != "operator-id" ||
		ha.Status.Bootstrap.OperatorAccount.LegacySecretName != "legacy" {
		t.Fatal("missing identity or migration status")
	}
	// Verified identity, migration, and retirement reconstruct entirely from the
	// managed Secret after the status subresource is lost.
	ha.Status = hav1.HomeAssistantStatus{}
	if err := c.Status().Update(ctx, ha); err != nil {
		t.Fatal(err)
	}
	// Simulate a restart after token persistence but before verification: the
	// ledger, not status or an in-memory password, must complete verification.
	secret.Annotations[operatorStageAnnotation] = "Issuing"
	if err := c.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	reconcile()
	if ha.Status.Bootstrap.OperatorAccount.UserID != "operator-id" || !ha.Status.Bootstrap.Completed {
		t.Fatal("status did not reconstruct from ledger")
	}
	if err := c.Delete(ctx, bootstrap); err != nil {
		t.Fatal(err)
	}
	reconcile()
	reconcile()
	if ownerLogins != 1 || created != 1 || issued != 1 {
		t.Fatal("routine reconcile reused bootstrap or issued credentials")
	}
	serialized, _ := json.Marshal(ha.Status)
	for _, password := range passwords {
		if strings.Contains(string(serialized), password) {
			t.Fatal("password leaked to status")
		}
	}
	if strings.Contains(string(serialized), "operator-token-") {
		t.Fatal("token leaked to status")
	}
	// Deleted bootstrap is neither read nor recreated during normal operation.
	if err := c.Get(ctx, types.NamespacedName{Name: "bootstrap", Namespace: "home"}, &corev1.Secret{}); err == nil {
		t.Fatal("bootstrap recreated")
	}
	if err := c.Delete(ctx, secret); err != nil {
		t.Fatal(err)
	}
	reconcile()
	if issued != 1 || resets != 0 {
		t.Fatal("token loss mutated an account")
	}
	ha.Spec.Bootstrap.OperatorAccount.RecoveryRequestID = "request-1"
	if err := c.Update(ctx, ha); err != nil {
		t.Fatal(err)
	}
	reconcile()
	if meta.FindStatusCondition(ha.Status.Conditions, conditionOperatorCredentialReady).Reason != reasonRecoveryFailed {
		t.Fatal("missing bootstrap recovery not actionable")
	}
	// An observed failed request is durable on the ledger and cannot replay even
	// after status loss, restart, or reappearance of the bootstrap credential.
	ha.Status = hav1.HomeAssistantStatus{}
	if err := c.Status().Update(ctx, ha); err != nil {
		t.Fatal(err)
	}
	bootstrap.ResourceVersion = ""
	if err := c.Create(ctx, bootstrap); err != nil {
		t.Fatal(err)
	}
	reconcile()
	if issued != 1 {
		t.Fatal("failed request replayed after status loss")
	}
	ha.Spec.Bootstrap.OperatorAccount.RecoveryRequestID = "request-2"
	if err := c.Update(ctx, ha); err != nil {
		t.Fatal(err)
	}
	reconcile()
	reconcile()
	if created != 1 || issued != 2 || resets != 1 {
		t.Fatalf("created=%d issued=%d resets=%d", created, issued, resets)
	}
	if ha.Status.Bootstrap.OperatorAccount.RecoveryResult != "Succeeded" {
		t.Fatal("recovery not recorded")
	}
	if err := c.Get(ctx, types.NamespacedName{Name: "legacy", Namespace: "home"}, legacy); err != nil {
		t.Fatal("legacy Secret deleted")
	}
}

type deniedOperatorSecretClient struct{ client.Client }

func (c deniedOperatorSecretClient) Get(
	ctx context.Context,
	key client.ObjectKey,
	obj client.Object,
	opts ...client.GetOption,
) error {
	if _, ok := obj.(*corev1.Secret); ok {
		return apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, key.Name, fmt.Errorf("denied"))
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func TestDedicatedSecretAccessDenied(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = hav1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "home", UID: "uid"},
		Spec: hav1.HomeAssistantSpec{
			Bootstrap: &hav1.BootstrapSpec{
				Enabled:         true,
				OperatorAccount: &hav1.OperatorAccountSpec{Mode: "dedicated", TokenSecretName: "operator"},
			},
		},
	}
	c := deniedOperatorSecretClient{
		fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ha).WithObjects(ha).Build(),
	}
	r := &HomeAssistantReconciler{Client: c, Scheme: scheme}
	result, err := r.reconcileBootstrap(context.Background(), ha)
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("result=%v error=%v", result, err)
	}
	condition := meta.FindStatusCondition(ha.Status.Conditions, conditionOperatorCredentialReady)
	if condition == nil || condition.Reason != reasonSecretAccessDenied {
		t.Fatal("access denied misclassified")
	}
}

func TestOperatorIdentityCreationCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		stage, userID    string
		found, wantError bool
	}{
		{"Creating", "", true, false},
		{"Creating", "operator-id", true, false},
		{"Creating", "", false, true},
		{"Provisioning", "", true, true},
	} {
		t.Run(tc.stage+tc.userID+fmt.Sprint(tc.found), func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = hav1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			ha := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "home", UID: "uid"}}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "operator",
					Namespace: "home",
					Annotations: map[string]string{
						operatorStageAnnotation: tc.stage,
						operatorUserAnnotation:  tc.userID,
					},
				},
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, req, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				_ = conn.WriteJSON(map[string]string{"type": "auth_required"})
				var message map[string]interface{}
				_ = conn.ReadJSON(&message)
				_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
				_ = conn.ReadJSON(&message)
				if message["type"] != "config/auth/list" {
					t.Error("restart duplicated or changed an account")
				}
				users := []haclient.AuthUser{}
				if tc.found {
					users = append(
						users,
						haclient.AuthUser{
							ID:       "operator-id",
							Name:     "Kubernetes operator uid",
							GroupIDs: []string{operatorAdminGroup},
						},
					)
				}
				_ = conn.WriteJSON(map[string]interface{}{"id": 1, "type": "result", "success": true, "result": users})
			}))
			defer server.Close()
			r := &HomeAssistantReconciler{Client: c, Scheme: scheme}
			_, err := r.ensureOperatorUser(
				context.Background(),
				ha,
				secret,
				haclient.NewClient(server.URL),
				"owner-session",
			)
			if (err != nil) != tc.wantError {
				t.Fatalf("wantError=%v error=%v", tc.wantError, err)
			}
		})
	}
}

func TestOperatorInterruptedIssuanceRequiresNewRequest(t *testing.T) {
	for _, stage := range []string{"Issuing", "Recovering", "Verified"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			_ = hav1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "home", UID: "uid"},
				Spec: hav1.HomeAssistantSpec{
					Bootstrap: &hav1.BootstrapSpec{
						Enabled: true,
						OperatorAccount: &hav1.OperatorAccountSpec{
							Mode:              "dedicated",
							TokenSecretName:   "operator",
							RecoveryRequestID: "observed-request",
						},
					},
				},
			}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "operator",
					Namespace: "home",
					Labels: map[string]string{
						"app.kubernetes.io/managed-by": "homeassistant-operator",
						operatorPurposeLabel:           "operator-api-token",
					},
					Annotations: map[string]string{
						operatorUserAnnotation:           "operator-id",
						operatorStageAnnotation:          stage,
						operatorRecoveryAnnotation:       "observed-request",
						operatorRecoveryResultAnnotation: "Failed",
					},
				},
				Type: corev1.SecretTypeOpaque,
			}
			_ = controllerutil.SetControllerReference(ha, secret, scheme)
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ha).WithObjects(ha, secret).Build()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				t.Error("interrupted request contacted Home Assistant")
				w.WriteHeader(500)
			}))
			defer server.Close()
			r := &HomeAssistantReconciler{
				Client:      c,
				Scheme:      scheme,
				NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
			}
			result, err := r.reconcileBootstrap(ctx, ha)
			if err != nil || result.RequeueAfter == 0 {
				t.Fatalf("result=%v error=%v", result, err)
			}
			condition := meta.FindStatusCondition(ha.Status.Conditions, conditionOperatorCredentialReady)
			if condition == nil || condition.Reason != reasonRecoveryFailed {
				t.Fatalf("interrupted request not observable: %+v", condition)
			}
			if ha.Status.Bootstrap.OperatorAccount.LastRecoveryRequestID != "observed-request" {
				t.Fatal("request ledger lost during status reconstruction")
			}
		})
	}
}

func TestLegacyCredentialSelectionAndRollback(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = hav1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	legacy := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "home"},
		Data:       map[string][]byte{"token": []byte("legacy-value")},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(legacy).Build()
	ha := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "home"},
		Status:     hav1.HomeAssistantStatus{Bootstrap: &hav1.BootstrapStatus{APITokenSecretName: "legacy"}},
	}
	if token, err := getAPIToken(context.Background(), c, ha); err != nil || token != "legacy-value" {
		t.Fatal("omitted operatorAccount changed legacy behavior")
	}
	ha.Status.Bootstrap.APITokenSecretName = "dedicated"
	ha.Spec.Bootstrap = &hav1.BootstrapSpec{
		APITokenSecretName: "legacy",
		OperatorAccount:    &hav1.OperatorAccountSpec{Mode: "legacy"},
	}
	if token, err := getAPIToken(context.Background(), c, ha); err != nil || token != "legacy-value" {
		t.Fatal("explicit rollback ignored retained legacy credential")
	}
}
