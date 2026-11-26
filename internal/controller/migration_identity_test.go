package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMigrationIdentityIsDurableAndDoesNotUseStatus(t *testing.T) {
	object := &metav1.ObjectMeta{Annotations: map[string]string{
		migrationImportedAnnotation:  "true",
		migrationSourceIDAnnotation:  "source-entry-id",
		migrationTargetUIDAnnotation: "target-uid",
	}}
	identity, err := migrationObjectIdentity(object)
	if err != nil {
		t.Fatal(err)
	}
	if identity != (migrationIdentity{SourceID: "source-entry-id", TargetUID: "target-uid"}) {
		t.Fatalf("identity = %#v", identity)
	}
	// ObjectMeta intentionally has no status: reconstruction or loss of status
	// cannot change the durable import provenance.
	identityAfterRestart, err := migrationObjectIdentity(object.DeepCopy())
	if err != nil || identityAfterRestart != identity {
		t.Fatalf("identity after status reconstruction = %#v, %v", identityAfterRestart, err)
	}
}

func TestMigrationIdentityRejectsMissingIdentityAndCollisions(t *testing.T) {
	wanted := migrationIdentity{SourceID: "source-a", TargetUID: "target"}
	for name, existing := range map[string]*metav1.ObjectMeta{
		"ordinary resource": {Name: "deterministic-name"},
		"incomplete import": {Annotations: map[string]string{migrationImportedAnnotation: "true"}},
		"another source": {Annotations: map[string]string{
			migrationImportedAnnotation: "true", migrationSourceIDAnnotation: "source-b", migrationTargetUIDAnnotation: "target",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			if !migrationIdentityConflict(existing, wanted) {
				t.Fatal("name collision was accepted")
			}
		})
	}
	same := &metav1.ObjectMeta{Annotations: map[string]string{
		migrationImportedAnnotation: "true", migrationSourceIDAnnotation: "source-a", migrationTargetUIDAnnotation: "target",
	}}
	if migrationIdentityConflict(same, wanted) {
		t.Fatal("same durable identity was treated as a collision")
	}
}

func TestMigrationIdentityRejectsTargetChangeEvenWithAdoptedMarker(t *testing.T) {
	object := &metav1.ObjectMeta{
		Annotations: map[string]string{
			migrationImportedAnnotation:  "true",
			migrationAdoptionAnnotation:  migrationAdopted,
			migrationSourceIDAnnotation:  "source",
			migrationTargetUIDAnnotation: "old-target",
		},
	}
	if !migrationIdentityConflict(
		object,
		migrationIdentity{SourceID: "source", TargetUID: "new-target"},
	) {
		t.Fatal("adopted marker bypassed target identity check")
	}
	if !migrationWritesBlocked(object) {
		t.Fatal("adopted marker unlocked mutations")
	}
}

func TestMigrationOwnerPreventsMarkerRemovalFromUnlockingWrites(t *testing.T) {
	controller := true
	object := &metav1.ObjectMeta{
		Annotations: map[string]string{migrationAdoptionAnnotation: migrationAdopted},
		OwnerReferences: []metav1.OwnerReference{
			{
				APIVersion: "ha.homeassistant.io/v1alpha1",
				Kind:       "HomeAssistantMigration",
				UID:        "migration",
				Controller: &controller,
			},
		},
	}
	if !migrationWritesBlocked(object) {
		t.Fatal("removing the import marker unlocked an owned migration resource")
	}
}
