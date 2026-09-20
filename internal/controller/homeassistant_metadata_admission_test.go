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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
)

var _ = Describe("HomeAssistant metadata admission", func() {
	DescribeTable("rejects operator-managed metadata keys",
		func(field, key string) {
			name := "reserved-" + strings.NewReplacer("/", "-", ".", "-").Replace(key)
			ha := &hav1.HomeAssistant{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			}
			if field == "annotations" {
				ha.Spec.Annotations = map[string]string{key: "override"}
			} else {
				ha.Spec.Labels = map[string]string{key: "override"}
			}

			err := k8sClient.Create(ctx, ha)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected create admission rejection, got %v", err)

			ha.Spec.Annotations = nil
			ha.Spec.Labels = nil
			Expect(k8sClient.Create(ctx, ha)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, ha) })
			if field == "annotations" {
				ha.Spec.Annotations = map[string]string{key: "override"}
			} else {
				ha.Spec.Labels = map[string]string{key: "override"}
			}
			err = k8sClient.Update(ctx, ha)
			Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected update admission rejection, got %v", err)
		},
		Entry("config hash annotation", "annotations", "ha.homeassistant.io/config-hash"),
		Entry("secrets hash annotation", "annotations", "ha.homeassistant.io/secrets-hash"),
		Entry("community repository hash annotation", "annotations", "ha.homeassistant.io/community-repository-hash"),
		Entry("user annotations tracker", "annotations", "ha.homeassistant.io/user-annotations"),
		Entry("user labels tracker", "annotations", "ha.homeassistant.io/user-labels"),
		Entry("name selector label", "labels", "app.kubernetes.io/name"),
		Entry("instance selector label", "labels", "app.kubernetes.io/instance"),
		Entry("managed-by selector label", "labels", "app.kubernetes.io/managed-by"),
	)

	It("accepts custom annotation and label keys", func() {
		ha := &hav1.HomeAssistant{
			ObjectMeta: metav1.ObjectMeta{Name: "custom-metadata", Namespace: "default"},
			Spec: hav1.HomeAssistantSpec{
				Annotations: map[string]string{"example.com/annotation": "value"},
				Labels:      map[string]string{"example.com/label": "value"},
			},
		}
		Expect(k8sClient.Create(ctx, ha)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, ha) })
	})
})
