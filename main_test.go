package main

import (
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func createPvc() *corev1.PersistentVolumeClaim {
	store := "My-Store"

	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "hello",
			Annotations: map[string]string{},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("5Gi"),
				},
			},
			StorageClassName: &store,
		},
	}
}

func TestCreatePatch(t *testing.T) {
	pvc := createPvc()
	pvc.ObjectMeta.Annotations[annotationKey] = "test"

	mutator := &latestSnapshotMutator{}
	mutator.mutatePvc(pvc)

	assert.Equal(t, "snapshot.storage.k8s.io", *pvc.Spec.DataSource.APIGroup)
	assert.Equal(t, "VolumeSnapshot", pvc.Spec.DataSource.Kind)
	assert.Equal(t, "test", pvc.Spec.DataSource.Name)
}

func TestCreatePatchNoAnnotation(t *testing.T) {
	pvc := createPvc()

	mutator := &latestSnapshotMutator{}
	mutator.mutatePvc(pvc)

	assert.Nil(t, pvc.Spec.DataSource)
}
