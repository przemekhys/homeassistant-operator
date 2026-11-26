package controller

import (
	"fmt"
	"reflect"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migration"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// BuildBackupWorkerRBAC constructs namespaced permissions; it does not establish
// isolation or create resources. ConfigMap create cannot be resourceNames-scoped
// by Kubernetes RBAC and must be constrained by admission in a production setup.
func BuildBackupWorkerRBAC(scheme *runtime.Scheme, op *ha.HomeAssistantMigration, r ha.MigrationExecutionRequest, name string) (*corev1.ServiceAccount, *rbacv1.Role, *rbacv1.RoleBinding, error) {
	if scheme == nil || op == nil || op.Namespace == "" || op.UID == "" || op.Spec.CancelRequested ||
		!op.DeletionTimestamp.IsZero() || migrationprotocol.AbortRequested(op) ||
		len(validation.IsDNS1123Subdomain(name)) != 0 || r.Stage != "backup" || r.SourceToken == nil ||
		r.RecoveryKey == nil || r.ArchiveStorage == nil || migrationprotocol.ValidateAccepted(op) != nil {
		return nil, nil, nil, fmt.Errorf("backup worker RBAC inputs invalid")
	}
	accepted := false
	for _, request := range op.Spec.ExecutionRequests {
		if reflect.DeepEqual(request, r) {
			accepted = true
		}
	}
	if !accepted {
		return nil, nil, nil, fmt.Errorf("backup request not accepted")
	}
	configMaps := []string{migration.WorkerReceiptName(r.RequestID), migration.WorkerReceiptName(r.RequestID) + "-source", r.ArchiveStorage.DescriptorName}
	resolution, err := migrationprotocol.ObservationResolution(op, r.Stage, r.RequestID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ambiguous resolution")
	}
	if resolution != nil {
		if migrationprotocol.ValidateEvidenceRef(resolution.EvidenceRef) != nil {
			return nil, nil, nil, fmt.Errorf("invalid evidence reference")
		}
	}
	meta := metav1.ObjectMeta{Name: name, Namespace: op.Namespace}
	sa := &corev1.ServiceAccount{ObjectMeta: *meta.DeepCopy()}
	role := &rbacv1.Role{ObjectMeta: *meta.DeepCopy(), Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{ha.GroupVersion.Group}, Resources: []string{"homeassistantmigrations"}, ResourceNames: []string{op.Name}, Verbs: []string{"get", "update"}},
		{APIGroups: []string{""}, Resources: []string{"secrets"}, ResourceNames: []string{r.SourceToken.Name, r.RecoveryKey.Name}, Verbs: []string{"get"}},
		{APIGroups: []string{""}, Resources: []string{"persistentvolumeclaims"}, ResourceNames: []string{r.ArchiveStorage.PVCName}, Verbs: []string{"get"}},
		{APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: configMaps, Verbs: []string{"get"}},
		{APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: []string{migration.WorkerReceiptName(r.RequestID)}, Verbs: []string{"update"}},
		{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"create"}},
	}}
	if resolution != nil {
		role.Rules = append(role.Rules, rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: []string{resolution.EvidenceRef.Name}, Verbs: []string{"get"}})
	}
	binding := &rbacv1.RoleBinding{ObjectMeta: *meta.DeepCopy(), RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: op.Namespace}}}
	for _, object := range []metav1.Object{sa, role, binding} {
		if err := controllerutil.SetControllerReference(op, object, scheme); err != nil {
			return nil, nil, nil, fmt.Errorf("worker RBAC owner unavailable")
		}
	}
	return sa, role, binding, nil
}
