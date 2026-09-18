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

package v1

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
)

func TestValidateHomeAssistantTLS(t *testing.T) {
	tests := []struct {
		name         string
		spec         hav1.HomeAssistantSpec
		wantErrs     int
		wantWarnings int
	}{
		{
			name: "empty spec is valid",
			spec: hav1.HomeAssistantSpec{},
		},
		{
			name: "ingress TLS with issuer is valid",
			spec: hav1.HomeAssistantSpec{Ingress: &hav1.IngressSpec{
				Enabled: true,
				TLS:     &hav1.IngressTLSSpec{Enabled: true, IssuerRef: &hav1.IssuerReference{Name: "i"}},
			}},
		},
		{
			name: "ingress TLS with issuer AND secret warns",
			spec: hav1.HomeAssistantSpec{Ingress: &hav1.IngressSpec{
				Enabled: true,
				TLS: &hav1.IngressTLSSpec{
					Enabled: true, SecretName: "s", IssuerRef: &hav1.IssuerReference{Name: "i"},
				},
			}},
			wantWarnings: 1,
		},
		{
			name: "gateway without host is rejected",
			spec: hav1.HomeAssistantSpec{Gateway: &hav1.GatewaySpec{
				Enabled: true, ParentRef: &hav1.GatewayParentRef{Name: "gw"},
			}},
			wantErrs: 1,
		},
		{
			name: "gateway with host and parentRef is valid",
			spec: hav1.HomeAssistantSpec{Gateway: &hav1.GatewaySpec{
				Enabled: true, Host: "ha.example.com",
				ParentRef: &hav1.GatewayParentRef{Name: "gw"},
			}},
		},
		{
			name:     "gateway enabled without attach point is rejected",
			spec:     hav1.HomeAssistantSpec{Gateway: &hav1.GatewaySpec{Enabled: true, Host: "ha.example.com"}},
			wantErrs: 1,
		},
		{
			name: "ingress tls without secret or issuer is rejected",
			spec: hav1.HomeAssistantSpec{Ingress: &hav1.IngressSpec{
				Enabled: true, TLS: &hav1.IngressTLSSpec{Enabled: true},
			}},
			wantErrs: 1,
		},
		{
			name: "invalid issuer kind is rejected",
			spec: hav1.HomeAssistantSpec{Ingress: &hav1.IngressSpec{
				Enabled: true,
				TLS: &hav1.IngressTLSSpec{
					Enabled: true, IssuerRef: &hav1.IssuerReference{Name: "i", Kind: "Bogus"},
				},
			}},
			wantErrs: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			warnings, errs := validateHomeAssistantTLS(&tc.spec)
			if len(errs) != tc.wantErrs {
				t.Fatalf("errs = %d (%v), want %d", len(errs), errs, tc.wantErrs)
			}
			if len(warnings) != tc.wantWarnings {
				t.Fatalf("warnings = %d (%v), want %d", len(warnings), warnings, tc.wantWarnings)
			}
		})
	}
}

