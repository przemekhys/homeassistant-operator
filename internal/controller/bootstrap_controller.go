package controller

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
)

const (
	// Bootstrap constants
	defaultOwnerName          = "Admin"
	defaultLanguage           = "en"
	defaultUsernameKey        = "username"
	defaultPasswordKey        = "password"
	apiTokenSecretKeyName     = "token"
	defaultAPITokenSecretName = "homeassistant-api-token"

	// Reconciliation intervals
	bootstrapRetryInterval    = 30 * time.Second
	bootstrapHealthCheckRetry = 10 * time.Second
	bootstrapAPIReadyRetry    = 5 * time.Second
	// onboardingConfirmDelay is how long we wait after first seeing a 404 from
	// /api/onboarding before concluding that onboarding is genuinely complete.
	// With the API readiness gate (CheckAPIReady), a 404 after API is confirmed
	// ready is highly trustworthy. This is just a safety net.
	onboardingConfirmDelay = 30 * time.Second

	// Login recovery
	maxLoginRecoveryRetries = 3

	// Condition reasons
	reasonBootstrapInProgress         = "BootstrapInProgress"
	reasonBootstrapCompleted          = "BootstrapCompleted"
	reasonBootstrapFailed             = "BootstrapFailed"
	reasonBootstrapNotReady           = "HomeAssistantNotReady"
	reasonBootstrapAlreadyDone        = "BootstrapAlreadyDone"
	reasonBootstrapLoginFailed        = "LoginRecoveryFailed"
	reasonBootstrapMissingCredentials = "MissingCredentials"
)

// reconcileBootstrap handles the automatic onboarding and API token
// creation. This is a complete rewrite from Job-based to native Go
// implementation.
func (r *HomeAssistantReconciler) reconcileBootstrap(
	ctx context.Context,
	ha *hav1alpha1.HomeAssistant,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Check if bootstrap is enabled
	if ha.Spec.Bootstrap == nil || !ha.Spec.Bootstrap.Enabled {
		return ctrl.Result{}, nil
	}

	// Check if bootstrap already completed
	if ha.Status.Bootstrap != nil && ha.Status.Bootstrap.Completed {
		log.V(1).Info("Bootstrap already completed, skipping")
		return ctrl.Result{}, nil
	}

	// Initialize bootstrap status if needed
	if ha.Status.Bootstrap == nil {
		ha.Status.Bootstrap = &hav1alpha1.BootstrapStatus{}
	}

	// Validate bootstrap configuration
	if err := r.validateBootstrapConfig(ha); err != nil {
		return r.updateBootstrapStatus(
			ctx, ha,
			reasonBootstrapMissingCredentials,
			err.Error(), false, false,
		)
	}

	// Get credentials from Secret
	username, password, err := r.getBootstrapCredentials(ctx, ha)
	if err != nil {
		log.Error(err, "Failed to get bootstrap credentials")
		return r.updateBootstrapStatus(
			ctx, ha, reasonBootstrapFailed,
			fmt.Sprintf("Failed to get credentials: %v", err),
			false, false,
		)
	}

	// Build Home Assistant URL
	haURL := r.buildHomeAssistantURL(ha)

	// Create HA client
	haClient := haclient.NewClient(haURL).WithTimeout(30 * time.Second)

	// Health check - ensure HA is responding before attempting bootstrap
	log.Info("Performing health check before bootstrap", "url", haURL)
	if err := haClient.CheckHealth(ctx); err != nil {
		if haclient.IsNotReady(err) {
			log.Info("Home Assistant not ready for bootstrap",
				"error", err.Error())
			return r.updateBootstrapStatus(
				ctx, ha, reasonBootstrapNotReady,
				"Health check failed - not ready yet",
				false, false,
			)
		}
		if haclient.IsBanned(err) {
			log.Error(err, "Operator IP banned by Home Assistant, attempting self-unban")
			return r.handleSelfBan(ctx, ha, err)
		}
		log.Error(err, "Bootstrap health check failed")
		return r.updateBootstrapStatus(
			ctx, ha, reasonBootstrapFailed,
			fmt.Sprintf("Health check failed: %v", err),
			false, false,
		)
	}
	// Full API readiness check: ensure all API routes are registered before
	// checking onboarding. This eliminates the ambiguous 404 from /api/onboarding
	// during the startup window where HTTP is up but routes aren't loaded.
	if err := haClient.CheckAPIReady(ctx); err != nil {
		log.Info("Home Assistant API not fully loaded yet", "error", err.Error())
		return ctrl.Result{RequeueAfter: bootstrapAPIReadyRetry}, nil
	}
	log.Info("API fully ready, proceeding with bootstrap")

	// Get bootstrap configuration
	ownerName := getOrDefault(ha.Spec.Bootstrap.OwnerName, defaultOwnerName)
	language := getOrDefault(ha.Spec.Bootstrap.Language, defaultLanguage)

	// Prepare bootstrap options
	opts := &haclient.BootstrapOptions{
		CreateLongLivedToken: ha.Spec.Bootstrap.CreateAPIToken,
		EnableAnalytics:      ha.Spec.Bootstrap.Analytics,
	}

	// Add location config if provided
	if ha.Spec.Bootstrap.Location != nil {
		opts.CoreConfig = r.buildCoreConfigRequest(ha)
	}

	// Perform bootstrap
	log.Info("Performing Home Assistant bootstrap", "url", haURL)
	token, err := haClient.PerformBootstrap(ctx, username, password, ownerName, language, opts)

	if err != nil {
		return r.handleBootstrapError(ctx, ha, err)
	}

	// Bootstrap completed successfully
	log.Info("Bootstrap completed successfully")

	// Create Secret with API token if requested
	tokenCreated := false
	if ha.Spec.Bootstrap.CreateAPIToken && token != "" {
		if err := r.createAPITokenSecret(ctx, ha, token); err != nil {
			log.Error(err, "Failed to create API token Secret")
			return r.updateBootstrapStatus(
				ctx, ha, reasonBootstrapFailed,
				fmt.Sprintf(
					"Failed to create token Secret: %v", err,
				), false, false,
			)
		}
		tokenCreated = true
	}

	// Mark bootstrap as completed
	return r.updateBootstrapStatus(
		ctx, ha, reasonBootstrapCompleted,
		"Bootstrap completed successfully", true, tokenCreated,
	)
}

