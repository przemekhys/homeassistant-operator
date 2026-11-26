package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// migrationRegistryReceipt is comparison evidence, not authorization to write.
// A future reconciler must re-read HA and revalidate the evidence before every
// action; an annotation or status carrying this value alone proves nothing.
type migrationRegistryReceipt struct {
	ObjectUID        string `json:"objectUID"`
	Generation       int64  `json:"generation"`
	TargetUID        string `json:"targetUID"`
	SourceID         string `json:"sourceID"`
	ComparisonDigest string `json:"comparisonDigest"`
}

func newMigrationRegistryReceipt(
	obj client.Object,
	identity migrationIdentity,
	desired, observed json.RawMessage,
) (migrationRegistryReceipt, error) {
	if obj == nil || obj.GetUID() == "" || obj.GetGeneration() <= 0 ||
		identity.SourceID == "" ||
		identity.TargetUID == "" {
		return migrationRegistryReceipt{}, fmt.Errorf(
			"migration receipt requires complete identity",
		)
	}
	want, err := canonicalMigrationJSON(desired)
	if err != nil {
		return migrationRegistryReceipt{}, fmt.Errorf("invalid desired comparison data")
	}
	actual, err := canonicalMigrationJSON(observed)
	if err != nil {
		return migrationRegistryReceipt{}, fmt.Errorf("invalid observed comparison data")
	}
	if string(want) != string(actual) {
		return migrationRegistryReceipt{}, fmt.Errorf("migration comparison is not equal")
	}
	// JSON framing prevents ambiguous concatenations between individual fields.
	framed, err := json.Marshal(struct {
		ObjectUID  string          `json:"objectUID"`
		Generation int64           `json:"generation"`
		TargetUID  string          `json:"targetUID"`
		SourceID   string          `json:"sourceID"`
		Definition json.RawMessage `json:"definition"`
	}{string(obj.GetUID()), obj.GetGeneration(), identity.TargetUID, identity.SourceID, want})
	if err != nil {
		return migrationRegistryReceipt{}, fmt.Errorf("invalid comparison data")
	}
	digest := sha256.Sum256(framed)
	return migrationRegistryReceipt{
		ObjectUID:        string(obj.GetUID()),
		Generation:       obj.GetGeneration(),
		TargetUID:        identity.TargetUID,
		SourceID:         identity.SourceID,
		ComparisonDigest: "sha256:" + hex.EncodeToString(digest[:]),
	}, nil
}

func migrationRegistryReceiptMatches(
	receipt migrationRegistryReceipt,
	obj client.Object,
	identity migrationIdentity,
	desired, observed json.RawMessage,
) bool {
	current, err := newMigrationRegistryReceipt(obj, identity, desired, observed)
	return err == nil && current == receipt
}
