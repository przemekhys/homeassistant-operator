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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/communityrepo"
)

var _ = Describe("HomeAssistantCommunityRepository API versions", func() {
	It("serves an existing alpha resource through the stable API", func() {
		alpha := &hav1alpha1.HomeAssistantCommunityRepository{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-compatible", Namespace: "default"},
			Spec: hav1alpha1.HomeAssistantCommunityRepositorySpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: "home"},
				Category:         hav1alpha1.CategoryTheme,
				Repository:       "acme/theme",
				Ref:              "v1.0.0",
			},
		}
		Expect(k8sClient.Create(ctx, alpha)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, alpha)).To(Succeed()) })

		stable := &hav1.HomeAssistantCommunityRepository{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: alpha.Name, Namespace: alpha.Namespace}, stable)).To(Succeed())
		Expect(stable.UID).To(Equal(alpha.UID))
		Expect(stable.Spec.HomeAssistantRef.Name).To(Equal(alpha.Spec.HomeAssistantRef.Name))
		Expect(stable.Spec.Category).To(Equal(hav1.CategoryTheme))
		Expect(stable.Spec.Repository).To(Equal(alpha.Spec.Repository))
		Expect(stable.Spec.Ref).To(Equal(alpha.Spec.Ref))
	})

	It("detects an alpha owner when a stable declaration targets the same extension", func() {
		alpha := &hav1alpha1.HomeAssistantCommunityRepository{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-owner", Namespace: "default"},
			Spec: hav1alpha1.HomeAssistantCommunityRepositorySpec{
				HomeAssistantRef: hav1alpha1.HomeAssistantReference{Name: "home"},
				Category:         hav1alpha1.CategoryTheme,
				Repository:       "acme/theme",
				Ref:              "v1.0.0",
			},
		}
		Expect(k8sClient.Create(ctx, alpha)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, alpha)).To(Succeed()) })
		alpha.Status.Phase = hav1alpha1.PhaseInstalled
		alpha.Status.ResolvedTarget = "example_theme"
		Expect(k8sClient.Status().Update(ctx, alpha)).To(Succeed())

		candidate := &hav1.HomeAssistantCommunityRepository{
			ObjectMeta: metav1.ObjectMeta{Name: "stable-candidate", Namespace: "default"},
			Spec: hav1.HomeAssistantCommunityRepositorySpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				Category:         hav1.CategoryTheme,
				Repository:       "acme/other-theme",
				Ref:              "v1.0.0",
			},
		}
		owner, err := (&HomeAssistantCommunityRepositoryReconciler{Client: k8sClient}).findConflictingOwner(
			ctx, candidate, "home", communityrepo.Resolved{ResolvedTarget: "example_theme"},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(owner).NotTo(BeNil())
		Expect(owner.Name).To(Equal(alpha.Name))
	})
})