// buildCoreConfigRequest builds CoreConfigRequest from HomeAssistant spec
func (r *HomeAssistantReconciler) buildCoreConfigRequest(
	ha *hav1alpha1.HomeAssistant,
) *haclient.CoreConfigRequest {
	if ha.Spec.Bootstrap == nil || ha.Spec.Bootstrap.Location == nil {
		return nil
	}

	loc := ha.Spec.Bootstrap.Location
	req := &haclient.CoreConfigRequest{
		LocationName: loc.Name,
		UnitSystem:   getOrDefault(loc.UnitSystem, "metric"),
	}

	if loc.Latitude != "" {
		if lat, err := strconv.ParseFloat(loc.Latitude, 64); err == nil {
			req.Latitude = lat
		}
	}
	if loc.Longitude != "" {
		if lon, err := strconv.ParseFloat(loc.Longitude, 64); err == nil {
			req.Longitude = lon
		}
	}
	if loc.Elevation != nil {
		req.Elevation = *loc.Elevation
	}
	if loc.Currency != "" {
		req.Currency = loc.Currency
	}

	// Use location timezone if specified, otherwise fall back to spec.timezone
	if loc.TimeZone != "" {
		req.TimeZone = loc.TimeZone
	} else if ha.Spec.Timezone != "" {
		req.TimeZone = ha.Spec.Timezone
	}

	return req
}

// validateBootstrapConfig validates bootstrap configuration
func (r *HomeAssistantReconciler) validateBootstrapConfig(
	ha *hav1alpha1.HomeAssistant,
) error {
	if ha.Spec.Bootstrap.Credentials == nil ||
		ha.Spec.Bootstrap.Credentials.SecretRef == nil {
		return fmt.Errorf(
			"bootstrap credentials secretRef required when enabled",
		)
	}
	if ha.Spec.Bootstrap.Credentials.SecretRef.Name == "" {
		return fmt.Errorf(
			"bootstrap credentials secret name cannot be empty",
		)
	}
	return nil
}

