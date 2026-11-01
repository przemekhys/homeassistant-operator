package utils

import "time"

// E2E Test Timeouts
//
// These constants define standard timeout values used across all E2E tests.
// They are centralized here to ensure consistency and make it easy to adjust
// timeout values globally if needed (e.g., for slower CI environments).

const (
	// BackupRestoreActivePoll is used while an accepted native operation is
	// observed. It does not authorize another dispatch.
	BackupRestoreActivePoll = 5 * time.Second

	// BackupRestoreDependencyRequeue is used for expected dependencies such as
	// target readiness and drain acknowledgements.
	BackupRestoreDependencyRequeue = 10 * time.Second

	// BackupRestoreObservationInterval is the idle observation period for
	// native backup state and encryption-key discovery.
	BackupRestoreObservationInterval = 60 * time.Second

	// BackupRestoreDiagnosticThreshold makes a long restore visible without
	// releasing a hold or retrying a non-idempotent command.
	BackupRestoreDiagnosticThreshold = 15 * time.Minute

	// DefaultEventuallyTimeout is the default timeout for Eventually assertions
	// when no specific timeout is needed. Used for general resource creation
	// and basic API operations.
	DefaultEventuallyTimeout = 2 * time.Minute

	// DefaultEventuallyPollingInterval is the default polling interval for
	// Eventually assertions. Determines how often conditions are checked.
	DefaultEventuallyPollingInterval = 1 * time.Second

	// HAPodReadyTimeout is the timeout for waiting for Home Assistant pod to become ready.
	// HA first boot can be slow due to:
	// - Package installation
	// - Database initialization
	// - Integration discovery
	// Increased for CI environments with limited resources (GitHub Actions: 4 CPU / 16 GB RAM)
	// Further increased to 20 minutes for E2E tests with slower startup times
	HAPodReadyTimeout = 20 * time.Minute

	// BootstrapTimeout is the timeout for complete bootstrap process including:
	// - HA startup (see HAPodReadyTimeout)
	// - Onboarding workflow (including up to 2 × 10 min confirmation windows for
	//   slow CI starts where /api/onboarding 404s transiently)
	// - API token generation
	// 45 min covers: ~13 min CI startup + 10 min window + reset + 10 min window + buffer
	BootstrapTimeout = 45 * time.Minute

	// HotReloadTimeout is the timeout for hot-reload operations via REST API.
	// Should be fast as it's just an HTTP call to reload config.
	HotReloadTimeout = 2 * time.Minute

	// RestartTimeout is the timeout for detecting pod restarts during rolling updates.
	// Includes time for:
	// - StatefulSet annotation change detection
	// - Pod termination
	// - New pod scheduling and startup
	// Increased significantly for CI environments (GitHub Actions runners are slower)
	RestartTimeout = 5 * time.Minute

	// ResourceTimeout is the timeout for basic Kubernetes resource operations:
	// - ConfigMap creation/update
	// - Secret creation/update
	// - Service creation
	// These should be fast API operations.
	ResourceTimeout = 30 * time.Second

	// StatusUpdateTimeout is the timeout for CRD status field updates.
	// Covers controller reconciliation writing status back to API server.
	StatusUpdateTimeout = 20 * time.Second

	// ReconciliationTimeout is the timeout for a complete reconciliation cycle:
	// - ConfigMap generation/update
	// - StatefulSet update
	// - Status update
	ReconciliationTimeout = 1 * time.Minute

	// ControllerPodReadyTimeout is the timeout for waiting for the operator
	// controller-manager pod to become ready during test setup.
	ControllerPodReadyTimeout = 2 * time.Minute

	// CertIssueTimeout is the timeout for a cert-manager Certificate to become
	// Ready (Secret populated) in TLS E2E tests. Self-signed/CA issuers are fast;
	// ACME issuers can be much slower and should override this locally.
	CertIssueTimeout = 2 * time.Minute
)
