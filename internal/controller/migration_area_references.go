package controller

import (
	"context"
	"encoding/json"
	"fmt"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// resolveMigrationAreaReferences uses named Kubernetes references only to find
// their durable HA IDs. It does not fall back to mutable status or HA names.
func resolveMigrationAreaReferences(
	ctx context.Context,
	kube client.Reader,
	area *hav1.HomeAssistantArea,
	targetUID string,
) (string, []string, error) {
	var floorID string
	if area.Spec.FloorName != "" {
		floor := &hav1.HomeAssistantFloor{}
		if err := kube.Get(ctx, client.ObjectKey{Namespace: area.Namespace, Name: area.Spec.FloorName}, floor); err != nil {
			return "", nil, fmt.Errorf("referenced floor unavailable")
		}
		identity, err := migrationObjectIdentity(floor)
		if err != nil || identity.TargetUID != targetUID ||
			floor.Spec.HomeAssistantRef.Name != area.Spec.HomeAssistantRef.Name ||
			!floor.DeletionTimestamp.IsZero() {
			return "", nil, fmt.Errorf("referenced floor identity is unverified")
		}
		floorID = identity.SourceID
	}
	var labels []string
	seen := map[string]bool{}
	for _, name := range area.Spec.Labels {
		label := &hav1.HomeAssistantLabel{}
		if err := kube.Get(ctx, client.ObjectKey{Namespace: area.Namespace, Name: name}, label); err != nil {
			return "", nil, fmt.Errorf("referenced label unavailable")
		}
		identity, err := migrationObjectIdentity(label)
		if err != nil || identity.TargetUID != targetUID ||
			label.Spec.HomeAssistantRef.Name != area.Spec.HomeAssistantRef.Name ||
			!label.DeletionTimestamp.IsZero() ||
			seen[identity.SourceID] {
			return "", nil, fmt.Errorf("referenced label identity is unverified")
		}
		seen[identity.SourceID] = true
		labels = append(labels, identity.SourceID)
	}
	return floorID, labels, nil
}

// migrationAreaRegistryReferencesExist rechecks referenced IDs against HA.
// A missing, ambiguous, malformed or inaccessible registry remains blocked.
func migrationAreaRegistryReferencesExist(
	ctx context.Context,
	ha *haclient.Client,
	token, floorID string,
	labelIDs []string,
) bool {
	for _, item := range []struct {
		kind string
		ids  []string
	}{{"floor", []string{floorID}}, {"label", labelIDs}} {
		if len(item.ids) == 0 || (item.kind == "floor" && item.ids[0] == "") {
			continue
		}
		raw, err := ha.SendWebSocketCommand(
			ctx,
			token,
			"config/"+item.kind+"_registry/list",
			nil,
		)
		if err != nil || !migrationRegistryContainsUniqueIDs(raw, item.kind, item.ids) {
			return false
		}
	}
	return true
}

func migrationRegistryContainsUniqueIDs(raw json.RawMessage, kind string, ids []string) bool {
	if err := validateMigrationJSON(raw); err != nil {
		return false
	}
	var entries []map[string]json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || entries == nil {
		return false
	}
	counts := map[string]int{}
	for _, entry := range entries {
		var id string
		if json.Unmarshal(entry[kind+"_id"], &id) != nil || id == "" {
			return false
		}
		counts[id]++
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] || counts[id] != 1 {
			return false
		}
		seen[id] = true
	}
	return true
}