// getBootstrapCredentials retrieves username and password from
// the credentials Secret
func (r *HomeAssistantReconciler) getBootstrapCredentials(
	ctx context.Context,
	ha *hav1alpha1.HomeAssistant,
) (string, string, error) {
	secretRef := ha.Spec.Bootstrap.Credentials.SecretRef

	// Get Secret
	credentialsSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      secretRef.Name,
		Namespace: ha.Namespace,
	}, credentialsSecret); err != nil {
		if errors.IsNotFound(err) {
			return "", "", fmt.Errorf("credentials secret %q not found", secretRef.Name)
		}
		return "", "", err
	}

	// Get keys (with defaults)
	usernameKey := getOrDefault(secretRef.UsernameKey, defaultUsernameKey)
	passwordKey := getOrDefault(secretRef.PasswordKey, defaultPasswordKey)

	// Extract username
	usernameBytes, ok := credentialsSecret.Data[usernameKey]
	if !ok {
		return "", "", fmt.Errorf("credentials secret missing key %q", usernameKey)
	}

	// Extract password
	passwordBytes, ok := credentialsSecret.Data[passwordKey]
	if !ok {
		return "", "", fmt.Errorf("credentials secret missing key %q", passwordKey)
	}

	return string(usernameBytes), string(passwordBytes), nil
}

// buildHomeAssistantURL builds the internal service URL for Home Assistant
func (r *HomeAssistantReconciler) buildHomeAssistantURL(ha *hav1alpha1.HomeAssistant) string {
	// Use internal service name
	// Format: http://<name>.<namespace>.svc.cluster.local:<port>
	serviceName := ha.Name
	port := defaultPort
	if ha.Spec.Service != nil && ha.Spec.Service.Port > 0 {
		port = int(ha.Spec.Service.Port)
	}

	return fmt.Sprintf(
		"http://%s.%s.svc.cluster.local:%d",
		serviceName, ha.Namespace, port,
	)
}

// handleBootstrapError handles errors from bootstrap process
func (r *HomeAssistantReconciler) handleBootstrapError(
	ctx context.Context,
	ha *hav1alpha1.HomeAssistant,
	err error,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Check error type
	if haclient.IsNotReady(err) {
		log.Info("Home Assistant not ready yet", "error",
			err.Error())
		return r.updateBootstrapStatus(
			ctx, ha, reasonBootstrapNotReady,
			"Home Assistant not ready yet", false, false,
		)
	}

	if haclient.IsOnboardingDone(err) {
		// Check if we've already exhausted login recovery retries.
		// Before giving up permanently, re-check /api/onboarding: if it now
		// returns 200 the earlier 404s were a startup false-positive and HA is
		// now ready for normal bootstrap. Reset the state so CreateUser can run.
		cond := meta.FindStatusCondition(ha.Status.Conditions, "BootstrapReady")
		if cond != nil && cond.Reason == reasonBootstrapLoginFailed {
			haURL := r.buildHomeAssistantURL(ha)
			haClient := haclient.NewClient(haURL).WithTimeout(30 * time.Second)
			if checkErr := haClient.CheckOnboardingStatus(ctx); checkErr == nil {
				log.Info("Onboarding endpoint available after LoginRecoveryFailed — " +
					"earlier 404s were transient, resetting bootstrap")
				return r.updateBootstrapStatus(
					ctx, ha, reasonBootstrapNotReady,
					"Onboarding endpoint recovered, retrying bootstrap",
					false, false,
					func(s *hav1alpha1.BootstrapStatus) {
						s.OnboardingDoneFirstSeen = nil
						s.LoginRecoveryAttempts = 0
					},
				)
			}
			log.Info("Login recovery previously exhausted, " +
				"waiting for manual intervention")
			return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
		}

		// During HA startup, /api/onboarding may 404 briefly before the
		// onboarding component registers its views. To avoid a false positive,
		// we use OnboardingDoneFirstSeen (a status field) as the reference time
		// instead of condition.LastTransitionTime, which only updates when
		// Status (True/False) changes — not when just the Reason changes.
		bs := ha.Status.Bootstrap
		if bs == nil || bs.OnboardingDoneFirstSeen == nil {
			// First time seeing OnboardingDone — record the timestamp.
			log.Info("Onboarding endpoint returned done, "+
				"setting confirmation timer",
				"error", err.Error())
			now := metav1.Now()
			return r.updateBootstrapStatus(
				ctx, ha, reasonBootstrapAlreadyDone,
				"Onboarding appears done, confirming...",
				false, false,
				func(s *hav1alpha1.BootstrapStatus) {
					s.OnboardingDoneFirstSeen = &now
					// Do NOT reset LoginRecoveryAttempts here — it may already
					// be non-zero from a previous LoginNoUser cycle. Resetting
					// here creates an infinite loop where the counter never
					// advances past 1.
				},
			)
		}

		// Check if enough wall-clock time has passed since we first saw 404.
		// Re-poll every bootstrapRetryInterval so that if onboarding becomes
		// available mid-window the next PerformBootstrap call will catch it.
		elapsed := time.Since(bs.OnboardingDoneFirstSeen.Time)
		if elapsed < onboardingConfirmDelay {
			log.Info("Onboarding 404 confirmation pending, waiting",
				"elapsed", elapsed.Round(time.Second),
				"required", onboardingConfirmDelay)
			return ctrl.Result{
				RequeueAfter: bootstrapRetryInterval,
			}, nil
		}
		log.Info("Onboarding confirmed done after delay, "+
			"attempting token creation",
			"elapsed", elapsed.Round(time.Second))
		return r.handleOnboardingAlreadyDone(ctx, ha)
	}

	// Other errors
	log.Error(err, "Bootstrap failed")
	return r.updateBootstrapStatus(
		ctx, ha, reasonBootstrapFailed,
		fmt.Sprintf("Bootstrap failed: %v", err), false, false,
	)
}

