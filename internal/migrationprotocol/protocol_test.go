package migrationprotocol

import (
	"encoding/json"
	"strings"
	"testing"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
)

func TestCanonicalJSON(t *testing.T) {
	got, err := CanonicalJSON([]byte(`{"z":"<>&","a":{"b":1}}`))
	if err != nil || string(got) != `{"a":{"b":1},"z":"<>&"}` {
		t.Fatalf("canonical bytes: %s, %v", got, err)
	}
	for _, raw := range []string{`{"a":1,"a":2}`, `{"a":{"b":1,"b":2}}`, `{} {}`, "{\"x\":\"\xff\"}"} {
		if _, err := CanonicalJSON([]byte(raw)); err == nil {
			t.Fatal("accepted ambiguous JSON")
		}
	}
}

func TestGoldenCanonicalProtocolBytes(t *testing.T) {
	d := "sha256:" + strings.Repeat("a", 64)
	tests := []struct{ raw, digest string }{
		{`{"archiveStorage":{"descriptorName":"descriptor","pvcName":"archive"},"operationUID":"op","recoveryKey":{"key":"key","name":"key","resourceVersion":"2","uid":"key"},"reportID":"report","scopeDigest":"` + d + `","sourceEndpoint":"https://source","sourceID":"https://source","sourceToken":{"key":"token","name":"token","resourceVersion":"1","uid":"token"},"stage":"backup","target":{"name":"target","uid":"target"},"verificationDigest":"` + d + `","version":"v1"}`, "sha256:bbcfc15e16f27712d34df401b7e3ac38bf5169caeaecd00d0b43792bcc48bfbc"},
		{`{"archiveDigest":"` + d + `","archivePVCUID":"pvc","archiveRef":{"name":"descriptor","uid":"descriptor"},"operationUID":"op","reportID":"report","scopeDigest":"` + d + `","sourceID":"https://source","stage":"upload","target":{"name":"target","uid":"target"},"verificationDigest":"` + d + `","version":"v1"}`, "sha256:a6020e98795bb7c594c720b495c209d7cbb7453b863aca412588b2c4848a1099"},
		{`{"archiveDigest":"` + d + `","archivePVCUID":"pvc","archiveRef":{"name":"descriptor","uid":"descriptor"},"operationUID":"op","recoveryKey":{"key":"key","name":"key","resourceVersion":"2","uid":"key"},"reportID":"report","scopeDigest":"` + d + `","sourceID":"https://source","stage":"restore","target":{"name":"target","uid":"target"},"uploadRequestID":"` + d + `","verificationDigest":"` + d + `","version":"v1"}`, "sha256:eec223d8140bdc9904476b1c12ea752e384f41c9939d738e504a13d5d946685a"},
		{`{"data":{"configEntryId":"","entityId":"sensor.test","platform":"test","uniqueId":"unique"},"id":"sensor.test","kind":"EntityRegistryIdentity","version":"v1"}`, "sha256:18851f45a1a64abc0ce4e30b55e22ed881481e401b8d036853c765c86ed110d9"},
		{`{"data":{"domain":"test","entryId":"entry","source":"user"},"id":"entry","kind":"IntegrationEntryIdentity","version":"v1"}`, "sha256:1442f9141ea283bf66e984a4bca40a8cc5bde08f1a826bebe4a45deca8b70916"},
		{`{"data":{"entityId":"todo.test","items":[{"status":"needs_action","summary":"A","uid":"a"}]},"id":"todo.test","kind":"TodoItems","version":"v1"}`, "sha256:cd04cfb1819cdf02a315e2d37b2e096b762dfa2943f580950401af9fe335a94b"},
	}
	for _, tc := range tests {
		b, err := CanonicalJSON([]byte(tc.raw))
		if err != nil || string(b) != tc.raw {
			t.Fatalf("canonical bytes differ: %s, %v", b, err)
		}
		var value any
		if err := Decode(b, &value); err != nil {
			t.Fatal(err)
		}
		got, err := Digest(value)
		if err != nil || got != tc.digest {
			t.Fatalf("golden digest: %s %v", got, err)
		}
	}
}

