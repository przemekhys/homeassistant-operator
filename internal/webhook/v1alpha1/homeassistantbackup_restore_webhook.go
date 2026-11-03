package v1alpha1

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
)

const restoreApprovalAnnotation = "ha.homeassistant.io/restore-approval"

// +kubebuilder:webhook:path=/validate-ha-homeassistant-io-v1alpha1-homeassistantbackup,mutating=false,failurePolicy=fail,sideEffects=None,groups=ha.homeassistant.io,resources=homeassistantbackups,verbs=create;update,versions=v1alpha1,name=vhomeassistantbackup-v1alpha1.kb.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-ha-homeassistant-io-v1alpha1-homeassistantrestore,mutating=false,failurePolicy=fail,sideEffects=None,groups=ha.homeassistant.io,resources=homeassistantrestores,verbs=create;update,versions=v1alpha1,name=vhomeassistantrestore-v1alpha1.kb.io,admissionReviewVersions=v1

type restoreApproval struct {
	OperationUID string `json:"operationUID"`
	TargetUID    string `json:"targetUID"`
	PlanDigest   string `json:"planDigest"`
	Challenge    string `json:"challenge"`
}

type backupValidator struct{}

var _ admission.Validator[*hav1alpha1.HomeAssistantBackup] = &backupValidator{}

func SetupBackupRestoreWebhooksWithManager(mgr ctrl.Manager) error {
	builder := ctrl.NewWebhookManagedBy(mgr, &hav1alpha1.HomeAssistantBackup{}).WithValidator(&backupValidator{})
	if err := builder.Complete(); err != nil {
		return err
	}
	return ctrl.NewWebhookManagedBy(mgr, &hav1alpha1.HomeAssistantRestore{}).
		WithValidator(&restoreValidator{}).
		Complete()
}

func (*backupValidator) ValidateCreate(
	_ context.Context,
	obj *hav1alpha1.HomeAssistantBackup,
) (admission.Warnings, error) {
	if obj.Spec.HomeAssistantRef.Name == "" || obj.Spec.HomeAssistantRef.UID == "" {
		return nil, fmt.Errorf("spec.homeAssistantRef.name and spec.homeAssistantRef.uid are required")
	}
	return nil, nil
}

func (*backupValidator) ValidateUpdate(
	_ context.Context,
	oldObj, newObj *hav1alpha1.HomeAssistantBackup,
) (admission.Warnings, error) {
	if !reflect.DeepEqual(oldObj.Spec, newObj.Spec) {
		return nil, fmt.Errorf("spec is immutable after creation")
	}
	return nil, nil
}
func (*backupValidator) ValidateDelete(context.Context, *hav1alpha1.HomeAssistantBackup) (admission.Warnings, error) {
	return nil, nil
}

type restoreValidator struct{}

var _ admission.Validator[*hav1alpha1.HomeAssistantRestore] = &restoreValidator{}

func (*restoreValidator) ValidateCreate(
	_ context.Context,
	obj *hav1alpha1.HomeAssistantRestore,
) (admission.Warnings, error) {
	if err := validateRestore(obj); err != nil {
		return nil, err
	}
	return nil, nil
}

func (*restoreValidator) ValidateUpdate(
	_ context.Context,
	oldObj, newObj *hav1alpha1.HomeAssistantRestore,
) (admission.Warnings, error) {
	if err := validateRestore(newObj); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(oldObj.Spec, newObj.Spec) {
		return nil, fmt.Errorf("spec is immutable after creation")
	}
	if raw := newObj.Annotations[restoreApprovalAnnotation]; raw != "" {
		var approval restoreApproval
		if err := json.Unmarshal([]byte(raw), &approval); err != nil || approval.OperationUID == "" ||
			approval.TargetUID == "" ||
			approval.PlanDigest == "" ||
			approval.Challenge == "" {
			return nil, fmt.Errorf("%s must be a complete JSON approval binding", restoreApprovalAnnotation)
		}
	}
	return nil, nil
}
func (*restoreValidator) ValidateDelete(context.Context, *hav1alpha1.HomeAssistantRestore) (admission.Warnings, error) {
	return nil, nil
}

func validateRestore(obj *hav1alpha1.HomeAssistantRestore) error {
	if obj.Spec.HomeAssistantRef.Name == "" || obj.Spec.HomeAssistantRef.UID == "" {
		return fmt.Errorf("spec.homeAssistantRef.name and spec.homeAssistantRef.uid are required")
	}
	if obj.Spec.BackupID == "" {
		return fmt.Errorf("spec.backupID is required")
	}
	if (obj.Spec.Source.AgentID == "") == (obj.Spec.Source.Uploaded == nil) {
		return fmt.Errorf("exactly one of spec.source.agentID or spec.source.uploaded is required")
	}
	if !obj.Spec.Scope.HomeAssistant {
		return fmt.Errorf("spec.scope.homeAssistant must be true")
	}
	return nil
}
