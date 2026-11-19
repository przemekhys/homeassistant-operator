package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/migration"
	"github.com/przemekhys/homeassistant-operator/internal/migrationprotocol"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// createProxyCredentialBundle is an unconnected create-only primitive. A
// returned UID must be durably protected before any material can be used.
// An immutable one-shot claim precedes key generation, so an uncertain bundle
// create cannot authorize another generation. The claim itself must be
// protected against deletion before this primitive can be used in production.
// The bundle contains both private keys and must never be mounted; immutable,
// least-privilege server and worker projections are created separately.
func createProxyCredentialBundle(ctx context.Context, writer client.Client, reader client.Reader, scheme *runtime.Scheme, op *ha.HomeAssistantMigration, request ha.MigrationExecutionRequest, now time.Time) (*ha.MigrationProxyCredentialBinding, error) {
	if ctx == nil || writer == nil || reader == nil || scheme == nil || op == nil || op.UID == "" || op.Namespace == "" || op.ResourceVersion == "" || request.Stage != "upload" {
		return nil, fmt.Errorf("proxy credential prerequisites unavailable")
	}
	currentOp := &ha.HomeAssistantMigration{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(op), currentOp); err != nil || currentOp.UID != op.UID || currentOp.ResourceVersion != op.ResourceVersion || currentOp.Spec.ProxyCredentialBinding != nil || currentOp.Spec.CancelRequested || !currentOp.DeletionTimestamp.IsZero() || migrationprotocol.AbortRequested(currentOp) || migrationprotocol.ValidateAccepted(currentOp) != nil {
		return nil, fmt.Errorf("proxy credential prerequisites unavailable")
	}
	accepted := false
	for _, current := range currentOp.Spec.ExecutionRequests {
		if reflect.DeepEqual(current, request) {
			accepted = true
		}
	}
	if !accepted {
		return nil, fmt.Errorf("proxy credential request unavailable")
	}
	name := "migration-proxy-" + migrationTargetSelector(string(op.UID))
	key := client.ObjectKey{Namespace: op.Namespace, Name: name}
	existing := &corev1.Secret{}
	if err := reader.Get(ctx, key, existing); err == nil {
		return nil, fmt.Errorf("proxy credential name unavailable")
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("proxy credential lookup unavailable")
	}
	immutable := true
	claim := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name + "-claim", Namespace: op.Namespace}, Immutable: &immutable, Data: map[string]string{"operationUID": string(op.UID), "requestID": request.RequestID}}
	if err := controllerutil.SetControllerReference(op, claim, scheme); err != nil {
		return nil, fmt.Errorf("proxy credential claim owner unavailable")
	}
	if err := writer.Create(ctx, claim); err != nil || claim.UID == "" {
		return nil, fmt.Errorf("proxy credential claim outcome unknown")
	}
	credentials, err := migration.GenerateProxyCredentials(now, 24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("proxy credential generation unavailable")
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: op.Namespace}, Immutable: &immutable, Data: map[string][]byte{
		"ca.crt": credentials.CA, "server.crt": credentials.ServerCertificate, "server.key": credentials.ServerKey,
		"client.crt": credentials.WorkerCertificate, "client.key": credentials.WorkerKey,
	}}
	if err := controllerutil.SetControllerReference(op, secret, scheme); err != nil {
		return nil, fmt.Errorf("proxy credential owner unavailable")
	}
	if err := writer.Create(ctx, secret); err != nil {
		return nil, fmt.Errorf("proxy credential create outcome unknown")
	}
	if secret.UID == "" || secret.ResourceVersion == "" {
		return nil, fmt.Errorf("proxy credential create outcome unknown")
	}
	for name, data := range map[string]map[string][]byte{
		proxyServerCredentialName(name): {"server.crt": credentials.ServerCertificate, "server.key": credentials.ServerKey, "client-ca.crt": credentials.CA, "client.crt": credentials.WorkerCertificate},
		proxyWorkerCredentialName(name): {"ca.crt": credentials.CA, "server.crt": credentials.ServerCertificate, "client.crt": credentials.WorkerCertificate, "client.key": credentials.WorkerKey},
	} {
		projection := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: op.Namespace}, Immutable: &immutable, Data: data}
		if controllerutil.SetControllerReference(op, projection, scheme) != nil || writer.Create(ctx, projection) != nil || projection.UID == "" {
			return nil, fmt.Errorf("proxy credential projection outcome unknown")
		}
	}
	return &ha.MigrationProxyCredentialBinding{Version: "v1", RequestID: request.RequestID, Bundle: ha.MigrationObjectReference{Name: secret.Name, UID: string(secret.UID)}, BundleResourceVersion: secret.ResourceVersion, Claim: ha.MigrationObjectReference{Name: claim.Name, UID: string(claim.UID)}}, nil
}

func proxyServerCredentialName(bundle string) string { return bundle + "-server" }
func proxyWorkerCredentialName(bundle string) string { return bundle + "-worker" }

