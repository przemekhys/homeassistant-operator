package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const migrationTargetLabel = "ha.homeassistant.io/migration-target"

// migrationTargetSelector derives a stable label value from the actual object
// UID. This is a selector only; it is not an authentication credential.
func migrationTargetSelector(uid string) string {
	if uid == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(uid))
	return hex.EncodeToString(digest[:20])
}

// buildBoundMigrationTargetPolicy constructs only. It neither creates a policy
// nor claims CNI enforcement, pod exclusivity, or authorization to start HA.
func buildBoundMigrationTargetPolicy(scheme *runtime.Scheme, op *ha.HomeAssistantMigration, target *hav1.HomeAssistant, request ha.MigrationExecutionRequest) (*networkingv1.NetworkPolicy, error) {
	if scheme == nil || op == nil || target == nil || op.UID == "" || op.Namespace == "" ||
		op.Spec.CancelRequested || !op.DeletionTimestamp.IsZero() || migrationprotocol.AbortRequested(op) ||
		(request.Stage != "upload" && request.Stage != "restore") || migrationprotocol.ValidateAccepted(op) != nil ||
		target.Namespace != op.Namespace || target.Name != op.Spec.Target.Name || string(target.UID) != op.Spec.Target.UID || target.UID == "" {
		return nil, fmt.Errorf("target isolation binding unavailable")
	}
	accepted := false
	for _, r := range op.Spec.ExecutionRequests {
		if reflect.DeepEqual(r, request) {
			accepted = true
		}
	}
	if !accepted {
		return nil, fmt.Errorf("target isolation request not accepted")
	}
	selector := migrationTargetSelector(string(target.UID))
	worker := strings.TrimPrefix(request.RequestID, "sha256:")[:40]
	policy := MigrationTargetIsolationPolicy(op.Namespace, "migration-target-"+selector, selector, worker)
	if policy == nil {
		return nil, fmt.Errorf("target isolation policy invalid")
	}
	if err := controllerutil.SetControllerReference(op, policy, scheme); err != nil {
		return nil, fmt.Errorf("target isolation owner unavailable")
	}
	return policy, nil
}

