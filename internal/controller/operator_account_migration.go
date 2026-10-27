package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	reasonLegacyCredentialMissing       = "LegacyCredentialMissing"
	reasonLegacyCredentialMalformed     = "LegacyCredentialMalformed"
	reasonLegacyCredentialRejected      = "LegacyCredentialRejected"
	reasonLegacyCredentialRequiresAdmin = "LegacyCredentialRequiresAdmin"
	reasonMigrationFailed               = "MigrationFailed"
)

// The managed ledger preserves the source name across status loss. Only a
// distinct legacy Secret can authorize migration; the target is never a source.
func (r *HomeAssistantReconciler) legacyMigrationSecretName(ha *hav1.HomeAssistant, secret *corev1.Secret) string {
	if name := secret.Annotations[operatorLegacyAnnotation]; name != "" {
		return name
	}
	if ha.Status.Bootstrap == nil {
		return ""
	}
	name := ha.Status.Bootstrap.APITokenSecretName
	if name == "" && ha.Status.Bootstrap.Completed {
		name = r.getAPITokenSecretName(ha)
	}
	if name == ha.Spec.Bootstrap.OperatorAccount.TokenSecretName {
		return ""
	}
	return name
}

// Migration uses an already-issued administrative API credential, so owner MFA
// remains enabled and no bootstrap password or second factor is needed.
func (r *HomeAssistantReconciler) migrateOperatorAccount(
	ctx context.Context, ha *hav1.HomeAssistant, secret *corev1.Secret, c *haclient.Client, sourceName string,
) (ctrl.Result, error) {
	if sourceName == "" || sourceName == secret.Name {
		return r.operatorAccountStatus(ctx, ha, secret, reasonLegacyCredentialMalformed,
			"Migration requires a legacy token Secret distinct from the dedicated token Secret")
	}
	if secret.Annotations[operatorLegacyAnnotation] != sourceName {
		secret.Annotations[operatorLegacyAnnotation] = sourceName
		if err := r.Update(ctx, secret); err != nil {
			return r.operatorSecretWriteFailure(ctx, ha, err)
		}
	}
	legacy := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: sourceName, Namespace: ha.Namespace}, legacy); err != nil {
		reason := reasonHomeAssistantUnavailable
		if apierrors.IsNotFound(err) {
			reason = reasonLegacyCredentialMissing
		}
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			reason = reasonSecretAccessDenied
		}
		return r.operatorAccountStatus(ctx, ha, secret, reason,
			"Cannot read legacy token Secret; restore it in the HomeAssistant namespace and check Secret permissions")
	}
	token := string(legacy.Data[apiTokenSecretKeyName])
	if legacy.Type != corev1.SecretTypeOpaque || strings.TrimSpace(token) == "" ||
		strings.TrimSpace(token) != token || strings.ContainsAny(token, " \t\r\n") {
		return r.operatorAccountStatus(
			ctx,
			ha,
			secret,
			reasonLegacyCredentialMalformed,
			"Restore a non-empty token key in the Opaque legacy Secret; migration does not fall back to owner password login",
		)
	}
	authCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	principal, err := c.CurrentUser(authCtx, token)
	if err != nil {
		if haclient.IsBanned(err) {
			return r.handleSelfBan(ctx, ha, err)
		}
		reason := reasonHomeAssistantUnavailable
		var haErr *haclient.Error
		if errors.As(err, &haErr) && haErr.Type == haclient.ErrorTypeAuth {
			reason = reasonLegacyCredentialRejected
		}
		return r.operatorAccountStatus(
			ctx,
			ha,
			secret,
			reason,
			"Cannot authenticate the legacy token; check Home Assistant availability or restore a valid legacy API credential",
		)
	}
	if principal.ID == secret.Annotations[operatorUserAnnotation] {
		return r.operatorAccountStatus(ctx, ha, secret, reasonLegacyCredentialRejected,
			"Migration source token identifies the dedicated user instead of the existing administrator")
	}
	if !principal.IsAdmin {
		return r.operatorAccountStatus(ctx, ha, secret, reasonLegacyCredentialRequiresAdmin,
			"Migration requires a legacy token with Home Assistant administrator permissions")
	}
	if err := r.applyOperatorBootstrapCoreConfig(ctx, ha, c, token, true); err != nil {
		return r.operatorCoreConfigFailure(ctx, ha, secret, err)
	}
	return r.completeOperatorProvisioning(ctx, ha, secret, c, token, false)
}

// Resume requested onboarding settings using the same administrative credential
// as account provisioning, including the legacy token during migration.
func (r *HomeAssistantReconciler) applyOperatorBootstrapCoreConfig(
	ctx context.Context, ha *hav1.HomeAssistant, c *haclient.Client, token string, resume bool,
) error {
	if !resume {
		return nil
	}
	req := r.buildCoreConfigRequest(ha)
	if req == nil {
		return nil
	}
	return c.ResumeBootstrapCoreConfig(ctx, token, req, ha.Spec.Bootstrap.Analytics)
}

func (r *HomeAssistantReconciler) operatorCoreConfigFailure(
	ctx context.Context, ha *hav1.HomeAssistant, secret *corev1.Secret, err error,
) (ctrl.Result, error) {
	if haclient.IsBanned(err) {
		return r.handleSelfBan(ctx, ha, err)
	}
	return r.operatorAccountStatus(ctx, ha, secret, reasonHomeAssistantUnavailable,
		fmt.Sprintf("Requested core configuration (including country) was not applied: %v", err))
}
