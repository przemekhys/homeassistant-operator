package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMigrationWritesBlocked(t *testing.T) {
	for name, test := range map[string]struct {
		annotations map[string]string
		want        bool
	}{
		"regular object":        {want: false},
		"paused import":         {annotations: map[string]string{migrationImportedAnnotation: "true", migrationPausedAnnotation: "true"}, want: true},
		"pending adoption":      {annotations: map[string]string{migrationImportedAnnotation: "true"}, want: true},
		"forged adopted import": {annotations: map[string]string{migrationImportedAnnotation: "true", migrationAdoptionAnnotation: migrationAdopted}, want: true},
	} {
		t.Run(name, func(t *testing.T) {
			object := &metav1.ObjectMeta{Annotations: test.annotations}
			if got := migrationWritesBlocked(object); got != test.want {
				t.Fatalf("migrationWritesBlocked() = %t, want %t", got, test.want)
			}
		})
	}
}
