package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	operatorUserAnnotation           = "homeassistant-operator.io/operator-user-id"
	operatorStageAnnotation          = "homeassistant-operator.io/credential-stage"
	operatorRecoveryAnnotation       = "homeassistant-operator.io/last-recovery-request-id"
	operatorRecoveryResultAnnotation = "homeassistant-operator.io/recovery-result"
	operatorLegacyAnnotation         = "homeassistant-operator.io/legacy-secret-name"
	operatorAdminGroup               = "system-admin"
)

// reconcileOperatorAccount observes the managed Secret before considering any
// owner credential. A missing or rejected verified credential never provisions.
func (r *HomeAssistantReconciler) reconcileOperatorAccount(
	ctx context.Context,
	ha *hav1.HomeAssistant,
) (ctrl.Result, error) {
	if !ha.Spec.Bootstrap.Enabled {
		return ctrl.Result{}, nil
	}
	account := ha.Spec.Bootstrap.OperatorAccount
	if account.TokenSecretName == "" {
		return r.operatorAccountStatus(
			ctx,
			ha,
			nil,
			reasonCredentialMalformed,
			"Dedicated mode requires tokenSecretName",
		)
	}
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: account.TokenSecretName, Namespace: ha.Namespace}, secret)
	if unexpectedOperatorSecretError(err) {
		reason := reasonHomeAssistantUnavailable
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			reason = reasonSecretAccessDenied
		}
		return r.operatorAccountStatus(
			ctx,
			ha,
			nil,
			reason,
			"Cannot read the operator Secret; check managed-namespace permissions",
		)
	}
	exists := err == nil
	if exists {
		if err := validateOperatorSecret(ha, secret); err != nil {
			return r.operatorAccountStatus(
				ctx,
				ha,
				nil,
				reasonCredentialMalformed,
				"Conflicting operator Secret; use an unused name or restore its managed ownership and metadata",
			)
		}
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
	}
	observedRequest := ""
	verifiedBefore := false
	if ha.Status.Bootstrap != nil && ha.Status.Bootstrap.OperatorAccount != nil {
		status := ha.Status.Bootstrap.OperatorAccount
		observedRequest = status.LastRecoveryRequestID
		verifiedBefore = status.Phase == "Verified" || status.TokenSecretName != ""
	}
	if exists {
		observedRequest = secret.Annotations[operatorRecoveryAnnotation]
		verifiedBefore = verifiedBefore || secret.Annotations[operatorStageAnnotation] == "Verified"
	}
	recovery := account.RecoveryRequestID != "" && account.RecoveryRequestID != observedRequest
	haClient := newHAClientForHA(ha, r.NewHAClient).WithTimeout(10 * time.Second)
	if exists && !recovery && secret.Annotations[operatorStageAnnotation] == "Recovering" {
		return r.operatorAccountStatus(
			ctx,
			ha,
			secret,
			reasonRecoveryFailed,
			"Recovery was interrupted or failed; correct bootstrap owner credentials and submit a new recoveryRequestID",
		)
	}
	if exists && len(secret.Data[apiTokenSecretKeyName]) > 0 && !recovery {
		return r.observeOperatorToken(ctx, ha, secret, haClient)
	}
	if !recovery &&
		(verifiedBefore || (exists && secret.Annotations[operatorStageAnnotation] == "Issuing") || observedRequest != "") {
		reason := reasonCredentialMissing
		if exists {
			reason = reasonCredentialMalformed
		}
		if observedRequest != "" && exists && secret.Annotations[operatorRecoveryResultAnnotation] == "Failed" {
			reason = reasonRecoveryFailed
		}
		return r.operatorAccountStatus(
			ctx,
			ha,
			secretIfExists(secret, exists),
			reason,
			"Restore the operator Secret or submit a new recoveryRequestID with valid bootstrap owner credentials",
		)
	}
	return r.runOperatorProvisioning(ctx, ha, secret, haClient, exists, recovery)
}

func unexpectedOperatorSecretError(err error) bool {
	return err != nil && !apierrors.IsNotFound(err)
}

