package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

func TestReconcileBootstrapHealth(t *testing.T) {
	for _, tt := range []struct {
		name       string
		secretName string
		secretData map[string][]byte
		completed  bool
		statusCode int
		wantAuth   string
	}{
		{
			name: "token from status", secretName: "custom-api-token",
			secretData: map[string][]byte{"token": []byte("custom-token")},
			completed:  true, statusCode: http.StatusOK, wantAuth: "Bearer custom-token",
		},
		{
			name:       "default token secret",
			secretData: map[string][]byte{"token": []byte("default-token")},
			completed:  true, statusCode: http.StatusOK, wantAuth: "Bearer default-token",
		},
		{name: "missing secret", completed: true, statusCode: http.StatusUnauthorized},
		{
			name: "missing token key", secretData: map[string][]byte{"other": []byte("unused")},
			completed: true, statusCode: http.StatusUnauthorized,
		},
		{
			name: "empty token", secretData: map[string][]byte{"token": {}},
			completed: true, statusCode: http.StatusUnauthorized,
		},
		{
			name:       "before bootstrap remains unauthenticated even with a token",
			secretData: map[string][]byte{"token": []byte("existing-token")},
			statusCode: http.StatusServiceUnavailable,
		},
		{
			name:       "authenticated probe detects ban",
			secretData: map[string][]byte{"token": []byte("valid-token")},
			completed:  true, statusCode: http.StatusForbidden, wantAuth: "Bearer valid-token",
		},
		{
			name:      "unauthenticated fallback detects ban",
			completed: true, statusCode: http.StatusForbidden,
		},
		{
			name:       "rate limiting does not trigger ban recovery",
			secretData: map[string][]byte{"token": []byte("valid-token")},
			completed:  true, statusCode: http.StatusTooManyRequests, wantAuth: "Bearer valid-token",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default"},
				Spec: hav1.HomeAssistantSpec{Bootstrap: &hav1.BootstrapSpec{
					Enabled: true,
					Credentials: &hav1.BootstrapCredentials{SecretRef: &hav1.CredentialsSecretRef{
						Name: "credentials",
					}},
				}},
				Status: hav1.HomeAssistantStatus{Bootstrap: &hav1.BootstrapStatus{
					Completed: tt.completed, APITokenSecretName: tt.secretName,
				}},
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "home-0", Namespace: ha.Namespace}}
			objects := []client.Object{ha, pod, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: ha.Namespace},
				Data:       map[string][]byte{"username": []byte("admin"), "password": []byte("password")},
			}}
			if tt.secretData != nil {
				name := tt.secretName
				if name == "" {
					name = "home-homeassistant-api-token"
				}
				objects = append(objects, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ha.Namespace}, Data: tt.secretData,
				})
			}
			scheme := runtime.NewScheme()
			if err := hav1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).
				WithStatusSubresource(&hav1.HomeAssistant{}).Build()
			requests := make(chan *http.Request, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests <- req
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()
			r := &HomeAssistantReconciler{
				Client: cl, Scheme: scheme, Recorder: events.NewFakeRecorder(16),
				NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
			}
			result, err := r.reconcileBootstrap(ctx, ha)
			if err != nil {
				t.Fatal(err)
			}
			if len(requests) != 1 {
				t.Fatalf("got %d HTTP requests, want one health check", len(requests))
			}
			req := <-requests
			if req.Method != http.MethodGet || req.URL.Path != "/api/" {
				t.Fatalf("unexpected health check: %s %s", req.Method, req.URL.Path)
			}
			if got := req.Header.Get("Authorization"); got != tt.wantAuth {
				t.Errorf("Authorization = %q, want %q", got, tt.wantAuth)
			}
			updated := &hav1.HomeAssistant{}
			if err := cl.Get(ctx, client.ObjectKeyFromObject(ha), updated); err != nil {
				t.Fatal(err)
			}
			podErr := cl.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, &corev1.Pod{})
			if tt.statusCode == http.StatusForbidden {
				if !apierrors.IsNotFound(podErr) {
					t.Fatalf("ban recovery should delete HA pod, got %v", podErr)
				}
				condition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeBanRecovery)
				if condition == nil || condition.Reason != reasonBanRecoveryInProgress || updated.Status.SelfUnbanCount != 1 {
					t.Fatalf("ban recovery status not set: %+v", updated.Status)
				}
				if result.RequeueAfter != selfUnbanRequeueWait {
					t.Errorf("requeue = %s, want %s", result.RequeueAfter, selfUnbanRequeueWait)
				}
			} else {
				if podErr != nil {
					t.Fatalf("health check should preserve HA pod: %v", podErr)
				}
				if updated.Status.SelfUnbanCount != 0 {
					t.Fatal("health check unexpectedly triggered ban recovery")
				}
				if tt.completed && result.RequeueAfter != 0 {
					t.Fatalf("post-bootstrap health check unexpectedly requeued: %+v", result)
				}
				if !tt.completed {
					condition := meta.FindStatusCondition(updated.Status.Conditions, "BootstrapReady")
					if condition == nil || condition.Reason != reasonBootstrapNotReady || result.RequeueAfter != bootstrapHealthCheckRetry {
						t.Fatalf("pre-bootstrap health failure should requeue as not ready: %+v, %+v", result, condition)
					}
				}
			}
		})
	}
}