func TestValidateGatewayFilters(t *testing.T) {
	str := func(s string) *string { return &s }
	code := func(i int) *int { return &i }

	tests := []struct {
		name     string
		filters  []hav1.HTTPRouteFilter
		wantErrs int
	}{
		{name: "no filters is valid"},
		{
			name: "unknown type is rejected",
			filters: []hav1.HTTPRouteFilter{
				{Type: "RequestMirror"},
			},
			wantErrs: 1,
		},
		{
			name: "missing sub-object for declared type is rejected",
			filters: []hav1.HTTPRouteFilter{
				{Type: "RequestHeaderModifier"},
			},
			wantErrs: 1,
		},
		{
			name: "sub-object for a different type is rejected",
			filters: []hav1.HTTPRouteFilter{
				{
					Type:                   "RequestHeaderModifier",
					ResponseHeaderModifier: &hav1.HTTPHeaderFilter{Set: []hav1.HTTPHeader{{Name: "a", Value: "b"}}},
				},
			},
			wantErrs: 2, // wrong field set + declared type's own field missing
		},
		{
			name: "empty header filter is rejected",
			filters: []hav1.HTTPRouteFilter{
				{Type: "RequestHeaderModifier", RequestHeaderModifier: &hav1.HTTPHeaderFilter{}},
			},
			wantErrs: 1,
		},
		{
			name: "valid header filter is accepted",
			filters: []hav1.HTTPRouteFilter{
				{Type: "RequestHeaderModifier", RequestHeaderModifier: &hav1.HTTPHeaderFilter{Remove: []string{"X-Debug"}}},
			},
		},
		{
			name: "empty redirect filter is rejected",
			filters: []hav1.HTTPRouteFilter{
				{Type: "RequestRedirect", RequestRedirect: &hav1.HTTPRequestRedirectFilter{}},
			},
			wantErrs: 1,
		},
		{
			name: "valid redirect filter is accepted",
			filters: []hav1.HTTPRouteFilter{
				{
					Type: "RequestRedirect",
					RequestRedirect: &hav1.HTTPRequestRedirectFilter{
						Scheme: str("https"), StatusCode: code(301),
					},
				},
			},
		},
		{
			name: "empty url rewrite filter is rejected",
			filters: []hav1.HTTPRouteFilter{
				{Type: "URLRewrite", URLRewrite: &hav1.HTTPURLRewriteFilter{}},
			},
			wantErrs: 1,
		},
		{
			name: "valid url rewrite filter is accepted",
			filters: []hav1.HTTPRouteFilter{
				{Type: "URLRewrite", URLRewrite: &hav1.HTTPURLRewriteFilter{Hostname: str("new.example.com")}},
			},
		},
		{
			name: "path modifier missing its matching field is rejected",
			filters: []hav1.HTTPRouteFilter{
				{Type: "URLRewrite", URLRewrite: &hav1.HTTPURLRewriteFilter{
					Path: &hav1.HTTPPathModifier{Type: "ReplaceFullPath"},
				}},
			},
			wantErrs: 1,
		},
		{
			name: "valid path modifier is accepted",
			filters: []hav1.HTTPRouteFilter{
				{Type: "URLRewrite", URLRewrite: &hav1.HTTPURLRewriteFilter{
					Path: &hav1.HTTPPathModifier{Type: "ReplacePrefixMatch", ReplacePrefixMatch: str("/")},
				}},
			},
		},
		{
			name: "response header modifier valid",
			filters: []hav1.HTTPRouteFilter{
				{Type: "ResponseHeaderModifier", ResponseHeaderModifier: &hav1.HTTPHeaderFilter{
					Set: []hav1.HTTPHeader{{Name: "X-Frame-Options", Value: "SAMEORIGIN"}},
				}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := &hav1.HomeAssistantSpec{Gateway: &hav1.GatewaySpec{Filters: tc.filters}}
			errs := validateGatewayFilters(spec)
			if len(errs) != tc.wantErrs {
				t.Fatalf("errs = %d (%v), want %d", len(errs), errs, tc.wantErrs)
			}
		})
	}
}

func TestValidateDevices(t *testing.T) {
	tests := []struct {
		name     string
		devices  []hav1.DevicePassthroughEntry
		wantErrs int
	}{
		{name: "no devices is valid"},
		{
			name:     "empty hostPath is rejected",
			devices:  []hav1.DevicePassthroughEntry{{HostPath: ""}},
			wantErrs: 1,
		},
		{
			name:     "relative hostPath is rejected",
			devices:  []hav1.DevicePassthroughEntry{{HostPath: "relative/path"}},
			wantErrs: 1,
		},
		{
			name:     "hostPath outside /dev is rejected",
			devices:  []hav1.DevicePassthroughEntry{{HostPath: "/etc/passwd"}},
			wantErrs: 1,
		},
		{
			name:     "hostPath with .. traversal is rejected",
			devices:  []hav1.DevicePassthroughEntry{{HostPath: "/dev/../etc/passwd"}},
			wantErrs: 1,
		},
		{
			name:     "malformed containerPath is rejected",
			devices:  []hav1.DevicePassthroughEntry{{HostPath: "/dev/ttyACM0", ContainerPath: "relative"}},
			wantErrs: 1,
		},
		{
			name: "duplicate hostPath is rejected",
			devices: []hav1.DevicePassthroughEntry{
				{HostPath: "/dev/ttyACM0"},
				{HostPath: "/dev/ttyACM0"},
			},
			// Both hostPath and its defaulted effective containerPath collide.
			wantErrs: 2,
		},
		{
			name: "two distinct valid devices are accepted",
			devices: []hav1.DevicePassthroughEntry{
				{HostPath: "/dev/ttyACM0"},
				{HostPath: "/dev/ttyACM1", ContainerPath: "/dev/zigbee"},
			},
		},
		{
			name: "duplicate explicit containerPath is rejected",
			devices: []hav1.DevicePassthroughEntry{
				{HostPath: "/dev/ttyACM0", ContainerPath: "/dev/zigbee"},
				{HostPath: "/dev/ttyACM1", ContainerPath: "/dev/zigbee"},
			},
			wantErrs: 1,
		},
		{
			name: "omitted containerPath colliding with another device's explicit containerPath is rejected",
			devices: []hav1.DevicePassthroughEntry{
				{HostPath: "/dev/ttyACM0"},
				{HostPath: "/dev/ttyACM1", ContainerPath: "/dev/ttyACM0"},
			},
			wantErrs: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := &hav1.HomeAssistantSpec{Alpha: &hav1.AlphaSpec{Devices: tc.devices}}
			errs := validateDevices(spec)
			if len(errs) != tc.wantErrs {
				t.Fatalf("errs = %d (%v), want %d", len(errs), errs, tc.wantErrs)
			}
		})
	}
}