// bindProxyCredentialBundle records only a confirmed, UID-bound result. It
// does not authorize the proxy, mount secrets, or prove isolation.
func bindProxyCredentialBundle(ctx context.Context, writer client.Client, reader client.Reader, op *ha.HomeAssistantMigration, binding ha.MigrationProxyCredentialBinding, now time.Time) error {
	if ctx == nil || writer == nil || reader == nil || op == nil || op.UID == "" || now.IsZero() {
		return fmt.Errorf("proxy credential binding unavailable")
	}
	current := &ha.HomeAssistantMigration{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(op), current); err != nil || current.UID != op.UID || current.Spec.CancelRequested || !current.DeletionTimestamp.IsZero() || migrationprotocol.AbortRequested(current) {
		return fmt.Errorf("proxy credential binding unavailable")
	}
	if current.Spec.ProxyCredentialBinding != nil && !reflect.DeepEqual(current.Spec.ProxyCredentialBinding, &binding) {
		return fmt.Errorf("proxy credential binding conflict")
	}
	updated := current.DeepCopy()
	updated.Spec.ProxyCredentialBinding = &binding
	if migrationprotocol.ValidateAccepted(updated) != nil || binding.Bundle.Name != "migration-proxy-"+migrationTargetSelector(string(op.UID)) || binding.Claim.Name != binding.Bundle.Name+"-claim" {
		return fmt.Errorf("proxy credential binding invalid")
	}
	claim := &corev1.ConfigMap{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: binding.Claim.Name}, claim); err != nil || string(claim.UID) != binding.Claim.UID || claim.Immutable == nil || !*claim.Immutable || len(claim.Data) != 2 || claim.Data["operationUID"] != string(op.UID) || claim.Data["requestID"] != binding.RequestID {
		return fmt.Errorf("proxy credential claim unavailable")
	}
	owner := metav1.GetControllerOf(claim)
	if owner == nil || owner.UID != op.UID {
		return fmt.Errorf("proxy credential claim unavailable")
	}
	bundle := &corev1.Secret{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: op.Namespace, Name: binding.Bundle.Name}, bundle); err != nil || string(bundle.UID) != binding.Bundle.UID || bundle.ResourceVersion != binding.BundleResourceVersion || bundle.Immutable == nil || !*bundle.Immutable || len(bundle.Data) != 5 {
		return fmt.Errorf("proxy credential bundle unavailable")
	}
	owner = metav1.GetControllerOf(bundle)
	if owner == nil || owner.UID != op.UID {
		return fmt.Errorf("proxy credential bundle unavailable")
	}
	material := &migration.ProxyCredentials{CA: bundle.Data["ca.crt"], ServerCertificate: bundle.Data["server.crt"], ServerKey: bundle.Data["server.key"], WorkerCertificate: bundle.Data["client.crt"], WorkerKey: bundle.Data["client.key"]}
	if migration.ValidateProxyCredentials(material, now) != nil {
		return fmt.Errorf("proxy credential bundle invalid")
	}
	if err := validateProxyCredentialProjections(reader, op, binding, bundle); err != nil {
		return err
	}
	if current.Spec.ProxyCredentialBinding != nil {
		return nil
	}
	if err := writer.Update(ctx, updated); err != nil {
		observed := &ha.HomeAssistantMigration{}
		if reader.Get(ctx, client.ObjectKeyFromObject(op), observed) == nil && observed.UID == op.UID && reflect.DeepEqual(observed.Spec.ProxyCredentialBinding, &binding) && migrationprotocol.ValidateAccepted(observed) == nil {
			return nil
		}
		return fmt.Errorf("proxy credential binding outcome unknown")
	}
	return nil
}

func validateProxyCredentialProjections(reader client.Reader, op *ha.HomeAssistantMigration, binding ha.MigrationProxyCredentialBinding, bundle *corev1.Secret) error {
	for name, expected := range map[string]map[string][]byte{
		proxyServerCredentialName(binding.Bundle.Name): {"server.crt": bundle.Data["server.crt"], "server.key": bundle.Data["server.key"], "client-ca.crt": bundle.Data["ca.crt"], "client.crt": bundle.Data["client.crt"]},
		proxyWorkerCredentialName(binding.Bundle.Name): {"ca.crt": bundle.Data["ca.crt"], "server.crt": bundle.Data["server.crt"], "client.crt": bundle.Data["client.crt"], "client.key": bundle.Data["client.key"]},
	} {
		projection := &corev1.Secret{}
		if reader.Get(context.Background(), client.ObjectKey{Namespace: op.Namespace, Name: name}, projection) != nil || projection.Immutable == nil || !*projection.Immutable || !reflect.DeepEqual(projection.Data, expected) {
			return fmt.Errorf("proxy credential projection unavailable")
		}
		owner := metav1.GetControllerOf(projection)
		if owner == nil || owner.UID != op.UID {
			return fmt.Errorf("proxy credential projection unavailable")
		}
	}
	return nil
}
