package migration

import (
	"context"
	"fmt"
	"os"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ArchiveValidationProof is the bounded termination message of a successful
// read-only validation Pod. It binds that execution to the exact descriptor
// UID; recreating a ConfigMap cannot reuse an earlier Job's success as proof.
type ArchiveValidationProof struct {
	Version        string `json:"version"`
	OperationUID   string `json:"operationUID"`
	RequestID      string `json:"requestID"`
	DescriptorName string `json:"descriptorName"`
	DescriptorUID  string `json:"descriptorUID"`
	Digest         string `json:"digest"`
	PVCUID         string `json:"pvcUID"`
}

func ProofForValidatedArchive(d *ArchiveDescriptor, cm *corev1.ConfigMap) ArchiveValidationProof {
	return ArchiveValidationProof{
		Version: "v1", OperationUID: d.OperationUID, RequestID: d.RequestID,
		DescriptorName: cm.Name, DescriptorUID: string(cm.UID), Digest: d.Digest, PVCUID: d.PVCUID,
	}
}

// WriteArchiveValidationProof runs only after the worker has verified the file.
// Kubernetes captures the message in Pod status, outside ConfigMap write RBAC.
func WriteArchiveValidationProof(
	ctx context.Context, kube client.Client, namespace, name, requestID, path string,
) error {
	r, err := ReadWorkerRequest(ctx, kube, namespace, name, "backup", requestID)
	if err != nil {
		return err
	}
	op := &ha.HomeAssistantMigration{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, op); err != nil {
		return fmt.Errorf("operation unavailable")
	}
	d, cm, err := ReadValidatedBackupEvidence(ctx, kube, op, *r)
	if err != nil {
		return err
	}
	b, err := migrationprotocol.Canonical(ProofForValidatedArchive(d, cm))
	if err != nil {
		return err
	}
	if len(b) > 4096 || os.WriteFile(path, b, 0600) != nil {
		return fmt.Errorf("archive validation proof unavailable")
	}
	return nil
}
