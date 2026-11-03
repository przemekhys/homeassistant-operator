package main

import (
	"context"
	"strings"
	"testing"

	"github.com/przemekhys/homeassistant-operator/test/utils"
)

func TestQualificationOptionsFailClosedBeforeConnecting(t *testing.T) {
	valid := qualificationOptions{
		Target: utils.BackupRestoreQualificationTarget{
			ImageDigest: "sha256:" + strings.Repeat("a", 64), Namespace: "backup-qualification-test", TargetUID: "target-uid",
		},
		Context: "k3d-disposable", TargetName: "home", Operation: "backup", HAURL: "http://127.0.0.1:18123",
	}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*qualificationOptions){
		func(o *qualificationOptions) { o.Context = "production" },
		func(o *qualificationOptions) { o.Context = "" },
		func(o *qualificationOptions) { o.Target.ImageDigest = "latest" },
		func(o *qualificationOptions) { o.Target.TargetUID = "" },
		func(o *qualificationOptions) { o.Target.Namespace = "default" },
		func(o *qualificationOptions) { o.Operation = "" },
		func(o *qualificationOptions) { o.HAURL = "http://192.0.2.1:8123" },
		func(o *qualificationOptions) { o.HAURL = "http://localhost:8123" },
		func(o *qualificationOptions) { o.HAURL = "http://canary@127.0.0.1:8123" },
		func(o *qualificationOptions) { o.HAURL = "http://127.0.0.1:8123?token=canary" },
	} {
		o := valid
		mutate(&o)
		if err := run(context.Background(), o); err == nil || strings.Contains(err.Error(), "canary") {
			t.Fatal("unsafe qualification options were accepted or disclosed")
		}
	}
}
