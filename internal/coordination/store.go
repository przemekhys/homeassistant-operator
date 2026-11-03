// Package coordination persists the safety-critical state used by backup and
// restore operations. It deliberately uses direct API reads at authorization
// boundaries: an informer cache is useful for observation, but can be stale
// when deciding whether a destructive operation may proceed.
package coordination

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	FormatVersion       = "v1"
	MaxEvidenceBytes    = 256 * 1024
	coordinationDataKey = "coordination.json"
	evidenceDataKey     = "evidence.json"
	instanceUIDLabel    = "ha.homeassistant.io/instance-uid"
	operationUIDLabel   = "ha.homeassistant.io/operation-uid"
)

var (
	ErrEvidenceTooLarge = errors.New("coordination evidence exceeds the 256 KiB budget")
	ErrEvidenceMissing  = errors.New("coordination evidence is missing")
)

// Root is the bounded, per-instance coordination document. It contains no
// credentials or operation history.
type Root struct {
	FormatVersion   string                          `json:"formatVersion"`
	InstanceUID     string                          `json:"instanceUID"`
	Initialized     bool                            `json:"initialized"`
	Epoch           int64                           `json:"epoch"`
	Mode            string                          `json:"mode"`
	ActivePermit    *Permit                         `json:"activePermit,omitempty"`
	RestoreHold     *Hold                           `json:"restoreHold,omitempty"`
	MaintenanceHold *MaintenanceHold                `json:"maintenanceHold,omitempty"`
	DrainAcks       map[string]DrainAcknowledgement `json:"drainAcks,omitempty"`
}

// PermitState records the only transitions that may authorize an effect.
type PermitState string

const (
	PermitPrepared  PermitState = "Prepared"
	PermitInFlight  PermitState = "InFlight"
	PermitConfirmed PermitState = "Confirmed"
	PermitUnknown   PermitState = "Unknown"
)

// Permit identifies one caller and one operation substep. It is never released
// merely because a heartbeat, process, or Lease expires.
type Permit struct {
	ID           string      `json:"id"`
	Owner        string      `json:"owner"`
	OperationUID string      `json:"operationUID,omitempty"`
	Class        string      `json:"class"`
	State        PermitState `json:"state"`
	Epoch        int64       `json:"epoch"`
}

// Hold prevents ordinary synchronization until an explicit safe resolution.
type Hold struct {
	OperationUID string `json:"operationUID"`
	Reason       string `json:"reason"`
}

// MaintenanceHold preserves the running workload while a restore-related
// operation is unsafe to interrupt. AllowedTickets are consumed only by the
// explicitly authorized preparation/resume paths; ordinary reconciliation has
// no authority to bypass this hold.
type MaintenanceHold struct {
	OperationUID   string   `json:"operationUID"`
	Reason         string   `json:"reason"`
	AllowedTickets []string `json:"allowedTickets,omitempty"`
}

// DrainAcknowledgement is tied to both epoch and pod/process identity. An old
// acknowledgement cannot prove a writer has drained after an epoch change.
type DrainAcknowledgement struct {
	Writer string `json:"writer"`
	Epoch  int64  `json:"epoch"`
	PodUID string `json:"podUID,omitempty"`
}

// Evidence is one retained operation document. Intent and approval are kept
// separately from the mutable journal so callers cannot accidentally mutate
// accepted inputs while recording observations.
type Evidence struct {
	FormatVersion string          `json:"formatVersion"`
	OperationUID  string          `json:"operationUID"`
	TargetUID     string          `json:"targetUID"`
	Intent        json.RawMessage `json:"intent,omitempty"`
	Approval      json.RawMessage `json:"approval,omitempty"`
	Journal       json.RawMessage `json:"journal,omitempty"`
}

// Store owns serialization into ConfigMaps. Reader must be an uncached direct
// API reader; Client performs create/update with resourceVersion CAS.
type Store struct {
	Reader client.Reader
	Client client.Client
}

func RootName(instanceUID types.UID) string      { return string(instanceUID) + "-ha-coordination" }
func EvidenceName(operationUID types.UID) string { return string(operationUID) + "-ha-evidence" }

func (s Store) CreateRoot(ctx context.Context, namespace string, instanceUID types.UID) (*Root, error) {
	root := &Root{FormatVersion: FormatVersion, InstanceUID: string(instanceUID), Initialized: true, Mode: "Open"}
	raw, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      RootName(instanceUID),
			Namespace: namespace,
			Labels:    map[string]string{instanceUIDLabel: string(instanceUID)},
		},
		Data: map[string]string{coordinationDataKey: string(raw)},
	}
	err = s.Client.Create(ctx, cm)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	if apierrors.IsAlreadyExists(err) {
		return s.GetRoot(ctx, namespace, instanceUID)
	}
	return root, nil
}

func (s Store) GetRoot(ctx context.Context, namespace string, instanceUID types.UID) (*Root, error) {
	cm := &corev1.ConfigMap{}
	if err := s.Reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: RootName(instanceUID)}, cm); err != nil {
		return nil, err
	}
	var root Root
	if err := json.Unmarshal([]byte(cm.Data[coordinationDataKey]), &root); err != nil {
		return nil, fmt.Errorf("decode coordination root: %w", err)
	}
	if root.FormatVersion != FormatVersion || root.InstanceUID != string(instanceUID) {
		return nil, fmt.Errorf("invalid coordination root identity")
	}
	return &root, nil
}