func (r *HomeAssistantReconciler) observeOperatorToken(
	ctx context.Context,
	ha *hav1.HomeAssistant,
	secret *corev1.Secret,
	c *haclient.Client,
) (ctrl.Result, error) {
	token := string(secret.Data[apiTokenSecretKeyName])
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if strings.TrimSpace(token) != token || strings.ContainsAny(token, " \r\n\t") {
		return r.operatorAccountStatus(ctx, ha, secret, reasonCredentialMalformed,
			"Operator token is malformed; restore it or submit a new recoveryRequestID")
	}
	// One bounded authenticated query verifies availability, principal, and role;
	// a valid token belonging to the human owner is not a dedicated credential.
	user, healthErr := c.CurrentUser(ctx, token)
	if healthErr != nil {
		reason := reasonHomeAssistantUnavailable
		var haErr *haclient.Error
		if errors.As(healthErr, &haErr) && haErr.Type == haclient.ErrorTypeAuth {
			reason = reasonCredentialRejected
		}
		return r.operatorAccountStatus(ctx, ha, secret, reason,
			"Check service availability or request explicit credential recovery")
	}
	if user.ID != secret.Annotations[operatorUserAnnotation] || user.IsOwner {
		return r.operatorAccountStatus(ctx, ha, secret, reasonCredentialRejected,
			"Token identifies another user; restore the dedicated credential or request recovery")
	}
	if !user.IsAdmin {
		return r.operatorAccountStatus(ctx, ha, secret, reasonRequiresElevatedRole,
			"The dedicated identity requires the documented administrator role")
	}
	if secret.Annotations[operatorStageAnnotation] != "Verified" {
		secret.Annotations[operatorStageAnnotation] = "Verified"
		if secret.Annotations[operatorRecoveryAnnotation] != "" {
			secret.Annotations[operatorRecoveryResultAnnotation] = "Succeeded"
		}
		if err := r.Update(ctx, secret); err != nil {
			return ctrl.Result{}, err
		}
	}
	return r.operatorAccountStatus(ctx, ha, secret, "Verified",
		"Dedicated credential verified; bootstrap credentials retired from routine use")
}

func (r *HomeAssistantReconciler) runOperatorProvisioning(
	ctx context.Context,
	ha *hav1.HomeAssistant,
	secret *corev1.Secret,
	haClient *haclient.Client,
	exists, recovery bool,
) (ctrl.Result, error) {
	account := ha.Spec.Bootstrap.OperatorAccount
	// Reserve the namespace-local ledger before external account mutations. It
	// stores only identifiers until a token is issued, never a temporary password.
	if !exists {
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      account.TokenSecretName,
				Namespace: ha.Namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "homeassistant-operator",
					operatorPurposeLabel:           "operator-api-token",
				},
				Annotations: map[string]string{operatorStageAnnotation: "Provisioning"},
			},
			Type: corev1.SecretTypeOpaque,
		}
		if ha.Status.Bootstrap != nil {
			if previous := ha.Status.Bootstrap.OperatorAccount; previous != nil {
				secret.Annotations[operatorUserAnnotation] = previous.UserID
			}
			legacy := ha.Status.Bootstrap.APITokenSecretName
			if legacy != "" && legacy != account.TokenSecretName {
				secret.Annotations[operatorLegacyAnnotation] = legacy
			}
		}
		if err := controllerutil.SetControllerReference(ha, secret, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, secret); err != nil {
			return r.operatorSecretWriteFailure(ctx, ha, err)
		}
	}
	if recovery {
		// Consume before reading bootstrap access. Failed and interrupted requests
		// require a new ID; status loss cannot replay a consumed request.
		secret.Annotations[operatorRecoveryAnnotation] = account.RecoveryRequestID
		secret.Annotations[operatorRecoveryResultAnnotation] = "Failed"
		secret.Annotations[operatorStageAnnotation] = "Recovering"
		if err := r.Update(ctx, secret); err != nil {
			return r.operatorSecretWriteFailure(ctx, ha, err)
		}
	}
	if err := r.validateBootstrapConfig(ha); err != nil {
		return r.operatorAccountStatus(
			ctx,
			ha,
			secret,
			reasonRecoveryFailed,
			"Supply a bootstrap owner credentials Secret; it is never recreated automatically",
		)
	}
	username, password, err := r.getBootstrapCredentials(ctx, ha)
	if err != nil {
		return r.operatorAccountStatus(
			ctx,
			ha,
			secret,
			reasonRecoveryFailed,
			"Bootstrap owner credentials unavailable; restore the referenced Secret and submit a new recoveryRequestID",
		)
	}
	if !recovery {
		if err := haClient.CheckAPIReady(ctx); err != nil {
			return r.operatorAccountStatus(
				ctx,
				ha,
				secret,
				reasonHomeAssistantUnavailable,
				"Waiting for Home Assistant API readiness",
			)
		}
		opts := &haclient.BootstrapOptions{
			EnableAnalytics: ha.Spec.Bootstrap.Analytics,
			CoreConfig:      r.buildCoreConfigRequest(ha),
		}
		_, err = haClient.PerformBootstrap(
			ctx,
			username,
			password,
			getOrDefault(ha.Spec.Bootstrap.OwnerName, defaultOwnerName),
			getOrDefault(ha.Spec.Bootstrap.Language, defaultLanguage),
			opts,
		)
		if err != nil && !haclient.IsOnboardingDone(err) {
			return r.operatorAccountStatus(
				ctx,
				ha,
				secret,
				reasonHomeAssistantUnavailable,
				"Waiting for Home Assistant onboarding",
			)
		}
	}
	session, err := haClient.LoginWithCredentials(ctx, username, password)
	if err != nil {
		return r.operatorAccountStatus(
			ctx,
			ha,
			secret,
			reasonRecoveryFailed,
			"Cannot authenticate bootstrap owner; correct credentials and submit a new recoveryRequestID",
		)
	}
	if err := r.provisionOperatorCredential(ctx, ha, secret, haClient, session.AccessToken, recovery); err != nil {
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			return r.operatorAccountStatus(ctx, ha, secret, reasonSecretAccessDenied,
				"Cannot write managed token Secret; check managed-namespace permissions")
		}
		return r.operatorAccountStatus(
			ctx,
			ha,
			secret,
			reasonRecoveryFailed,
			"Dedicated credential provisioning failed; check the owner account and submit a new recoveryRequestID",
		)
	}
	return r.operatorAccountStatus(
		ctx,
		ha,
		secret,
		"Verified",
		"Dedicated operator credential verified; bootstrap credentials are retired from routine use",
	)
}

