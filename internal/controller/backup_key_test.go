package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/test/utils"
)

const (
	backupKeyFixtureNamespace   = "backup-key-fixtures"
	backupKeyFixturePasswordKey = "password"
	backupKeyInstanceUIDLabel   = "ha.homeassistant.io/instance-uid"
	backupKeyPurposeLabel       = "ha.homeassistant.io/purpose"
)

// backupKeyHistoryFixture models two observed key values for one deleted or
// replaced instance. The names are deterministic only for tests; production
// history Secret names must be randomly generated and persisted before create.
type backupKeyHistoryFixture struct {
	Target   *hav1.HomeAssistant
	History  []*corev1.Secret
	Canaries []string
}

func newBackupKeyHistoryFixture(t *testing.T) backupKeyHistoryFixture {
	t.Helper()

	const (
		instanceName = "backup-key-fixture"
		instanceUID  = "backup-key-fixture-uid"
	)
	first := utils.BackupRestoreTestKey + "-first"
	second := utils.BackupRestoreTestKey + "-second"
	return backupKeyHistoryFixture{
		Target: &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{
			Name:      instanceName,
			Namespace: backupKeyFixtureNamespace,
			UID:       types.UID(instanceUID),
		}},
		History: []*corev1.Secret{
			newImmutableBackupKeySecret("backup-key-history-first", instanceUID, first),
			newImmutableBackupKeySecret("backup-key-history-second", instanceUID, second),
		},
		Canaries: []string{first, second},
	}
}

func newImmutableBackupKeySecret(name, instanceUID, password string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: backupKeyFixtureNamespace,
			Labels: map[string]string{
				backupKeyInstanceUIDLabel: instanceUID,
				backupKeyPurposeLabel:     backupKeyHistoryPurpose,
			},
		},
		Immutable: ptr.To(true),
		Type:      corev1.SecretTypeOpaque,
		Data:      map[string][]byte{backupKeyFixturePasswordKey: []byte(password)},
	}
}

func assertRetainedBackupKeySecret(t *testing.T, secret *corev1.Secret, instanceUID string) {
	t.Helper()
	if secret.Immutable == nil || !*secret.Immutable {
		t.Fatalf("Secret %q must be immutable", secret.Name)
	}
	if len(secret.OwnerReferences) != 0 {
		t.Fatalf("Secret %q must not have owner references: %#v", secret.Name, secret.OwnerReferences)
	}
	if secret.Type != corev1.SecretTypeOpaque {
		t.Fatalf("Secret %q type = %q, want %q", secret.Name, secret.Type, corev1.SecretTypeOpaque)
	}
	if secret.Labels[backupKeyInstanceUIDLabel] != instanceUID {
		t.Fatalf(
			"Secret %q instance UID label = %q, want %q",
			secret.Name,
			secret.Labels[backupKeyInstanceUIDLabel],
			instanceUID,
		)
	}
	if secret.Labels[backupKeyPurposeLabel] != backupKeyHistoryPurpose {
		t.Fatalf(
			"Secret %q purpose label = %q, want %q",
			secret.Name,
			secret.Labels[backupKeyPurposeLabel],
			backupKeyHistoryPurpose,
		)
	}
}

func assertBackupKeyCanariesRedacted(t *testing.T, output string, canaries ...string) {
	t.Helper()
	for _, canary := range canaries {
		if strings.Contains(output, canary) {
			t.Fatalf("sensitive backup key canary leaked: %q", canary)
		}
	}
}

func TestBackupKeyHistoryFixturesAreRetainedAndRedactable(t *testing.T) {
	fixture := newBackupKeyHistoryFixture(t)
	for _, secret := range fixture.History {
		assertRetainedBackupKeySecret(t, secret, string(fixture.Target.UID))
	}
	assertBackupKeyCanariesRedacted(t, "backup key observation failed", fixture.Canaries...)
}

