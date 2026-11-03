package main

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/test/utils"
)

func TestScopedBackupGateRejectsTargetsOutsideQualification(t *testing.T) {
	digest := utils.HomeAssistantBackupRestoreCandidate.PlatformDigests["amd64"]
	for _, tc := range []struct {
		name    string
		mutate  func(*scopedBackupGate, *corev1.Namespace, *corev1.Pod)
		allowed bool
	}{
		{name: "pinned isolated target", allowed: true},
		{name: "unlabelled namespace", mutate: func(_ *scopedBackupGate, ns *corev1.Namespace, _ *corev1.Pod) {
			ns.Labels = nil
		}},
		{name: "different namespace", mutate: func(g *scopedBackupGate, _ *corev1.Namespace, _ *corev1.Pod) {
			g.Target.Namespace = "other"
		}},
		{name: "different target UID", mutate: func(g *scopedBackupGate, _ *corev1.Namespace, _ *corev1.Pod) {
			g.Target.TargetUID = "other"
		}},
		{name: "different pinned digest", mutate: func(g *scopedBackupGate, _ *corev1.Namespace, _ *corev1.Pod) {
			g.Target.ImageDigest = "sha256:" + strings.Repeat("b", 64)
		}},
		{name: "different actual imageID", mutate: func(_ *scopedBackupGate, _ *corev1.Namespace, p *corev1.Pod) {
			p.Status.ContainerStatuses[0].ImageID = "containerd://sha256:" + strings.Repeat("b", 64)
		}},
		{name: "missing imageID", mutate: func(_ *scopedBackupGate, _ *corev1.Namespace, p *corev1.Pod) {
			p.Status.ContainerStatuses[0].ImageID = ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, hav1.AddToScheme} {
				if err := add(scheme); err != nil {
					t.Fatal(err)
				}
			}
			target := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{
				Name: "home", Namespace: "backup-qualification-test", UID: "target-uid",
			}}
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name: target.Namespace, Labels: map[string]string{utils.BackupQualificationNamespaceLabel: "true"},
			}}
			sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
				Name: target.Name, Namespace: target.Namespace, UID: "sts-uid",
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: hav1.GroupVersion.String(), Kind: "HomeAssistant", Name: target.Name,
					UID: target.UID, Controller: ptr.To(true),
				}},
			}}
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: target.Name + "-0", Namespace: target.Namespace, UID: "pod-uid",
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: "apps/v1", Kind: "StatefulSet", Name: sts.Name, UID: sts.UID, Controller: ptr.To(true),
					}},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: "home-assistant", Image: "ghcr.io/home-assistant/home-assistant@" + digest,
				}}},
				Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
					Name: "home-assistant", ImageID: "containerd://" + digest, Ready: true,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				}}},
			}
			gate := scopedBackupGate{Target: utils.BackupRestoreQualificationTarget{
				ImageDigest: digest, Namespace: target.Namespace, TargetUID: string(target.UID),
			}}
			if tc.mutate != nil {
				tc.mutate(&gate, ns, pod)
			}
			gate.Reader = fake.NewClientBuilder().WithScheme(scheme).WithObjects(target, ns, sts, pod).Build()
			if err := gate.AuthorizeBackup(context.Background(), target); (err == nil) != tc.allowed {
				t.Fatalf("allowed = %v, want %v", err == nil, tc.allowed)
			}
			archive, err := gate.ArchiveForBackupJob(context.Background(), target, "accepted-job")
			if (err == nil) != tc.allowed || (tc.allowed && archive != "accepted-job") {
				t.Fatal("candidate archive lookup did not enforce its live image and identity scope")
			}
			if _, err := gate.ArchiveForBackupJob(context.Background(), target, ""); err == nil {
				t.Fatal("candidate archive lookup accepted an empty job identity")
			}
		})
	}
}