func (r *HomeAssistantReconciler) operatorSecretWriteFailure(
	ctx context.Context, ha *hav1.HomeAssistant, err error,
) (ctrl.Result, error) {
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return r.operatorAccountStatus(ctx, ha, nil, reasonSecretAccessDenied,
			"Cannot write operator Secret; check managed-namespace Secret permissions")
	}
	return ctrl.Result{}, err
}

func secretIfExists(secret *corev1.Secret, exists bool) *corev1.Secret {
	if exists {
		return secret
	}
	return nil
}

func (r *HomeAssistantReconciler) provisionOperatorCredential(
	ctx context.Context,
	ha *hav1.HomeAssistant,
	secret *corev1.Secret,
	c *haclient.Client,
	ownerToken string,
	recovery bool,
) error {
	user, err := r.ensureOperatorUser(ctx, ha, secret, c, ownerToken)
	if err != nil {
		return err
	}
	return r.issueOperatorCredential(ctx, ha, secret, c, user, ownerToken, recovery)
}

func (r *HomeAssistantReconciler) ensureOperatorUser(
	ctx context.Context,
	ha *hav1.HomeAssistant,
	secret *corev1.Secret,
	c *haclient.Client,
	ownerToken string,
) (*haclient.AuthUser, error) {
	users, err := c.ListUsers(ctx, ownerToken)
	if err != nil {
		return nil, err
	}
	// The UID makes the intended name stable across restarts and unique across
	// replacement Kubernetes resources. Never adopt a human owner identity.
	name := "Kubernetes operator " + string(ha.UID)
	username := "ha_operator_" + string(ha.UID)
	userID := secret.Annotations[operatorUserAnnotation]
	var user *haclient.AuthUser
	for i := range users {
		if (userID != "" && users[i].ID == userID) || (userID == "" && users[i].Name == name) {
			if user != nil {
				return nil, fmt.Errorf("ambiguous operator identity")
			}
			user = &users[i]
		}
	}
	if user != nil && (user.IsOwner || user.Name != name || (user.Username != "" && user.Username != username)) {
		return nil, fmt.Errorf("conflicting operator identity")
	}
	if user != nil && userID == "" && secret.Annotations[operatorStageAnnotation] != "Creating" &&
		secret.Annotations[operatorStageAnnotation] != "Recovering" {
		return nil, fmt.Errorf("existing identity lacks operator creation intent")
	}
	if user == nil {
		if userID != "" {
			return nil, fmt.Errorf("dedicated identity missing; manual repair required")
		}
		// Persist creation intent before the external operation. A retry queries
		// the deterministic identity instead of blindly creating another user.
		if secret.Annotations[operatorStageAnnotation] == "Creating" {
			return nil, fmt.Errorf("ambiguous user creation; manual repair required")
		}
		secret.Annotations[operatorStageAnnotation] = "Creating"
		if err := r.Update(ctx, secret); err != nil {
			return nil, err
		}
		user, err = c.CreateAuthUser(ctx, ownerToken, name, []string{operatorAdminGroup})
		if err != nil {
			return nil, err
		}
	}
	secret.Annotations[operatorUserAnnotation] = user.ID
	if err := r.Update(ctx, secret); err != nil {
		return nil, err
	}
	if !slices.Contains(user.GroupIDs, operatorAdminGroup) {
		if err := c.UpdateAuthUser(ctx, ownerToken, user.ID, []string{operatorAdminGroup}); err != nil {
			return nil, err
		}
	}
	return user, nil
}