func TestRequestGoldenDigests(t *testing.T) {
	d := "sha256:" + strings.Repeat("a", 64)
	r := ha.MigrationExecutionRequest{Version: "v1", Stage: "backup", OperationUID: "op", SourceID: "https://source", SourceEndpoint: "https://source", Target: ha.MigrationTarget{Name: "target", UID: "target"}, ReportID: "report", ScopeDigest: d, VerificationDigest: d, ArchiveStorage: &ha.MigrationArchiveStorage{PVCName: "archive", DescriptorName: "descriptor"}, SourceToken: &ha.MigrationSecretIdentity{Name: "token", Key: "token", UID: "token", ResourceVersion: "1"}, RecoveryKey: &ha.MigrationSecretIdentity{Name: "key", Key: "key", UID: "key", ResourceVersion: "2"}}
	for _, tc := range []struct{ stage, want string }{{"backup", "sha256:bbcfc15e16f27712d34df401b7e3ac38bf5169caeaecd00d0b43792bcc48bfbc"}, {"upload", "sha256:a6020e98795bb7c594c720b495c209d7cbb7453b863aca412588b2c4848a1099"}, {"restore", "sha256:eec223d8140bdc9904476b1c12ea752e384f41c9939d738e504a13d5d946685a"}} {
		r.Stage = tc.stage
		if tc.stage == "upload" {
			r.SourceEndpoint = ""
			r.ArchiveStorage = nil
			r.SourceToken = nil
			r.RecoveryKey = nil
			r.ArchiveRef = &ha.MigrationObjectReference{Name: "descriptor", UID: "descriptor"}
			r.ArchiveDigest = d
			r.ArchivePVCUID = "pvc"
		}
		if tc.stage == "restore" {
			r.UploadRequestID = d
			r.RecoveryKey = &ha.MigrationSecretIdentity{Name: "key", Key: "key", UID: "key", ResourceVersion: "2"}
		}
		id, err := RequestID(r)
		if err != nil || id != tc.want {
			t.Fatalf("%s: %s %v", tc.stage, id, err)
		}
		r.RequestID = id
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseRequest(b); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVerificationProjections(t *testing.T) {
	a := []byte(`{"entityId":"todo.test","items":[{"uid":"b","summary":"B","status":"completed"},{"uid":"a","summary":"A","status":"needs_action"}]}`)
	b := []byte(`{"items":[{"status":"needs_action","summary":"A","uid":"a"},{"status":"completed","summary":"B","uid":"b"}],"entityId":"todo.test"}`)
	x, err := ProjectionDigest("TodoItems", "todo.test", a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := ProjectionDigest("TodoItems", "todo.test", b)
	if err != nil || x != y {
		t.Fatal("item order changed digest", err)
	}
	for _, raw := range []string{`{"entityId":"todo.test"}`, `{"entityId":"todo.test","items":null}`, strings.Replace(string(a), `"uid":"b"`, `"uid":"a"`, 1), strings.Replace(string(a), `"summary":"B"`, `"password":"secret"`, 1)} {
		if _, err := ProjectionDigest("TodoItems", "todo.test", []byte(raw)); err == nil {
			t.Fatal("accepted incomplete/unknown projection")
		}
	}
	items := []ha.MigrationVerificationItem{{Kind: "TodoItems", ID: "todo.test", ExpectedDigest: x}}
	if _, err := VerificationDigest(items); err != nil {
		t.Fatal(err)
	}
	if _, err := VerificationDigest(append(items, items[0])); err == nil {
		t.Fatal("duplicate verification")
	}
}

func TestEvidenceParser(t *testing.T) {
	raw := []byte(`{"version":"v1","operationUID":"op","requestID":"sha256:` + strings.Repeat("a", 64) + `","stage":"backup","sourceID":"https://source","targetUID":"target","jobID":"01J/HA:JOB"}`)
	if _, err := ParseEvidence(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseEvidence([]byte(strings.Replace(string(raw), `"jobID":"01J/HA:JOB"`, `"password":"secret"`, 1))); err == nil {
		t.Fatal("accepted unknown evidence")
	}
	ref := &ha.MigrationEvidenceReference{APIVersion: "v1", Kind: "ConfigMap", Name: "evidence", UID: "uid", Key: "result.json"}
	if err := ValidateEvidenceRef(ref); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*ha.MigrationEvidenceReference){func(r *ha.MigrationEvidenceReference) { r.Kind = "Secret" }, func(r *ha.MigrationEvidenceReference) { r.Name = "https://example.test" }, func(r *ha.MigrationEvidenceReference) { r.UID = "" }, func(r *ha.MigrationEvidenceReference) { r.Key = "../secret" }} {
		copy := *ref
		change(&copy)
		if ValidateEvidenceRef(&copy) == nil {
			t.Fatal("accepted invalid evidence reference")
		}
	}
}

func TestProjectionGoldenDigests(t *testing.T) {
	for _, tc := range []struct{ kind, id, raw, digest string }{
		{"EntityRegistryIdentity", "sensor.test", `{"entityId":"sensor.test","uniqueId":"unique","platform":"test","configEntryId":""}`, "sha256:18851f45a1a64abc0ce4e30b55e22ed881481e401b8d036853c765c86ed110d9"},
		{"IntegrationEntryIdentity", "entry", `{"entryId":"entry","domain":"test","source":"user"}`, "sha256:1442f9141ea283bf66e984a4bca40a8cc5bde08f1a826bebe4a45deca8b70916"},
		{"TodoItems", "todo.test", `{"entityId":"todo.test","items":[{"uid":"a","summary":"A","status":"needs_action"}]}`, "sha256:cd04cfb1819cdf02a315e2d37b2e096b762dfa2943f580950401af9fe335a94b"},
	} {
		got, err := ProjectionDigest(tc.kind, tc.id, []byte(tc.raw))
		if err != nil || got != tc.digest {
			t.Fatalf("%s: %s %v", tc.kind, got, err)
		}
		var changed string
		switch tc.kind {
		case "EntityRegistryIdentity":
			changed = strings.Replace(tc.raw, `"unique"`, `"other"`, 1)
		case "IntegrationEntryIdentity":
			changed = strings.Replace(tc.raw, `"user"`, `"other"`, 1)
		case "TodoItems":
			changed = strings.Replace(tc.raw, `"summary":"A"`, `"summary":"changed"`, 1)
		}
		other, err := ProjectionDigest(tc.kind, tc.id, []byte(changed))
		if err != nil || other == got {
			t.Fatalf("%s did not bind significant field: %v", tc.kind, err)
		}
	}
}

func TestVerificationSetGolden(t *testing.T) {
	items := []ha.MigrationVerificationItem{
		{Kind: "TodoItems", ID: "todo.test", ExpectedDigest: "sha256:cd04cfb1819cdf02a315e2d37b2e096b762dfa2943f580950401af9fe335a94b"},
		{Kind: "EntityRegistryIdentity", ID: "sensor.test", ExpectedDigest: "sha256:18851f45a1a64abc0ce4e30b55e22ed881481e401b8d036853c765c86ed110d9"},
	}
	want := "sha256:e1b171848db365d24632c725a163ac01d91d9a60ca0addbafd9354f8f02a256f"
	sorted := []ha.MigrationVerificationItem{items[1], items[0]}
	bytes, err := Canonical(struct {
		Version string                         `json:"version"`
		Items   []ha.MigrationVerificationItem `json:"items"`
	}{"v1", sorted})
	if err != nil || string(bytes) != `{"items":[{"expectedDigest":"sha256:18851f45a1a64abc0ce4e30b55e22ed881481e401b8d036853c765c86ed110d9","id":"sensor.test","kind":"EntityRegistryIdentity"},{"expectedDigest":"sha256:cd04cfb1819cdf02a315e2d37b2e096b762dfa2943f580950401af9fe335a94b","id":"todo.test","kind":"TodoItems"}],"version":"v1"}` {
		t.Fatalf("unexpected canonical set bytes: %s, %v", bytes, err)
	}
	got, err := VerificationDigest(items)
	if err != nil || got != want {
		t.Fatalf("verification digest %s: %v", got, err)
	}
	items[0], items[1] = items[1], items[0]
	got, err = VerificationDigest(items)
	if err != nil || got != want {
		t.Fatalf("verification order affected digest %s: %v", got, err)
	}
}

func TestApprovalsBindVerificationSet(t *testing.T) {
	items := []ha.MigrationVerificationItem{{Kind: "TodoItems", ID: "todo.test", ExpectedDigest: "sha256:" + strings.Repeat("a", 64)}}
	digest, err := VerificationDigest(items)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"backup", "restore", "resume", "manual-cutover"} {
		op := &ha.HomeAssistantMigration{Spec: ha.HomeAssistantMigrationSpec{ExecutionProtocol: "v1", Verification: items, Approvals: []ha.MigrationApproval{{ID: id, Approved: true, Digest: "sha256:" + strings.Repeat("b", 64), VerificationDigest: digest}}}}
		if err := ValidateApprovals(op); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		op.Spec.Approvals[0].VerificationDigest = "sha256:" + strings.Repeat("c", 64)
		if ValidateApprovals(op) == nil {
			t.Fatalf("%s accepted another verification set", id)
		}
		op.Spec.Approvals[0].VerificationDigest = digest
		op.Spec.Verification = append([]ha.MigrationVerificationItem(nil), items...)
		op.Spec.Verification[0].ExpectedDigest = "sha256:" + strings.Repeat("d", 64)
		if ValidateApprovals(op) == nil {
			t.Fatalf("%s accepted changed expectations", id)
		}
	}
	op := &ha.HomeAssistantMigration{Spec: ha.HomeAssistantMigrationSpec{ExecutionProtocol: "v1", Verification: items, Approvals: []ha.MigrationApproval{{ID: "resume", Approved: true, Digest: "not-a-result", VerificationDigest: digest}}}}
	if ValidateApprovals(op) == nil {
		t.Fatal("resume accepted invalid result digest")
	}
	op.Spec.Approvals[0].Digest = "sha256:" + strings.Repeat("c", 64)
	op.Spec.Approvals = append(op.Spec.Approvals, op.Spec.Approvals[0])
	if ValidateApprovals(op) == nil {
		t.Fatal("duplicate approvals accepted")
	}
}
