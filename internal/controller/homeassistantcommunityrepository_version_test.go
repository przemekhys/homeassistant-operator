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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	"github.com/przemekhys/homeassistant-operator/internal/communityrepo"
)

var _ = Describe("HomeAssistantCommunityRepository API versions", func() {
	It("registers only the stable community repository kind", func() {
		knownTypes := scheme.Scheme.AllKnownTypes()
		_, alphaRegistered := knownTypes[schema.GroupVersionKind{
			Group: "ha.homeassistant.io", Version: "v1alpha1", Kind: "HomeAssistantCommunityRepository",
		}]
		_, stableRegistered := knownTypes[schema.GroupVersionKind{
			Group: "ha.homeassistant.io", Version: "v1", Kind: "HomeAssistantCommunityRepository",
		}]
		Expect(alphaRegistered).To(BeFalse())
		Expect(stableRegistered).To(BeTrue())
	})

	It("detects a stable owner when another declaration targets the same extension", func() {
		ownerRepo := &hav1.HomeAssistantCommunityRepository{
			ObjectMeta: metav1.ObjectMeta{Name: "stable-owner", Namespace: "default"},
			Spec: hav1.HomeAssistantCommunityRepositorySpec{
				HomeAssistantRef: hav1.HomeAssistantReference{Name: "home"},
				Category:         hav1.CategoryTheme,
				Repository:       "acme/theme",
				Ref:              "v1.0.0",
			},
		}
		Expect(k8sClient.Create(ctx, ownerRepo)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, ownerRepo)).To(Succeed()) })
		ownerRepo.Status.Phase = hav1.CommunityRepositoryPhaseInstalled
		ownerRepo.Status.ResolvedTarget = "example_theme"
		Expect(k8sClient.Status().Update(ctx, ownerRepo)).To(Succeed())

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
		Expect(owner.Name).To(Equal(ownerRepo.Name))
	})
})
