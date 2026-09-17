/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

var _ = Describe("Additional volume update detection", func() {
	statefulSetWithVolume := func(volume corev1.Volume, mount corev1.VolumeMount) *appsv1.StatefulSet {
		return &appsv1.StatefulSet{
			Spec: appsv1.StatefulSetSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Volumes:    []corev1.Volume{*volume.DeepCopy()},
						Containers: []corev1.Container{{Name: "home-assistant", VolumeMounts: []corev1.VolumeMount{*mount.DeepCopy()}}},
					},
				},
			},
		}
	}

	baseVolume := func() corev1.Volume {
		return corev1.Volume{
			Name: "extra",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: "extra"},
			},
		}
	}
	baseMount := func() corev1.VolumeMount {
		return corev1.VolumeMount{Name: "extra", MountPath: "/extra"}
	}

	DescribeTable("detects optional field additions and removals",
		func(mutate func(*corev1.Volume, *corev1.VolumeMount)) {
			volume, mount := baseVolume(), baseMount()
			current := statefulSetWithVolume(volume, mount)
			mutate(&volume, &mount)
			desired := statefulSetWithVolume(volume, mount)

			Expect(needsUpdate(current, desired)).To(BeTrue(), "addition must trigger an update")
			Expect(needsUpdate(desired, current)).To(BeTrue(), "removal must trigger an update")
		},
		Entry("subPath", func(_ *corev1.Volume, mount *corev1.VolumeMount) {
			mount.SubPath = "nested"
		}),
		Entry("mountPropagation", func(_ *corev1.Volume, mount *corev1.VolumeMount) {
			mount.MountPropagation = ptr.To(corev1.MountPropagationHostToContainer)
		}),
		Entry("secret optional", func(volume *corev1.Volume, _ *corev1.VolumeMount) {
			volume.Secret.Optional = ptr.To(true)
		}),
	)

	It("ignores API-server defaultMode values", func() {
		desiredVolume := corev1.Volume{
			Name: "extra",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "extra"},
				},
			},
		}
		currentVolume := *desiredVolume.DeepCopy()
		currentVolume.ConfigMap.DefaultMode = ptr.To[int32](corev1.ConfigMapVolumeSourceDefaultMode)
		mount := baseMount()

		Expect(needsUpdate(
			statefulSetWithVolume(currentVolume, mount),
			statefulSetWithVolume(desiredVolume, mount),
		)).To(BeFalse())
	})

	It("ignores the API-server default hostPath type", func() {
		desiredVolume := corev1.Volume{
			Name: "extra",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/homeassistant"},
			},
		}
		currentVolume := *desiredVolume.DeepCopy()
		currentVolume.HostPath.Type = ptr.To(corev1.HostPathUnset)
		mount := baseMount()

		Expect(needsUpdate(
			statefulSetWithVolume(currentVolume, mount),
			statefulSetWithVolume(desiredVolume, mount),
		)).To(BeFalse())
	})
})