// handleOnboardingAlreadyDone handles the case where onboarding was
// already completed (e.g., by a previous run or because HA completed it
// before the operator could). It logs in with the configured credentials
// and creates a long-lived API token if requested.
func (r *HomeAssistantReconciler) handleOnboardingAlreadyDone(
	ctx context.Context,
	ha *hav1alpha1.HomeAssistant,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Check if API token creation was requested
	if !ha.Spec.Bootstrap.CreateAPIToken {
		log.Info("Onboarding already completed, no API token requested")
		return r.updateBootstrapStatus(
			ctx, ha, reasonBootstrapAlreadyDone,
			"Onboarding already completed", true, false,
			func(s *hav1alpha1.BootstrapStatus) {
				s.OnboardingDoneFirstSeen = nil
				s.LoginRecoveryAttempts = 0
			},
		)
	}

	// Check if Secret already exists
	secretName := r.getAPITokenSecretName(ha)
	existingSecret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: ha.Namespace,
	}, existingSecret)
	if err == nil {
		log.Info("API token Secret already exists",
			"Secret.Name", secretName)
		return r.updateBootstrapStatus(
			ctx, ha, reasonBootstrapAlreadyDone,
			"Onboarding completed, token exists",
			true, true,
			func(s *hav1alpha1.BootstrapStatus) {
				s.OnboardingDoneFirstSeen = nil
				s.LoginRecoveryAttempts = 0
			},
		)
	}
	if !errors.IsNotFound(err) {
		log.Error(err, "Failed to check for existing API Secret")
		return r.updateBootstrapStatus(
			ctx, ha, reasonBootstrapFailed,
			fmt.Sprintf("Failed to check API Secret: %v", err),
			false, false,
		)
	}

	haURL := r.buildHomeAssistantURL(ha)
	haClient := haclient.NewClient(haURL).WithTimeout(30 * time.Second)

	// Re-check /api/onboarding before attempting login recovery.
	// Three cases:
	//   nil          — onboarding is pending (transient 404 resolved) → reset
	//   IsOnboardingDone — onboarding genuinely done → fall through to login
	//   other error  — transient probe failure (timeout/network) → requeue
	checkErr := haClient.CheckOnboardingStatus(ctx)
	if checkErr == nil {
		log.Info("Onboarding endpoint is now available — " +
			"earlier 404 was transient, resetting bootstrap")
		return r.updateBootstrapStatus(
			ctx, ha, reasonBootstrapNotReady,
			"Onboarding endpoint recovered, retrying bootstrap",
			false, false,
			func(s *hav1alpha1.BootstrapStatus) {
				s.OnboardingDoneFirstSeen = nil
				s.LoginRecoveryAttempts = 0
			},
		)
	}
	if !haclient.IsOnboardingDone(checkErr) {
		log.Info("Onboarding re-check returned transient error, requeueing",
			"error", checkErr)
		return r.updateBootstrapStatus(
			ctx, ha, reasonBootstrapNotReady,
			fmt.Sprintf("Onboarding re-check failed: %v", checkErr),
			false, false,
		)
	}

	// Secret doesn't exist — login with credentials and create token.
	username, password, err := r.getBootstrapCredentials(ctx, ha)
	if err != nil {
		log.Error(err, "Failed to get credentials for token recovery")
		return r.updateBootstrapStatus(ctx, ha, reasonBootstrapFailed,
			fmt.Sprintf("Failed to get credentials: %v", err),
			false, false)
	}

	log.Info("Onboarding already completed, logging in to create API token")
	tokenResp, err := haClient.LoginWithCredentials(ctx, username, password)
	if err != nil {
		attempts := 1
		if ha.Status.Bootstrap != nil {
			attempts = ha.Status.Bootstrap.LoginRecoveryAttempts + 1
		}
		log.Error(err, "Failed to login with credentials",
			"attempt", attempts, "maxAttempts", maxLoginRecoveryRetries)

		// After exhausting retries, stop looping and require manual
		// intervention (credential fix or HA reset).
		if attempts >= maxLoginRecoveryRetries {
			log.Error(err, "Login recovery exhausted — manual intervention required")
			return r.updateBootstrapStatus(ctx, ha, reasonBootstrapLoginFailed,
				fmt.Sprintf(
					"Login recovery failed after %d attempts: %v — "+
						"check credentials or reset HA onboarding",
					attempts, err,
				),
				false, false,
				func(s *hav1alpha1.BootstrapStatus) {
					s.LoginRecoveryAttempts = attempts
				},
			)
		}

		// type=form means HA returned "no such user" — onboarding routes were
		// not registered yet when we first saw the 404 (transient CI startup).
		// Reset OnboardingDoneFirstSeen so the confirmation window restarts and
		// PerformBootstrap is retried once onboarding becomes available.
		// LoginRecoveryAttempts is NOT incremented here: LoginNoUser is a
		// startup race condition, not a credential failure. Counting it would
		// exhaust the retry limit before onboarding routes finish loading on
		// slow CI runners (causing an infinite attempt=1 loop when combined
		// with the OnboardingDone first-seen handler).
		if haclient.IsLoginNoUser(err) {
			log.Info("Login returned type=form — no user exists, " +
				"resetting onboarding window to await registration")
			return r.updateBootstrapStatus(ctx, ha, reasonBootstrapNotReady,
				"No user in HA yet, resetting onboarding window",
				false, false,
				func(s *hav1alpha1.BootstrapStatus) {
					s.OnboardingDoneFirstSeen = nil
					// Keep LoginRecoveryAttempts unchanged — this is not a
					// credential failure, only a transient startup condition.
				},
			)
		}

		return r.updateBootstrapStatus(ctx, ha, reasonBootstrapAlreadyDone,
			fmt.Sprintf("Login failed (attempt %d/%d): %v",
				attempts, maxLoginRecoveryRetries, err),
			false, false,
			func(s *hav1alpha1.BootstrapStatus) {
				s.LoginRecoveryAttempts = attempts
			},
		)
	}

	// Create long-lived token
	longLivedResp, err := haClient.CreateLongLivedToken(
		ctx, tokenResp.AccessToken,
		&haclient.LongLivedTokenRequest{
			ClientName: "kubernetes-operator",
			Lifespan:   3650,
		},
	)
	if err != nil {
		log.Error(err, "Failed to create long-lived token after login")
		return r.updateBootstrapStatus(ctx, ha, reasonBootstrapFailed,
			fmt.Sprintf("Token creation failed: %v", err),
			false, false)
	}

	// Store token in Secret
	if err := r.createAPITokenSecret(ctx, ha, longLivedResp.Token); err != nil {
		log.Error(err, "Failed to create API token Secret")
		return r.updateBootstrapStatus(ctx, ha, reasonBootstrapFailed,
			fmt.Sprintf("Failed to create token Secret: %v", err),
			false, false)
	}

	log.Info("Successfully recovered: logged in and created API token")
	return r.updateBootstrapStatus(
		ctx, ha, reasonBootstrapCompleted,
		"Bootstrap completed (token created via login recovery)",
		true, true,
		func(s *hav1alpha1.BootstrapStatus) {
			s.OnboardingDoneFirstSeen = nil
			s.LoginRecoveryAttempts = 0
		},
	)
}