func TestValidateAdditionalVolumes(t *testing.T) {
	validVolume := func(name string) corev1.Volume {
		return corev1.Volume{
			Name: name,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: name},
			},
		}
	}
	valid := func() *hav1.HomeAssistantSpec {
		return &hav1.HomeAssistantSpec{AdditionalVolumes: &hav1.AdditionalVolumesSpec{
			Volumes:      []corev1.Volume{validVolume("extra")},
			VolumeMounts: []corev1.VolumeMount{{Name: "extra", MountPath: "/extra"}},
		}}
	}

	tests := []struct {
		name    string
		spec    *hav1.HomeAssistantSpec
		wantErr string
	}{
		{name: "unset configuration is accepted", spec: &hav1.HomeAssistantSpec{}},
		{name: "valid volume and mount are accepted", spec: valid()},
		{
			name: "one volume may be mounted at distinct paths",
			spec: func() *hav1.HomeAssistantSpec {
				spec := valid()
				spec.AdditionalVolumes.VolumeMounts = append(spec.AdditionalVolumes.VolumeMounts,
					corev1.VolumeMount{Name: "extra", MountPath: "/extra-read-only", ReadOnly: true})
				return spec
			}(),
		},
		{
			name: "duplicate volume name identifies the second entry",
			spec: func() *hav1.HomeAssistantSpec {
				spec := valid()
				spec.AdditionalVolumes.Volumes = append(spec.AdditionalVolumes.Volumes, validVolume("extra"))
				return spec
			}(),
			wantErr: `volumes[1].name "extra" duplicates`,
		},
		{
			name: "duplicate mount path identifies the second entry",
			spec: func() *hav1.HomeAssistantSpec {
				spec := valid()
				spec.AdditionalVolumes.VolumeMounts = append(spec.AdditionalVolumes.VolumeMounts,
					corev1.VolumeMount{Name: "extra", MountPath: "/extra"})
				return spec
			}(),
			wantErr: `volumeMounts[1].mountPath "/extra" duplicates`,
		},
		{
			name: "equivalent mount paths are duplicates",
			spec: func() *hav1.HomeAssistantSpec {
				spec := valid()
				spec.AdditionalVolumes.VolumeMounts = append(spec.AdditionalVolumes.VolumeMounts,
					corev1.VolumeMount{Name: "extra", MountPath: "/extra/"})
				return spec
			}(),
			wantErr: `volumeMounts[1].mountPath "/extra" duplicates`,
		},
		{
			name: "dangling mount identifies its entry",
			spec: &hav1.HomeAssistantSpec{AdditionalVolumes: &hav1.AdditionalVolumesSpec{
				VolumeMounts: []corev1.VolumeMount{{Name: "missing", MountPath: "/extra"}},
			}},
			wantErr: `volumeMounts[0].name "missing" does not reference`,
		},
		{
			name: "volume without a source is rejected",
			spec: &hav1.HomeAssistantSpec{AdditionalVolumes: &hav1.AdditionalVolumesSpec{
				Volumes: []corev1.Volume{{Name: "extra"}},
			}},
			wantErr: "volumes[0] must define exactly one volume source, got 0",
		},
		{
			name: "volume with multiple sources is rejected",
			spec: &hav1.HomeAssistantSpec{AdditionalVolumes: &hav1.AdditionalVolumesSpec{
				Volumes: []corev1.Volume{{Name: "extra", VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{SecretName: "extra"},
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "extra"},
					},
				}}},
			}},
			wantErr: "volumes[0] must define exactly one volume source, got 2",
		},
		{
			name: "generated device volume name is reserved",
			spec: &hav1.HomeAssistantSpec{AdditionalVolumes: &hav1.AdditionalVolumesSpec{
				Volumes: []corev1.Volume{validVolume("device-12")},
			}},
			wantErr: `volumes[0].name "device-12" is reserved`,
		},
		{
			name: "operator mount path is reserved",
			spec: func() *hav1.HomeAssistantSpec {
				spec := valid()
				spec.AdditionalVolumes.VolumeMounts[0].MountPath = "/config/secrets.yaml"
				return spec
			}(),
			wantErr: `mountPath "/config/secrets.yaml" is reserved`,
		},
		{
			name: "equivalent operator mount path is reserved",
			spec: func() *hav1.HomeAssistantSpec {
				spec := valid()
				spec.AdditionalVolumes.VolumeMounts[0].MountPath = "/config/"
				return spec
			}(),
			wantErr: `mountPath "/config" is reserved`,
		},
		{
			name: "device mount path collision is rejected",
			spec: func() *hav1.HomeAssistantSpec {
				spec := valid()
				spec.Alpha = &hav1.AlphaSpec{Devices: []hav1.DevicePassthroughEntry{{HostPath: "/dev/zigbee"}}}
				spec.AdditionalVolumes.VolumeMounts[0].MountPath = "/dev/zigbee"
				return spec
			}(),
			wantErr: `mountPath "/dev/zigbee" conflicts with spec.alpha.devices[0]`,
		},
		{
			name: "equivalent device mount path collision is rejected",
			spec: func() *hav1.HomeAssistantSpec {
				spec := valid()
				spec.Alpha = &hav1.AlphaSpec{Devices: []hav1.DevicePassthroughEntry{{HostPath: "/dev/zigbee"}}}
				spec.AdditionalVolumes.VolumeMounts[0].MountPath = "/dev/zigbee/"
				return spec
			}(),
			wantErr: `mountPath "/dev/zigbee" conflicts with spec.alpha.devices[0]`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := validateAdditionalVolumes(tc.spec)
			if tc.wantErr == "" && len(errs) != 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}
			if tc.wantErr != "" && !strings.Contains(strings.Join(errs, "; "), tc.wantErr) {
				t.Fatalf("errors %v do not contain %q", errs, tc.wantErr)
			}
		})
	}

	for _, name := range []string{"config", "ha-configuration", "ha-recorder-db", "ha-secrets", "community-repositories"} {
		t.Run("reserved volume name "+name, func(t *testing.T) {
			spec := &hav1.HomeAssistantSpec{AdditionalVolumes: &hav1.AdditionalVolumesSpec{
				Volumes: []corev1.Volume{validVolume(name)},
			}}
			errs := validateAdditionalVolumes(spec)
			if !strings.Contains(strings.Join(errs, "; "), `name "`+name+`" is reserved`) {
				t.Fatalf("errors %v do not identify reserved name %q", errs, name)
			}
		})
	}

	for _, mountPath := range []string{
		"/config", "/config/configuration.yaml", "/config/recorder_db_url.yaml", "/config/secrets.yaml",
	} {
		t.Run("reserved mount path "+mountPath, func(t *testing.T) {
			spec := valid()
			spec.AdditionalVolumes.VolumeMounts[0].MountPath = mountPath
			errs := validateAdditionalVolumes(spec)
			if !strings.Contains(strings.Join(errs, "; "), `mountPath "`+mountPath+`" is reserved`) {
				t.Fatalf("errors %v do not identify reserved path %q", errs, mountPath)
			}
		})
	}
}