func (r *HomeAssistantReconciler) issueOperatorCredential(
	ctx context.Context,
	ha *hav1.HomeAssistant,
	secret *corev1.Secret,
	c *haclient.Client,
	user *haclient.AuthUser,
	ownerToken string,
	recovery bool,
) error {
	username := "ha_operator_" + string(ha.UID)
	// The password exists only in this call. Persist Issuing before mutating
	// credentials so a crash cannot silently issue another long-lived token.
	secret.Annotations[operatorStageAnnotation] = "Issuing"
	if err := r.Update(ctx, secret); err != nil {
		return err
	}
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return err
	}
	password := hex.EncodeToString(bytes)
	var err error
	if user.Username != "" {
		if !recovery {
			return fmt.Errorf("interrupted local credential attachment requires explicit recovery")
		}
		err = c.ResetLocalPassword(ctx, ownerToken, user.ID, password)
	} else {
		err = c.AttachLocalCredentials(ctx, ownerToken, user.ID, username, password)
	}
	if err != nil {
		return err
	}
	session, err := c.LoginWithCredentials(ctx, username, password)
	if err != nil {
		return err
	}
	issued, err := c.CreateLongLivedToken(
		ctx,
		session.AccessToken,
		&haclient.LongLivedTokenRequest{
			ClientName: "kubernetes-operator-" + string(ha.UID) + "-" + secret.Annotations[operatorRecoveryAnnotation],
			Lifespan:   3650,
		},
	)
	if err != nil {
		return err
	}
	// Retain the issued token before verification so a transient outage or
	// restart can retry verification without issuing another credential.
	secret.Data = map[string][]byte{apiTokenSecretKeyName: []byte(issued.Token)}
	if err := r.Update(ctx, secret); err != nil {
		return err
	}
	principal, err := c.CurrentUser(ctx, issued.Token)
	if err != nil {
		return err
	}
	if principal.ID != user.ID || principal.IsOwner || !principal.IsAdmin {
		return fmt.Errorf("unexpected runtime principal or role")
	}
	if err := c.CheckHealthWithToken(ctx, issued.Token); err != nil {
		return err
	}
	secret.Annotations[operatorStageAnnotation] = "Verified"
	if recovery {
		secret.Annotations[operatorRecoveryResultAnnotation] = "Succeeded"
	}
	return r.Update(ctx, secret)
}

func (r *HomeAssistantReconciler) operatorAccountStatus(
	ctx context.Context,
	ha *hav1.HomeAssistant,
	secret *corev1.Secret,
	reason, message string,
) (ctrl.Result, error) {
	patch := client.MergeFrom(ha.DeepCopy())
	if ha.Status.Bootstrap == nil {
		ha.Status.Bootstrap = &hav1.BootstrapStatus{}
	}
	if ha.Status.Bootstrap.OperatorAccount == nil {
		ha.Status.Bootstrap.OperatorAccount = &hav1.OperatorAccountStatus{}
	}
	bs := ha.Status.Bootstrap
	bs.APITokenReady = reason == "Verified"
	status := bs.OperatorAccount
	if secret != nil {
		status.UserID = secret.Annotations[operatorUserAnnotation]
		status.LastRecoveryRequestID = secret.Annotations[operatorRecoveryAnnotation]
		status.RecoveryResult = secret.Annotations[operatorRecoveryResultAnnotation]
		status.LegacySecretName = secret.Annotations[operatorLegacyAnnotation]
	}
	if reason == "Verified" {
		meta.SetStatusCondition(
			&ha.Status.Conditions,
			metav1.Condition{
				Type:               "BootstrapReady",
				Status:             metav1.ConditionTrue,
				Reason:             reasonBootstrapCompleted,
				Message:            "Onboarding complete; dedicated runtime credential verified",
				ObservedGeneration: ha.Generation,
			},
		)
		status.Phase = "Verified"
		status.Capability = "Administrator"
		status.TokenSecretName = ha.Spec.Bootstrap.OperatorAccount.TokenSecretName
		status.TokenSecretNamespace = ha.Namespace
		bs.Completed, bs.APITokenReady = true, true
		bs.APITokenSecretName = status.TokenSecretName
		if status.LegacySecretName != "" {
			status.MigrationPhase = "Completed"
		}
	} else {
		if status.Phase != "Verified" {
			status.Phase = "Provisioning"
		}
		if status.LegacySecretName != "" && status.Phase != "Verified" {
			status.MigrationPhase = "Provisioning"
		}
	}
	setOperatorCredentialConditions(ha, reason, message)
	if err := r.Status().Patch(ctx, ha, patch); err != nil {
		return ctrl.Result{}, err
	}
	if reason == "Verified" {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: bootstrapRetryInterval}, nil
}
