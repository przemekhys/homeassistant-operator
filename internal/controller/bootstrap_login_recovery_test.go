package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBootstrapLoginRecovery(t *testing.T) {
	for _, path := range []string{"core config", "token creation"} {
		for _, failure := range []string{"invalid credentials", "no user"} {
			t.Run(path+"/"+failure, func(t *testing.T) {
				ctx := context.Background()
				firstSeen := metav1.NewTime(time.Now().Add(-time.Minute))
				ha := &hav1.HomeAssistant{
					ObjectMeta: metav1.ObjectMeta{Name: "home", Namespace: "default"},
					Spec: hav1.HomeAssistantSpec{Bootstrap: &hav1.BootstrapSpec{
						Enabled: true, CreateAPIToken: true,
						Credentials: &hav1.BootstrapCredentials{SecretRef: &hav1.CredentialsSecretRef{Name: "owner"}},
					}},
					Status: hav1.HomeAssistantStatus{Bootstrap: &hav1.BootstrapStatus{OnboardingDoneFirstSeen: &firstSeen}},
				}
				if path == "core config" {
					ha.Spec.Bootstrap.Location = &hav1.LocationConfig{Country: "PL"}
				}
				if failure == "no user" {
					// A startup race must not exhaust the last remaining attempt.
					ha.Status.Bootstrap.LoginRecoveryAttempts = maxLoginRecoveryRetries - 1
				}
				scheme := runtime.NewScheme()
				if err := hav1.AddToScheme(scheme); err != nil {
					t.Fatal(err)
				}
				if err := corev1.AddToScheme(scheme); err != nil {
					t.Fatal(err)
				}
				cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(ha).WithObjects(ha, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "owner", Namespace: ha.Namespace},
					Data:       map[string][]byte{"username": []byte("owner"), "password": []byte("password")},
				}).Build()
				var logins atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					switch req.URL.Path {
					case "/api/onboarding":
						w.WriteHeader(http.StatusNotFound)
					case "/auth/login_flow":
						logins.Add(1)
						_ = json.NewEncoder(w).Encode(map[string]string{"flow_id": "flow", "type": "form"})
					case "/auth/login_flow/flow":
						if failure == "invalid credentials" {
							w.WriteHeader(http.StatusUnauthorized)
							return
						}
						_ = json.NewEncoder(w).Encode(map[string]string{"type": "form"})
					default:
						t.Errorf("unexpected request: %s", req.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()
				r := &HomeAssistantReconciler{
					Client: cl, Scheme: scheme,
					NewHAClient: func(string) *haclient.Client { return haclient.NewClient(server.URL) },
				}
				for attempt := 1; attempt <= maxLoginRecoveryRetries+1; attempt++ {
					result, err := r.handleOnboardingAlreadyDone(ctx, ha)
					if err != nil {
						t.Fatal(err)
					}
					if err := cl.Get(ctx, client.ObjectKeyFromObject(ha), ha); err != nil {
						t.Fatal(err)
					}
					bs := ha.Status.Bootstrap
					condition := meta.FindStatusCondition(ha.Status.Conditions, "BootstrapReady")
					wantAttempts := min(attempt, maxLoginRecoveryRetries)
					wantReason, wantRetry := reasonBootstrapAlreadyDone, bootstrapRetryInterval
					wantLogins := wantAttempts
					if attempt >= maxLoginRecoveryRetries {
						wantReason, wantRetry = reasonBootstrapLoginFailed, 5*time.Minute
					}
					if failure == "no user" {
						wantAttempts = maxLoginRecoveryRetries - 1
						wantReason, wantRetry = reasonBootstrapNotReady, bootstrapHealthCheckRetry
						wantLogins = attempt
						if bs.OnboardingDoneFirstSeen != nil {
							t.Fatal("missing user did not reset onboarding confirmation window")
						}
					}
					if bs.Completed || bs.LoginRecoveryAttempts != wantAttempts || condition == nil ||
						condition.Status != metav1.ConditionFalse || condition.Reason != wantReason ||
						result.RequeueAfter != wantRetry || logins.Load() != int32(wantLogins) {
						t.Fatalf("attempt %d: status=%+v condition=%+v result=%+v logins=%d",
							attempt, bs, condition, result, logins.Load())
					}
				}
			})
		}
	}
}