// bootstrapStatusModifier is an optional function applied to BootstrapStatus
// before saving, allowing callers to update fields beyond the standard ones.
type bootstrapStatusModifier func(*hav1alpha1.BootstrapStatus)

// updateBootstrapStatus updates the bootstrap status and returns appropriate
// Result. tokenCreated indicates whether an API token was actually created
// (vs onboarding already done). Optional modifiers can update additional
// status fields (e.g. OnboardingDoneFirstSeen, LoginRecoveryAttempts).
func (r *HomeAssistantReconciler) updateBootstrapStatus(
	ctx context.Context,
	ha *hav1alpha1.HomeAssistant,
	reason, message string,
	completed, tokenCreated bool,
	mods ...bootstrapStatusModifier,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Retry with exponential backoff to handle optimistic locking conflicts
	// This prevents race conditions when multiple controllers try to update status simultaneously
	const maxRetries = 3
	backoff := time.Millisecond * 100

	for attempt := 1; attempt <= maxRetries; attempt++ {
		// Refresh HomeAssistant from API server to get latest resourceVersion
		freshHA := &hav1alpha1.HomeAssistant{}
		if err := r.Get(ctx, types.NamespacedName{Name: ha.Name, Namespace: ha.Namespace}, freshHA); err != nil {
			return ctrl.Result{}, err
		}

		// Initialize bootstrap status if needed
		if freshHA.Status.Bootstrap == nil {
			freshHA.Status.Bootstrap = &hav1alpha1.BootstrapStatus{}
		}

		// Apply desired status updates to fresh object
		now := metav1.Now()
		freshHA.Status.Bootstrap.LastAttempt = &now
		freshHA.Status.Bootstrap.Message = message
		freshHA.Status.Bootstrap.Completed = completed

		if completed && tokenCreated {
			freshHA.Status.Bootstrap.APITokenReady = true
			freshHA.Status.Bootstrap.APITokenSecretName =
				r.getAPITokenSecretName(freshHA)
		}

		// Apply optional field modifiers (e.g. OnboardingDoneFirstSeen, LoginRecoveryAttempts)
		for _, mod := range mods {
			mod(freshHA.Status.Bootstrap)
		}

		// Update main status condition
		conditionStatus := metav1.ConditionFalse
		if completed {
			conditionStatus = metav1.ConditionTrue
		}
		meta.SetStatusCondition(&freshHA.Status.Conditions, metav1.Condition{
			Type:               "BootstrapReady",
			Status:             conditionStatus,
			ObservedGeneration: freshHA.Generation,
			Reason:             reason,
			Message:            message,
		})

		// Attempt status update
		if err := r.Status().Update(ctx, freshHA); err != nil {
			if errors.IsConflict(err) && attempt < maxRetries {
				// Optimistic locking conflict - retry with backoff
				log.Info("Bootstrap status update conflict, retrying",
					"attempt", attempt,
					"backoff", backoff)
				time.Sleep(backoff)
				backoff *= 2 // Exponential backoff
				continue
			}
			// Non-conflict error or max retries exceeded
			log.Error(err, "Failed to update bootstrap status")
			return ctrl.Result{}, err
		}

		// Success
		log.Info("Bootstrap status updated successfully", "attempt", attempt)
		break
	}

	// Determine requeue behavior
	if completed {
		return ctrl.Result{}, nil
	}

	// Not completed - requeue based on reason
	switch reason {
	case reasonBootstrapNotReady:
		// HA not ready - retry quickly
		return ctrl.Result{RequeueAfter: bootstrapHealthCheckRetry}, nil
	case reasonBootstrapLoginFailed:
		// Login recovery exhausted - long wait, needs manual intervention
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	case reasonBootstrapFailed, reasonBootstrapMissingCredentials:
		// Error - retry with backoff
		return ctrl.Result{RequeueAfter: bootstrapRetryInterval}, nil
	default:
		// Default retry
		return ctrl.Result{RequeueAfter: bootstrapRetryInterval}, nil
	}
}

