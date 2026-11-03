package controller

import (
	"context"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/coordination"
)

const (
	synchronizationWriterSuffix    = "-sync-writer"
	synchronizationWriterTokenName = "synchronization-writer-token"
	synchronizationWriterTokenPath = "/var/run/secrets/ha-coordination"
)

func synchronizationWriterServiceAccountName(ha *hav1.HomeAssistant) string {
	maxBaseLength := 63 - len(synchronizationWriterSuffix)
	base := strings.TrimRight(ha.Name, "-")
	if len(base) > maxBaseLength {
		base = strings.TrimRight(base[:maxBaseLength], "-")
	}
	return base + synchronizationWriterSuffix
}

// reconcileSynchronizationWriterAccess creates the narrowly scoped identity
// used by pod-side writers. It is intentionally absent unless coordinated
// synchronization is explicitly enabled.
func (r *HomeAssistantReconciler) reconcileSynchronizationWriterAccess(ctx context.Context, ha *hav1.HomeAssistant) error {
	if !coordinationEnabled(ha) {
		return nil
	}
	name := synchronizationWriterServiceAccountName(ha)
	objects := []client.Object{
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ha.Namespace}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ha.Namespace}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ha.Namespace}},
	}
	for _, object := range objects {
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, object, func() error {
			if err := controllerutil.SetControllerReference(ha, object, r.Scheme); err != nil {
				return err
			}
			switch typed := object.(type) {
			case *rbacv1.Role:
				typed.Rules = []rbacv1.PolicyRule{{
					APIGroups:     []string{""},
					Resources:     []string{"configmaps"},
					ResourceNames: []string{coordination.RootName(ha.UID)},
					Verbs:         []string{"get", "update"},
				}}
			case *rbacv1.RoleBinding:
				typed.RoleRef = rbacv1.RoleRef{
					APIGroup: rbacv1.GroupName, Kind: "Role", Name: name,
				}
				typed.Subjects = []rbacv1.Subject{{
					Kind: "ServiceAccount", Name: name, Namespace: ha.Namespace,
				}}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func configureSynchronizationWriterPodAccess(sts *appsv1.StatefulSet, ha *hav1.HomeAssistant) {
	if !coordinationEnabled(ha) {
		return
	}
	podSpec := &sts.Spec.Template.Spec
	podSpec.ServiceAccountName = synchronizationWriterServiceAccountName(ha)
	automount := false
	podSpec.AutomountServiceAccountToken = &automount
	podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
		Name: synchronizationWriterTokenName,
		VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
			Sources: []corev1.VolumeProjection{
				{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
					Audience:          "https://kubernetes.default.svc",
					ExpirationSeconds: int64Ptr(3600),
					Path:              "token",
				}},
				{ConfigMap: &corev1.ConfigMapProjection{
					LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"},
					Items:                []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}},
				}},
			},
		}},
	})
	mount := corev1.VolumeMount{Name: synchronizationWriterTokenName, MountPath: synchronizationWriterTokenPath, ReadOnly: true}
	for index := range podSpec.InitContainers {
		podSpec.InitContainers[index].VolumeMounts = append(podSpec.InitContainers[index].VolumeMounts, mount)
		podSpec.InitContainers[index].Env = append(podSpec.InitContainers[index].Env,
			synchronizationWriterEnvironment(ha, podSpec.InitContainers[index].Name)...)
	}
	for index := range podSpec.Containers {
		if podSpec.Containers[index].Name != "home-assistant" {
			podSpec.Containers[index].VolumeMounts = append(podSpec.Containers[index].VolumeMounts, mount)
			podSpec.Containers[index].Env = append(podSpec.Containers[index].Env,
				synchronizationWriterEnvironment(ha, podSpec.Containers[index].Name)...)
		}
	}
}

func synchronizationWriterEnvironment(ha *hav1.HomeAssistant, writer string) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
		{Name: "POD_UID", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}}},
		{Name: "HA_SYNC_COORDINATION_CONFIGMAP", Value: coordination.RootName(ha.UID)},
		{Name: "HA_SYNC_WRITER", Value: "pod/$(POD_UID)/" + writer},
	}
}

func int64Ptr(value int64) *int64 { return &value }