// ensureStoppedTargetPolicy creates but never adopts or changes a policy.
// It is preparation only: callers must not infer enforcement or start HA from
// a successful return. Direct, fresh reads are required; no caller may supply
// a cached reader for these checks.
//
//nolint:gocyclo // Validation and lifecycle branches are intentionally fail-closed.
func ensureStoppedTargetPolicy(ctx context.Context, writer client.Client, reader client.Reader, scheme *runtime.Scheme, op *ha.HomeAssistantMigration, target *hav1.HomeAssistant, request ha.MigrationExecutionRequest) error {
	if ctx == nil || writer == nil || reader == nil || op == nil || target == nil {
		return fmt.Errorf("isolation inputs unavailable")
	}
	currentOp := &ha.HomeAssistantMigration{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(op), currentOp); err != nil || currentOp.UID != op.UID {
		return fmt.Errorf("migration unavailable")
	}
	currentTarget := &hav1.HomeAssistant{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(target), currentTarget); err != nil || currentTarget.UID != target.UID || !migrationRestorePreparationEnabled(currentTarget) {
		return fmt.Errorf("restore target unavailable")
	}
	policy, err := buildBoundMigrationTargetPolicy(scheme, currentOp, currentTarget, request)
	if err != nil {
		return err
	}
	sts := &appsv1.StatefulSet{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(target), sts); err != nil {
		return fmt.Errorf("target workload unavailable")
	}
	owner := metav1.GetControllerOf(sts)
	if owner == nil || owner.UID != target.UID || sts.Spec.Replicas == nil || *sts.Spec.Replicas != 0 ||
		sts.Status.Replicas != 0 || sts.Spec.Selector == nil ||
		sts.Spec.Template.Labels[migrationTargetLabel] != migrationTargetSelector(string(target.UID)) {
		return fmt.Errorf("target workload is not stopped")
	}
	selector, err := metav1.LabelSelectorAsSelector(sts.Spec.Selector)
	if err != nil || selector.Empty() || !selector.Matches(labels.Set(sts.Spec.Template.Labels)) {
		return fmt.Errorf("target workload selector invalid")
	}
	pods := &corev1.PodList{}
	if err := reader.List(ctx, pods, client.InNamespace(op.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil || len(pods.Items) != 0 {
		return fmt.Errorf("target pods remain")
	}
	existing := &networkingv1.NetworkPolicy{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(policy), existing); err == nil {
		owner = metav1.GetControllerOf(existing)
		if owner == nil || owner.UID != op.UID || !reflect.DeepEqual(existing.Spec, policy.Spec) {
			return fmt.Errorf("target policy collision")
		}
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("target policy unavailable")
	}
	if err := writer.Create(ctx, policy); err != nil {
		return fmt.Errorf("target policy creation outcome unknown")
	}
	return nil
}

// observeTargetPolicySnapshot detects API-visible additive ingress on the
// actual target template labels. It does not prove enforcement, identity, or
// protection against changes after the read and must not authorize start.
func observeTargetPolicySnapshot(ctx context.Context, reader client.Reader, scheme *runtime.Scheme, op *ha.HomeAssistantMigration, target *hav1.HomeAssistant, request ha.MigrationExecutionRequest) error {
	if ctx == nil || reader == nil || target == nil || op == nil {
		return fmt.Errorf("target policy observation unavailable")
	}
	want, err := buildBoundMigrationTargetPolicy(scheme, op, target, request)
	if err != nil {
		return err
	}
	current := &networkingv1.NetworkPolicy{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(want), current); err != nil {
		return fmt.Errorf("target policy unavailable")
	}
	owner := metav1.GetControllerOf(current)
	if owner == nil || owner.UID != op.UID || current.UID == "" || !reflect.DeepEqual(current.Spec, want.Spec) {
		return fmt.Errorf("target policy binding invalid")
	}
	want.UID = current.UID
	sts := &appsv1.StatefulSet{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(target), sts); err != nil {
		return fmt.Errorf("target workload unavailable")
	}
	owner = metav1.GetControllerOf(sts)
	if owner == nil || owner.UID != target.UID || sts.Spec.Template.Labels[migrationTargetLabel] != migrationTargetSelector(string(target.UID)) {
		return fmt.Errorf("target workload binding invalid")
	}
	policies := &networkingv1.NetworkPolicyList{}
	if err := reader.List(ctx, policies, client.InNamespace(op.Namespace)); err != nil || !MigrationPolicySetExclusive(want, policies.Items, sts.Spec.Template.Labels) {
		return fmt.Errorf("conflicting target ingress policy")
	}
	return nil
}

// MigrationTargetIsolationPolicy is a candidate, not proof that a CNI enforces
// it. Pod label control and node/host-network access remain trust assumptions.
func MigrationTargetIsolationPolicy(namespace, name, targetID, workerID string) *networkingv1.NetworkPolicy {
	if len(validation.IsDNS1123Label(namespace)) != 0 || len(validation.IsDNS1123Subdomain(name)) != 0 ||
		targetID == "" || workerID == "" || len(validation.IsValidLabelValue(targetID)) != 0 ||
		len(validation.IsValidLabelValue(workerID)) != 0 {
		return nil
	}
	tcp := corev1.ProtocolTCP
	// HA's unauthenticated port remains unreachable. Only the mTLS proxy port
	// is exposed to the pinned worker identity.
	port := intstr.FromInt32(8124)
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{migrationTargetLabel: targetID}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From:  []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"ha.homeassistant.io/migration-worker": workerID}}}},
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &port}},
			}},
		},
	}
}

// MigrationPolicySetExclusive rejects additive ingress allowances. Its result
// is only a snapshot of API objects, not a runtime enforcement or identity proof.
// In particular it must never by itself release the restore-start gate.
func MigrationPolicySetExclusive(want *networkingv1.NetworkPolicy, policies []networkingv1.NetworkPolicy, podLabels map[string]string) bool {
	if want == nil || want.UID == "" || want.Namespace == "" || len(podLabels) == 0 {
		return false
	}
	if len(want.Spec.Ingress) != 1 || len(want.Spec.Ingress[0].From) != 1 || want.Spec.Ingress[0].From[0].PodSelector == nil {
		return false
	}
	targetID := want.Spec.PodSelector.MatchLabels[migrationTargetLabel]
	workerID := want.Spec.Ingress[0].From[0].PodSelector.MatchLabels["ha.homeassistant.io/migration-worker"]
	expected := MigrationTargetIsolationPolicy(want.Namespace, want.Name, targetID, workerID)
	if expected == nil || !reflect.DeepEqual(want.Spec, expected.Spec) {
		return false
	}
	found := false
	for _, policy := range policies {
		if policy.Namespace != want.Namespace {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(&policy.Spec.PodSelector)
		if err != nil {
			return false
		}
		if !selector.Matches(labels.Set(podLabels)) {
			continue
		}
		if len(policy.Spec.Ingress) == 0 {
			continue
		}
		ingress := len(policy.Spec.PolicyTypes) == 0
		for _, typ := range policy.Spec.PolicyTypes {
			if typ == networkingv1.PolicyTypeIngress {
				ingress = true
			}
		}
		if !ingress {
			continue
		}
		if found || policy.Name != want.Name || policy.UID != want.UID || !reflect.DeepEqual(policy.Spec, want.Spec) {
			return false
		}
		found = true
	}
	return found
}