// createAPITokenSecret creates or updates the Secret containing the
// API token
func (r *HomeAssistantReconciler) createAPITokenSecret(
	ctx context.Context,
	ha *hav1alpha1.HomeAssistant,
	token string,
) error {
	log := logf.FromContext(ctx)

	secretName := r.getAPITokenSecretName(ha)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: ha.Namespace,
			Labels:    r.labelsForHomeAssistant(ha),
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			apiTokenSecretKeyName: []byte(token),
		},
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(ha, secret, r.Scheme); err != nil {
		return err
	}

	// Check if Secret already exists
	existingSecret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ha.Namespace}, existingSecret)

	if err != nil && errors.IsNotFound(err) {
		// Create new Secret
		log.Info("Creating API token Secret", "Secret.Name", secretName)
		return r.Create(ctx, secret)
	} else if err != nil {
		return err
	}

	// Update existing Secret
	log.Info("Updating API token Secret", "Secret.Name", secretName)
	existingSecret.Data = secret.Data
	return r.Update(ctx, existingSecret)
}

// getAPITokenSecretName returns the name of the Secret for the API token
func (r *HomeAssistantReconciler) getAPITokenSecretName(ha *hav1alpha1.HomeAssistant) string {
	if ha.Spec.Bootstrap != nil && ha.Spec.Bootstrap.APITokenSecretName != "" {
		return ha.Spec.Bootstrap.APITokenSecretName
	}
	return ha.Name + "-" + defaultAPITokenSecretName
}

