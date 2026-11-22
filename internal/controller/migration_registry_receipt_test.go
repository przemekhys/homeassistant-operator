package controller

import (
	"encoding/json"
	"strings"
	"testing"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMigrationRegistryReceiptRejectsChangedIntentOrObservation(t *testing.T) {
	object := &hav1.HomeAssistantFloor{
		ObjectMeta: metav1.ObjectMeta{UID: "object-uid", Generation: 3},
	}
	identity := migrationIdentity{SourceID: "floor-id", TargetUID: "target-uid"}
	desired := json.RawMessage(`{"name":"private-name","level":1}`)
	observed := json.RawMessage(`{"level":1,"name":"private-name"}`)
	receipt, err := newMigrationRegistryReceipt(object, identity, desired, observed)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || strings.Contains(string(encoded), "private-name") {
		t.Fatalf("unsafe receipt: %s %v", encoded, err)
	}
	if !migrationRegistryReceiptMatches(
		receipt,
		object.DeepCopy(),
		identity,
		desired,
		observed,
	) {
		t.Fatal("restart lost valid comparison evidence")
	}
	changed := object.DeepCopy()
	changed.Generation++
	if migrationRegistryReceiptMatches(receipt, changed, identity, desired, observed) {
		t.Fatal("generation change accepted")
	}
	changed = object.DeepCopy()
	changed.UID = "new-object"
	if migrationRegistryReceiptMatches(receipt, changed, identity, desired, observed) {
		t.Fatal("UID change accepted")
	}
	if migrationRegistryReceiptMatches(
		receipt,
		object,
		migrationIdentity{SourceID: "another", TargetUID: "target-uid"},
		desired,
		observed,
	) {
		t.Fatal("source change accepted")
	}
	if migrationRegistryReceiptMatches(
		receipt,
		object,
		identity,
		desired,
		json.RawMessage(`{"name":"changed","level":1}`),
	) {
		t.Fatal("changed HA state accepted")
	}
}