func TestValidateNodeSelector(t *testing.T) {
	tests := []struct {
		name         string
		nodeSelector map[string]string
		wantErrs     int
	}{
		{name: "no nodeSelector is valid"},
		{
			name:         "valid nodeSelector is accepted",
			nodeSelector: map[string]string{"ha-device-node": "zigbee"},
		},
		{
			name:         "valid prefixed key is accepted",
			nodeSelector: map[string]string{"example.com/ha-device-node": "zigbee"},
		},
		{
			name:         "key containing a space is rejected",
			nodeSelector: map[string]string{"ha device node": "zigbee"},
			wantErrs:     1,
		},
		{
			name:         "value starting with a hyphen is rejected",
			nodeSelector: map[string]string{"ha-device-node": "-zigbee"},
			wantErrs:     1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := &hav1.HomeAssistantSpec{}
			if tc.nodeSelector != nil {
				spec.Scheduling = &hav1.SchedulingSpec{NodeSelector: tc.nodeSelector}
			}
			errs := validateNodeSelector(spec)
			if len(errs) != tc.wantErrs {
				t.Fatalf("errs = %d (%v), want %d", len(errs), errs, tc.wantErrs)
			}
		})
	}
}

func TestValidatorRejectsAndAccepts(t *testing.T) {
	v := &HomeAssistantCustomValidator{}

	bad := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "home"},
		Spec: hav1.HomeAssistantSpec{Ingress: &hav1.IngressSpec{
			Enabled: true, TLS: &hav1.IngressTLSSpec{Enabled: true},
		}},
	}
	if _, err := v.ValidateCreate(context.Background(), bad); err == nil {
		t.Fatal("expected ValidateCreate to reject ingress TLS without issuer/secret")
	}

	good := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "home"},
		Spec: hav1.HomeAssistantSpec{Ingress: &hav1.IngressSpec{
			Enabled: true,
			TLS:     &hav1.IngressTLSSpec{Enabled: true, IssuerRef: &hav1.IssuerReference{Name: "i"}},
		}},
	}
	if _, err := v.ValidateCreate(context.Background(), good); err != nil {
		t.Fatalf("expected valid HomeAssistant to be accepted, got %v", err)
	}
}

