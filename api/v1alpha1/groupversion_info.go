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

// Package v1alpha1 contains API Schema definitions for the ha v1alpha1 API group.
// Resources in this package are experimental: fields may change shape or be
// removed in a minor release, with no deprecation period. That is the trade-off
// for shipping a capability whose real-world behaviour cannot be settled by
// tests alone; it graduates to a stable group once it has been exercised on
// hardware the maintainers do not have.
// +kubebuilder:object:generate=true
// +groupName=ha.homeassistant.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "ha.homeassistant.io", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = &schemeBuilder{groupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// schemeBuilder is a minimal, dependency-free replacement for the deprecated
// sigs.k8s.io/controller-runtime/pkg/scheme.Builder: api packages should only
// depend on the standard library, k8s.io/apimachinery and other api packages,
// not on controller-runtime. It keeps the same Register/AddToScheme call
// shape so the per-Kind `SchemeBuilder.Register(&X{}, &XList{})` calls in
// this package's *_types.go files don't need to change.
type schemeBuilder struct {
	groupVersion   schema.GroupVersion
	runtimeBuilder runtime.SchemeBuilder
}

// Register adds one or more objects to the SchemeBuilder so they can be added to a Scheme.
func (bld *schemeBuilder) Register(object ...runtime.Object) *schemeBuilder {
	bld.runtimeBuilder = append(bld.runtimeBuilder, func(scheme *runtime.Scheme) error {
		scheme.AddKnownTypes(bld.groupVersion, object...)
		metav1.AddToGroupVersion(scheme, bld.groupVersion)
		return nil
	})
	return bld
}

// AddToScheme adds all registered types to s.
func (bld *schemeBuilder) AddToScheme(s *runtime.Scheme) error {
	return bld.runtimeBuilder.AddToScheme(s)
}
