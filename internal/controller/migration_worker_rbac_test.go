package controller

import (
	"context"
	"reflect"
	"testing"

	"github.com/przemekhys/homeassistant-operator/internal/migration"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestBackupWorkerRBACBindsAcceptedRequest(t *testing.T) {
	ctx := context.Background()
	kube, op := requestTestObjects(t)
	r, err := acceptBackupRequest(ctx, kube, kube, op)
	if err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(op), op); err != nil {
		t.Fatal(err)
	}
	if err := rbacv1.AddToScheme(kube.Scheme()); err != nil {
		t.Fatal(err)
	}
	sa, role, binding, err := BuildBackupWorkerRBAC(kube.Scheme(), op, *r, "worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range []metav1.Object{sa, role, binding} {
		if owner := metav1.GetControllerOf(obj); owner == nil || owner.UID != op.UID {
			t.Fatal("missing operation owner")
		}
	}
	if binding.RoleRef.Name != role.Name || len(binding.Subjects) != 1 || binding.Subjects[0].Name != sa.Name || binding.Subjects[0].Namespace != op.Namespace {
		t.Fatal("binding is not scoped")
	}
	for _, rule := range role.Rules {
		for _, resource := range rule.Resources {
			if resource == "secrets" && (!reflect.DeepEqual(rule.Verbs, []string{"get"}) || len(rule.ResourceNames) == 0) {
				t.Fatal("unscoped secret access")
			}
			if resource == "secrets" && (!containsName(rule.ResourceNames, r.SourceToken.Name) || !containsName(rule.ResourceNames, r.RecoveryKey.Name)) {
				t.Fatal("missing pinned secrets")
			}
			if resource == "configmaps" && containsName(rule.Verbs, "update") && !reflect.DeepEqual(rule.ResourceNames, []string{migration.WorkerReceiptName(r.RequestID)}) {
				t.Fatal("worker can update other configmaps")
			}
		}
	}
	foreign := *r
	foreign.OperationUID = "foreign"
	if _, _, _, err := BuildBackupWorkerRBAC(kube.Scheme(), op, foreign, "worker"); err == nil {
		t.Fatal("foreign request accepted")
	}
	cancelled := op.DeepCopy()
	cancelled.Spec.CancelRequested = true
	if _, _, _, err := BuildBackupWorkerRBAC(kube.Scheme(), cancelled, *r, "worker"); err == nil {
		t.Fatal("cancelled operation granted worker access")
	}
}

func containsName(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}