func TestValidatorRejectsInvalidAdditionalVolumesOnCreateAndUpdate(t *testing.T) {
	v := &HomeAssistantCustomValidator{}
	good := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "home"},
		Spec: hav1.HomeAssistantSpec{AdditionalVolumes: &hav1.AdditionalVolumesSpec{
			Volumes: []corev1.Volume{{
				Name: "extra",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			}},
			VolumeMounts: []corev1.VolumeMount{{Name: "extra", MountPath: "/extra"}},
		}},
	}
	bad := good.DeepCopy()
	bad.Spec.AdditionalVolumes.VolumeMounts[0].Name = "missing"

	if _, err := v.ValidateCreate(context.Background(), bad); err == nil ||
		!strings.Contains(err.Error(), "volumeMounts[0]") {
		t.Fatalf("expected create to reject the invalid mount and identify its entry, got %v", err)
	}
	if _, err := v.ValidateUpdate(context.Background(), good, bad); err == nil ||
		!strings.Contains(err.Error(), "volumeMounts[0]") {
		t.Fatalf("expected update to reject the invalid mount and identify its entry, got %v", err)
	}
	if _, err := v.ValidateUpdate(context.Background(), good, good.DeepCopy()); err != nil {
		t.Fatalf("expected valid update to be accepted, got %v", err)
	}
}

func TestValidatorRejectsInvalidMetadataOnCreateAndUpdate(t *testing.T) {
	tests := []struct {
		name      string
		spec      hav1.HomeAssistantSpec
		wantField string
	}{
		{
			name:      "invalid label key",
			spec:      hav1.HomeAssistantSpec{Labels: map[string]string{"not a key": "value"}},
			wantField: "spec.labels key",
		},
		{
			name:      "invalid label value",
			spec:      hav1.HomeAssistantSpec{Labels: map[string]string{"example.com/key": "-invalid"}},
			wantField: `spec.labels["example.com/key"] value`,
		},
		{
			name:      "invalid annotation key",
			spec:      hav1.HomeAssistantSpec{Annotations: map[string]string{"not a key": "value"}},
			wantField: "spec.annotations key",
		},
	}

	v := &HomeAssistantCustomValidator{}
	good := &hav1.HomeAssistant{
		ObjectMeta: metav1.ObjectMeta{Name: "home"},
		Spec: hav1.HomeAssistantSpec{
			Labels:      map[string]string{"example.com/key": "valid-value"},
			Annotations: map[string]string{"example.com/key": "annotation values may contain spaces"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bad := &hav1.HomeAssistant{ObjectMeta: metav1.ObjectMeta{Name: "home"}, Spec: tc.spec}
			if _, err := v.ValidateCreate(context.Background(), bad); err == nil ||
				!strings.Contains(err.Error(), tc.wantField) {
				t.Fatalf("expected create to reject %s, got %v", tc.wantField, err)
			}
			if _, err := v.ValidateUpdate(context.Background(), good, bad); err == nil ||
				!strings.Contains(err.Error(), tc.wantField) {
				t.Fatalf("expected update to reject %s, got %v", tc.wantField, err)
			}
		})
	}
	if _, err := v.ValidateCreate(context.Background(), good); err != nil {
		t.Fatalf("expected valid metadata to be accepted, got %v", err)
	}
}