// getOrDefault returns value if non-empty, otherwise returns defaultValue
func getOrDefault(value, defaultValue string) string {
	if value != "" {
		return value
	}
	return defaultValue
}

// handleSelfBan is called when HA returns 403/429 indicating the operator's IP
// is banned. It removes the IP from ip_bans.yaml via exec, deletes the HA pod
// (forcing HA to restart and clear in-memory bans), and requeues after
// selfUnbanRequeueWait to give HA time to come back up.
//
// Limits: at most selfUnbanMaxCount unbans total; at most one per selfUnbanCooldown.
// Once limits are exceeded the operator stops retrying and requires manual intervention.
func (r *HomeAssistantReconciler) handleSelfBan(
	ctx context.Context,
	ha *hav1alpha1.HomeAssistant,
	banErr error,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Enforce total unban limit
	if ha.Status.SelfUnbanCount >= selfUnbanMaxCount {
		log.Error(banErr, "Self-unban limit reached — manual intervention required",
			"count", ha.Status.SelfUnbanCount, "max", selfUnbanMaxCount)
		r.Recorder.Eventf(ha, nil, corev1.EventTypeWarning, "SelfUnbanLimitReached",
			"SelfUnbanLimitReached",
			"Operator IP banned and self-unban limit (%d) reached; "+
				"manually remove the operator IP from /config/ip_bans.yaml and restart HA",
			selfUnbanMaxCount)
		return ctrl.Result{RequeueAfter: selfUnbanRequeueWait}, nil
	}

	// Enforce cooldown
	if ha.Status.LastSelfUnban != nil {
		elapsed := time.Since(ha.Status.LastSelfUnban.Time)
		if elapsed < selfUnbanCooldown {
			log.Info("Self-unban cooldown active, waiting",
				"elapsed", elapsed.Round(time.Second),
				"cooldown", selfUnbanCooldown)
			return ctrl.Result{RequeueAfter: selfUnbanCooldown - elapsed}, nil
		}
	}

	podIP := os.Getenv("POD_IP")
	if podIP == "" {
		log.Error(nil, "POD_IP env var not set — cannot determine IP to unban; "+
			"add downward API env POD_IP=status.podIP to the operator Deployment")
		// Emit the misconfiguration event only once (SelfUnbanCount == 0) to avoid
		// flooding the event stream on every reconcile while the operator is banned.
		if ha.Status.SelfUnbanCount == 0 {
			r.Recorder.Eventf(ha, nil, corev1.EventTypeWarning, "SelfUnbanFailed",
				"SelfUnbanFailed",
				"POD_IP env var not set; add downward API to operator Deployment")
		}
		return ctrl.Result{RequeueAfter: selfUnbanCooldown}, nil
	}

	log.Info("Removing operator IP from HA ip_bans.yaml via exec",
		"podIP", podIP, "haPod", ha.Name+"-0")

	if r.RestConfig == nil {
		log.Error(nil, "RestConfig not set on reconciler — cannot exec into HA pod")
		if ha.Status.SelfUnbanCount == 0 {
			r.Recorder.Eventf(ha, nil, corev1.EventTypeWarning, "SelfUnbanFailed",
				"SelfUnbanFailed", "RestConfig not configured on reconciler")
		}
		return ctrl.Result{RequeueAfter: selfUnbanCooldown}, nil
	}

	if err := unbanSelfFromHA(ctx, r.RestConfig, ha.Namespace, ha.Name, podIP); err != nil {
		log.Error(err, "Failed to remove IP from ip_bans.yaml")
		r.Recorder.Eventf(ha, nil, corev1.EventTypeWarning, "SelfUnbanFailed",
			"SelfUnbanFailed",
			"Failed to remove %s from ip_bans.yaml: %v", podIP, err)
		return ctrl.Result{RequeueAfter: selfUnbanCooldown}, nil
	}

	// Delete the HA pod so StatefulSet recreates it — clears in-memory bans.
	haPod := &corev1.Pod{}
	podKey := types.NamespacedName{Name: ha.Name + "-0", Namespace: ha.Namespace}
	if err := r.Get(ctx, podKey, haPod); err != nil {
		if !errors.IsNotFound(err) {
			log.Error(err, "Failed to get HA pod for deletion after ip_bans removal")
			return ctrl.Result{RequeueAfter: selfUnbanCooldown}, nil
		}
		// Pod already gone — StatefulSet will recreate it; proceed to update status.
	} else {
		if err := r.Delete(ctx, haPod); err != nil {
			log.Error(err, "Failed to delete HA pod after ip_bans removal")
			return ctrl.Result{RequeueAfter: selfUnbanCooldown}, nil
		}
		log.Info("Deleted HA pod to clear in-memory bans", "pod", ha.Name+"-0")
	}

	// Update status only after the pod delete succeeded (or pod was already gone).
	now := metav1.Now()
	patch := client.MergeFrom(ha.DeepCopy())
	ha.Status.SelfUnbanCount++
	ha.Status.LastSelfUnban = &now
	if err := r.Status().Patch(ctx, ha, patch); err != nil {
		log.Error(err, "Failed to update SelfUnban status")
		return ctrl.Result{RequeueAfter: selfUnbanCooldown}, nil
	}

	r.Recorder.Eventf(ha, nil, corev1.EventTypeNormal, "SelfUnbanned",
		"SelfUnbanned",
		"Removed operator IP %s from ip_bans.yaml and restarted HA pod (unban %d/%d)",
		podIP, ha.Status.SelfUnbanCount, selfUnbanMaxCount)

	log.Info("Self-unban complete, requeuing after restart wait",
		"podIP", podIP,
		"unbanCount", ha.Status.SelfUnbanCount,
		"requeueAfter", selfUnbanRequeueWait)

	return ctrl.Result{RequeueAfter: selfUnbanRequeueWait}, nil
}