func TestBackupKeyHistoryRetainsImmutableKeyUsingPreselectedName(t *testing.T) {
	ctx := context.Background()
	fixture := newBackupKeyHistoryFixture(t)
	store := BackupKeyHistory{Client: backupKeyClient(t)}
	plan := BackupKeySecretPlan{Name: "backup-key-preselected"}

	created, err := store.Retain(ctx, fixture.Target, fixture.Canaries[0], plan)
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != plan.Name {
		t.Fatalf("Secret name = %q, want durable preselected name %q", created.Name, plan.Name)
	}
	assertRetainedBackupKeySecret(t, created, string(fixture.Target.UID))

	again, err := store.Retain(
		ctx,
		fixture.Target,
		fixture.Canaries[0],
		BackupKeySecretPlan{Name: "must-not-be-created"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if again.Name != created.Name {
		t.Fatalf("same value returned %q, want existing %q", again.Name, created.Name)
	}
	secrets := &corev1.SecretList{}
	if err := store.Client.List(ctx, secrets, client.InNamespace(fixture.Target.Namespace)); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 1 {
		t.Fatalf("retained Secret count = %d, want 1", len(secrets.Items))
	}
}

func TestNewBackupKeySecretPlanSelectsDistinctNames(t *testing.T) {
	first := NewBackupKeySecretPlan()
	second := NewBackupKeySecretPlan()
	if !strings.HasPrefix(first.Name, "backup-key-") || !strings.HasPrefix(second.Name, "backup-key-") {
		t.Fatalf("unexpected backup-key plans: %#v, %#v", first, second)
	}
	if first.Name == second.Name {
		t.Fatalf("backup-key plans reused name %q", first.Name)
	}
}

func TestBackupKeyHistoryUsesExistingValueForBackRotation(t *testing.T) {
	ctx := context.Background()
	fixture := newBackupKeyHistoryFixture(t)
	store := BackupKeyHistory{Client: backupKeyClient(t, fixture.History[0], fixture.History[1])}

	secret, err := store.Retain(
		ctx,
		fixture.Target,
		fixture.Canaries[0],
		BackupKeySecretPlan{Name: "new-name-must-not-be-used"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if secret.Name != fixture.History[0].Name {
		t.Fatalf("back-rotated value used %q, want existing %q", secret.Name, fixture.History[0].Name)
	}
}

func TestBackupKeyHistoryRetainsRotationAndBackRotationIdempotently(t *testing.T) {
	ctx := context.Background()
	fixture := newBackupKeyHistoryFixture(t)
	store := BackupKeyHistory{Client: backupKeyClient(t)}

	first, err := store.Retain(ctx, fixture.Target, fixture.Canaries[0], BackupKeySecretPlan{Name: "backup-key-first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Retain(
		ctx,
		fixture.Target,
		fixture.Canaries[1],
		BackupKeySecretPlan{Name: "backup-key-second"},
	)
	if err != nil {
		t.Fatal(err)
	}
	backRotated, err := store.Retain(
		ctx,
		fixture.Target,
		fixture.Canaries[0],
		BackupKeySecretPlan{Name: "must-not-create-back-rotation"},
	)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := store.Retain(
		ctx,
		fixture.Target,
		fixture.Canaries[1],
		BackupKeySecretPlan{Name: "must-not-create-repeat"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if backRotated.Name != first.Name || repeated.Name != second.Name {
		t.Fatalf(
			"existing rotation history was not reused: back-rotation=%q repeat=%q, want %q and %q",
			backRotated.Name,
			repeated.Name,
			first.Name,
			second.Name,
		)
	}

	secrets := &corev1.SecretList{}
	if err := store.Client.List(ctx, secrets, client.InNamespace(fixture.Target.Namespace)); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 2 {
		t.Fatalf("retained Secret count after repeated observations = %d, want 2", len(secrets.Items))
	}
	for i := range secrets.Items {
		assertRetainedBackupKeySecret(t, &secrets.Items[i], string(fixture.Target.UID))
	}
}

func TestBackupKeyHistoryRejectsInvalidObservations(t *testing.T) {
	ctx := context.Background()
	fixture := newBackupKeyHistoryFixture(t)
	store := BackupKeyHistory{Client: backupKeyClient(t)}

	tests := []struct {
		name     string
		target   *hav1.HomeAssistant
		password string
		plan     BackupKeySecretPlan
		want     error
	}{
		{
			name:     "nil target",
			password: fixture.Canaries[0],
			plan:     BackupKeySecretPlan{Name: "unused"},
			want:     ErrBackupKeyTargetInvalid,
		},
		{
			name:     "target without UID",
			target:   &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Namespace: fixture.Target.Namespace}},
			password: fixture.Canaries[0],
			plan:     BackupKeySecretPlan{Name: "unused"},
			want:     ErrBackupKeyTargetInvalid,
		},
		{
			name:   "empty password",
			target: fixture.Target,
			plan:   BackupKeySecretPlan{Name: "unused"},
			want:   ErrBackupKeyEmpty,
		},
		{
			name:     "missing preselected name",
			target:   fixture.Target,
			password: fixture.Canaries[0],
			want:     ErrBackupKeyPlanMissing,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := store.Retain(ctx, tt.target, tt.password, tt.plan)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Retain error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestBackupKeyHistoryReturnsReadAndRBACFailures(t *testing.T) {
	ctx := context.Background()
	fixture := newBackupKeyHistoryFixture(t)
	resource := schema.GroupResource{Resource: "secrets"}

	t.Run("list forbidden", func(t *testing.T) {
		forbidden := apierrors.NewForbidden(resource, "", errors.New("list denied"))
		store := BackupKeyHistory{Client: listErrorClient{Client: backupKeyClient(t), err: forbidden}}
		_, err := store.Retain(ctx, fixture.Target, fixture.Canaries[0], BackupKeySecretPlan{Name: "unused"})
		if !apierrors.IsForbidden(err) {
			t.Fatalf("Retain error = %v, want forbidden", err)
		}
	})

	t.Run("create forbidden", func(t *testing.T) {
		forbidden := apierrors.NewForbidden(resource, "backup-key-denied", errors.New("create denied"))
		store := BackupKeyHistory{Client: createErrorClient{Client: backupKeyClient(t), err: forbidden}}
		_, err := store.Retain(ctx, fixture.Target, fixture.Canaries[0], BackupKeySecretPlan{Name: "backup-key-denied"})
		if !apierrors.IsForbidden(err) {
			t.Fatalf("Retain error = %v, want forbidden", err)
		}
		assertBackupKeyCanariesRedacted(t, err.Error(), fixture.Canaries[0])
	})

	t.Run("read preselected name forbidden after uncertain create", func(t *testing.T) {
		forbidden := apierrors.NewForbidden(resource, "backup-key-read-denied", errors.New("get denied"))
		store := BackupKeyHistory{Client: createAndGetErrorClient{
			Client:    backupKeyClient(t),
			createErr: errors.New("lost create response"),
			getErr:    forbidden,
		}}
		_, err := store.Retain(
			ctx,
			fixture.Target,
			fixture.Canaries[0],
			BackupKeySecretPlan{Name: "backup-key-read-denied"},
		)
		if !apierrors.IsForbidden(err) {
			t.Fatalf("Retain error = %v, want forbidden", err)
		}
	})
}

func TestBackupKeyHistoryReconcilesUncertainCreateAtPreselectedName(t *testing.T) {
	ctx := context.Background()
	fixture := newBackupKeyHistoryFixture(t)
	base := backupKeyClient(t)
	store := BackupKeyHistory{Client: createThenErrorClient{Client: base, err: errors.New("lost create response")}}
	plan := BackupKeySecretPlan{Name: "backup-key-uncertain-create"}

	secret, err := store.Retain(ctx, fixture.Target, fixture.Canaries[0], plan)
	if err != nil {
		t.Fatal(err)
	}
	if secret.Name != plan.Name {
		t.Fatalf("reconciled Secret name = %q, want %q", secret.Name, plan.Name)
	}
}

func TestBackupKeyHistoryKeepsDeletedInstanceHistoryWithoutOwnerReferences(t *testing.T) {
	ctx := context.Background()
	fixture := newBackupKeyHistoryFixture(t)
	old := newImmutableBackupKeySecret("backup-key-deleted-instance", "deleted-instance-uid", fixture.Canaries[0])
	store := BackupKeyHistory{Client: backupKeyClient(t, old)}
	recreated := fixture.Target.DeepCopy()
	recreated.UID = types.UID("recreated-instance-uid")

	created, err := store.Retain(
		ctx,
		recreated,
		fixture.Canaries[0],
		BackupKeySecretPlan{Name: "backup-key-recreated-instance"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if created.Name == old.Name {
		t.Fatal("recreated Home Assistant reused deleted instance history")
	}
	assertRetainedBackupKeySecret(t, created, string(recreated.UID))
	assertRetainedBackupKeySecret(t, old, "deleted-instance-uid")

	secrets := &corev1.SecretList{}
	if err := store.Client.List(ctx, secrets, client.InNamespace(fixture.Target.Namespace)); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 2 {
		t.Fatalf("retained Secret count after Home Assistant recreation = %d, want 2", len(secrets.Items))
	}
}

func TestBackupKeyCleanupRequiresExplicitRequestAndMatchingUID(t *testing.T) {
	secret := newImmutableBackupKeySecret("backup-key", "instance-uid", "value")
	if BackupKeySecretCleanupEligible(secret, types.UID("instance-uid"), false) {
		t.Fatal("retained key was eligible for automatic cleanup")
	}
	if BackupKeySecretCleanupEligible(secret, types.UID("other-instance"), true) {
		t.Fatal("retained key was eligible for cleanup by a replacement instance")
	}
	if !BackupKeySecretCleanupEligible(secret, types.UID("instance-uid"), true) {
		t.Fatal("retained key was not eligible for explicitly authorised cleanup")
	}
}

func backupKeyClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

type createThenErrorClient struct {
	client.Client
	err error
}

func (c createThenErrorClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err := c.Client.Create(ctx, obj, opts...); err != nil {
		return err
	}
	return c.err
}

type listErrorClient struct {
	client.Client
	err error
}

func (c listErrorClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return c.err
}

type createErrorClient struct {
	client.Client
	err error
}

func (c createErrorClient) Create(context.Context, client.Object, ...client.CreateOption) error {
	return c.err
}

type createAndGetErrorClient struct {
	client.Client
	createErr error
	getErr    error
}

func (c createAndGetErrorClient) Create(context.Context, client.Object, ...client.CreateOption) error {
	return c.createErr
}

func (c createAndGetErrorClient) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return c.getErr
}
