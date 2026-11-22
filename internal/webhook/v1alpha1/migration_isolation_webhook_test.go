package v1alpha1

import (
	"context"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestMigrationIsolationValidatorProtectsCredentialMounts(t *testing.T) {
	v := &MigrationIsolationValidator{OperatorUsername: "system:serviceaccount:operator:manager"}
	request := func(user, group, kind, body string) admission.Request {
		return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{UID: types.UID("test"), Operation: admissionv1.Create, Kind: metav1.GroupVersionKind{Group: group, Version: "v1", Kind: kind}, UserInfo: authentication(user), Object: runtimeRaw(body)}}
	}
	for _, tc := range []struct {
		name, user, group, kind, body string
		allowed                       bool
	}{
		{"unrelated pod", "alice", "", "Pod", `{"spec":{"containers":[{}]}}`, true},
		{"foreign secret create", "alice", "", "Secret", `{"metadata":{"name":"migration-proxy-x"}}`, false},
		{"operator secret create", v.OperatorUsername, "", "Secret", `{"metadata":{"name":"migration-proxy-x"}}`, true},
		{"foreign configmap create", "alice", "", "ConfigMap", `{"metadata":{"name":"migration-proxy-x-claim"}}`, false},
		{"foreign pod volume", "alice", "", "Pod", `{"spec":{"volumes":[{"secret":{"secretName":"migration-proxy-x"}}],"containers":[{}]}}`, false},
		{"foreign pod projected", "alice", "", "Pod", `{"spec":{"volumes":[{"projected":{"sources":[{"secret":{"name":"migration-proxy-x"}}]}}],"containers":[{}]}}`, false},
		{"foreign pod environment", "alice", "", "Pod", `{"spec":{"containers":[{"env":[{"valueFrom":{"secretKeyRef":{"name":"migration-proxy-x"}}}]}]}}`, false},
		{"foreign job", "alice", "batch", "Job", `{"spec":{"template":{"spec":{"volumes":[{"secret":{"secretName":"migration-proxy-x"}}],"containers":[{}]}}}}`, false},
		{"operator job", v.OperatorUsername, "batch", "Job", `{"spec":{"template":{"spec":{"volumes":[{"secret":{"secretName":"migration-proxy-x"}}],"containers":[{}]}}}}`, true},
		{"job controller pod", "system:serviceaccount:kube-system:job-controller", "", "Pod", `{"spec":{"volumes":[{"secret":{"secretName":"migration-proxy-x"}}],"containers":[{}]}}`, true},
		{"shared controller manager pod", "system:kube-controller-manager", "", "Pod", `{"spec":{"volumes":[{"secret":{"secretName":"migration-proxy-x"}}],"containers":[{}]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := v.Handle(context.Background(), request(tc.user, tc.group, tc.kind, tc.body))
			if response.Result != nil && response.Result.Code == 400 {
				t.Fatalf("fixture failed to decode: %#v", response.Result)
			}
			if response.Allowed != tc.allowed {
				t.Fatalf("allowed=%v, want %v", response.Allowed, tc.allowed)
			}
		})
	}
}

func TestMigrationIsolationEphemeralContainerUpdates(t *testing.T) {
	v := &MigrationIsolationValidator{OperatorUsername: "system:serviceaccount:operator:manager"}
	for _, tc := range []struct {
		name, body string
		allowed    bool
	}{
		{"protected reference", `{"spec":{"ephemeralContainers":[{"env":[{"name":"KEY","valueFrom":{"secretKeyRef":{"name":"migration-proxy-x","key":"key"}}}]}]}}`, false},
		{"unrelated reference", `{"spec":{"ephemeralContainers":[{"env":[{"name":"KEY","valueFrom":{"secretKeyRef":{"name":"unrelated","key":"key"}}}]}]}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
				Operation: admissionv1.Update, Kind: metav1.GroupVersionKind{Version: "v1", Kind: "Pod"},
				SubResource: "ephemeralcontainers", UserInfo: authentication("alice"), Object: runtimeRaw(tc.body),
			}}
			response := v.Handle(context.Background(), req)
			if response.Allowed != tc.allowed || response.Result == nil || response.Result.Code == 400 {
				t.Fatalf("response=%+v, want allowed=%t and a policy decision", response, tc.allowed)
			}
		})
	}
}

func authentication(username string) authenticationv1.UserInfo {
	return authenticationv1.UserInfo{Username: username}
}
func runtimeRaw(body string) runtime.RawExtension { return runtime.RawExtension{Raw: []byte(body)} }
