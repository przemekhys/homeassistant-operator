package controller

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	migrationImportedAnnotation  = "ha.homeassistant.io/migration-imported"
	migrationPausedAnnotation    = "ha.homeassistant.io/migration-paused"
	migrationAdoptionAnnotation  = "ha.homeassistant.io/migration-adoption"
	migrationSourceIDAnnotation  = "ha.homeassistant.io/migration-source-id"
	migrationTargetUIDAnnotation = "ha.homeassistant.io/migration-target-uid"
	migrationAdopted             = "adopted"
)

// migrationIdentity is immutable import provenance kept in metadata, rather
// than status, so it survives controller restarts and status reconstruction.
type migrationIdentity struct {
	SourceID  string
	TargetUID string
}

func migrationObjectIdentity(object metav1.Object) (migrationIdentity, error) {
	annotations := object.GetAnnotations()
	if annotations[migrationImportedAnnotation] != "true" {
		return migrationIdentity{}, fmt.Errorf("object is not imported by a migration")
	}
	identity := migrationIdentity{
		SourceID:  strings.TrimSpace(annotations[migrationSourceIDAnnotation]),
		TargetUID: strings.TrimSpace(annotations[migrationTargetUIDAnnotation]),
	}
	if identity.SourceID == "" || identity.TargetUID == "" {
		return migrationIdentity{}, fmt.Errorf(
			"imported object has incomplete migration identity",
		)
	}
	return identity, nil
}

// migrationIdentityConflict prevents a create-only import from being confused
// with a resource bearing a deterministic colliding name from another source.
func migrationIdentityConflict(existing metav1.Object, wanted migrationIdentity) bool {
	actual, err := migrationObjectIdentity(existing)
	return err != nil || actual != wanted
}

// migrationWritesBlocked keeps every imported object fail-closed, including
// after read-only adoption. An editable annotation is not authorization for
// external mutations. Ordinary CRs retain their existing behaviour.
func migrationWritesBlocked(object metav1.Object) bool {
	annotations := object.GetAnnotations()
	if annotations[migrationImportedAnnotation] == "true" {
		return true
	}
	// An owned import must remain protected even if its editable annotation is
	// accidentally removed. Ownership itself never authorizes a write.
	for _, owner := range object.GetOwnerReferences() {
		group, version, valid := strings.Cut(owner.APIVersion, "/")
		if valid && group == "ha.homeassistant.io" && version != "" && owner.Kind == "HomeAssistantMigration" &&
			owner.Controller != nil &&
			*owner.Controller {
			return true
		}
	}
	return false
}

// removeBlockedMigrationFinalizer removes only the controller's own finalizer.
// It applies to all imported objects so deletion never triggers a write to
// Home Assistant while still allowing Kubernetes deletion to complete.
func removeBlockedMigrationFinalizer(
	ctx context.Context,
	c client.Client,
	object client.Object,
	finalizer string,
) (bool, error) {
	if !migrationWritesBlocked(object) || object.GetDeletionTimestamp().IsZero() ||
		!controllerutil.ContainsFinalizer(object, finalizer) {
		return false, nil
	}
	controllerutil.RemoveFinalizer(object, finalizer)
	return true, c.Update(ctx, object)
}