// UpdateRoot fetches directly and executes mutate once. An API conflict is
// returned to the caller; effects must never occur inside this method.
func (s Store) UpdateRoot(
	ctx context.Context,
	namespace string,
	instanceUID types.UID,
	mutate func(*Root) error,
) (*Root, error) {
	cm := &corev1.ConfigMap{}
	if err := s.Reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: RootName(instanceUID)}, cm); err != nil {
		return nil, err
	}
	var root Root
	if err := json.Unmarshal([]byte(cm.Data[coordinationDataKey]), &root); err != nil {
		return nil, err
	}
	if err := mutate(&root); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(&root)
	if err != nil {
		return nil, err
	}
	cm.Data[coordinationDataKey] = string(raw)
	if err := s.Client.Update(ctx, cm); err != nil {
		return nil, err
	}
	return &root, nil
}

func (s Store) CreateEvidence(
	ctx context.Context,
	namespace string,
	operationUID, targetUID types.UID,
	intent json.RawMessage,
) (*Evidence, error) {
	evidence := &Evidence{
		FormatVersion: FormatVersion,
		OperationUID:  string(operationUID),
		TargetUID:     string(targetUID),
		Intent:        intent,
	}
	if err := validateEvidenceSize(evidence); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return nil, err
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      EvidenceName(operationUID),
			Namespace: namespace,
			Labels:    map[string]string{operationUIDLabel: string(operationUID), instanceUIDLabel: string(targetUID)},
		},
		Data: map[string]string{evidenceDataKey: string(raw)},
	}
	err = s.Client.Create(ctx, cm)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	if apierrors.IsAlreadyExists(err) {
		existing, getErr := s.GetEvidence(ctx, namespace, operationUID)
		if getErr != nil {
			return nil, getErr
		}
		if existing.TargetUID != string(targetUID) || string(existing.Intent) != string(intent) {
			return nil, errors.New("operation evidence already exists with different immutable intent")
		}
		return existing, nil
	}
	return evidence, nil
}

func (s Store) GetEvidence(ctx context.Context, namespace string, operationUID types.UID) (*Evidence, error) {
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: namespace, Name: EvidenceName(operationUID)}
	if err := s.Reader.Get(ctx, key, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrEvidenceMissing
		}
		return nil, err
	}
	var evidence Evidence
	if err := json.Unmarshal([]byte(cm.Data[evidenceDataKey]), &evidence); err != nil {
		return nil, fmt.Errorf("decode operation evidence: %w", err)
	}
	if evidence.FormatVersion != FormatVersion || evidence.OperationUID != string(operationUID) {
		return nil, fmt.Errorf("invalid operation evidence identity")
	}
	return &evidence, nil
}

// AcceptApproval stores the reviewed approval exactly once. A different
// approval is rejected rather than silently replacing an accepted consent.
func (s Store) AcceptApproval(
	ctx context.Context,
	namespace string,
	operationUID types.UID,
	approval json.RawMessage,
) (*Evidence, error) {
	return s.updateEvidence(ctx, namespace, operationUID, func(evidence *Evidence) error {
		if len(evidence.Approval) != 0 && string(evidence.Approval) != string(approval) {
			return errors.New("operation approval is immutable once accepted")
		}
		evidence.Approval = append(json.RawMessage(nil), approval...)
		return nil
	})
}

// UpdateJournal is the sole mutable portion of evidence. It is intentionally
// separate from intent and approval to make accidental consent mutation hard.
func (s Store) UpdateJournal(
	ctx context.Context,
	namespace string,
	operationUID types.UID,
	mutate func(json.RawMessage) (json.RawMessage, error),
) (*Evidence, error) {
	return s.updateEvidence(ctx, namespace, operationUID, func(evidence *Evidence) error {
		next, err := mutate(append(json.RawMessage(nil), evidence.Journal...))
		if err != nil {
			return err
		}
		evidence.Journal = next
		return nil
	})
}

func (s Store) updateEvidence(
	ctx context.Context,
	namespace string,
	operationUID types.UID,
	mutate func(*Evidence) error,
) (*Evidence, error) {
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: namespace, Name: EvidenceName(operationUID)}
	if err := s.Reader.Get(ctx, key, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrEvidenceMissing
		}
		return nil, err
	}
	var evidence Evidence
	if err := json.Unmarshal([]byte(cm.Data[evidenceDataKey]), &evidence); err != nil {
		return nil, fmt.Errorf("decode operation evidence: %w", err)
	}
	if evidence.FormatVersion != FormatVersion || evidence.OperationUID != string(operationUID) {
		return nil, fmt.Errorf("invalid operation evidence identity")
	}
	if err := mutate(&evidence); err != nil {
		return nil, err
	}
	if err := validateEvidenceSize(&evidence); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(&evidence)
	if err != nil {
		return nil, err
	}
	cm.Data[evidenceDataKey] = string(raw)
	if err := s.Client.Update(ctx, cm); err != nil {
		return nil, err
	}
	return &evidence, nil
}

func validateEvidenceSize(evidence *Evidence) error {
	raw, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	if len(raw) > MaxEvidenceBytes {
		return ErrEvidenceTooLarge
	}
	return nil
}
