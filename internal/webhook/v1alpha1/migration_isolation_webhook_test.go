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
		{"foreign pod environment", "alice", "", "Pod", `{"spec":{"containers":[{"env":[{"valueFrom":{"secretKeyRef":{"name":"migration-proxy-x"}}]}]}}`, false},
		{"foreign job", "alice", "batch", "Job", `{"spec":{"template":{"spec":{"volumes":[{"secret":{"secretName":"migration-proxy-x"}}],"containers":[{}]}}}}`, false},
		{"operator job", v.OperatorUsername, "batch", "Job", `{"spec":{"template":{"spec":{"volumes":[{"secret":{"secretName":"migration-proxy-x"}}],"containers":[{}]}}}}`, true},
		{"job controller pod", "system:serviceaccount:kube-system:job-controller", "", "Pod", `{"spec":{"volumes":[{"secret":{"secretName":"migration-proxy-x"}}],"containers":[{}]}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := v.Handle(context.Background(), request(tc.user, tc.group, tc.kind, tc.body)).Allowed; got != tc.allowed {
				t.Fatalf("allowed=%v, want %v", got, tc.allowed)
			}
		})
	}
}

func authentication(username string) authenticationv1.UserInfo {
	return authenticationv1.UserInfo{Username: username}
}
func runtimeRaw(body string) runtime.RawExtension { return runtime.RawExtension{Raw: []byte(body)} }
