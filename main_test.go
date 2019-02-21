package main

import (
	"encoding/json"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"gopkg.in/evanphx/json-patch.v4"
	"k8s.io/apimachinery/pkg/api/resource"
	"github.com/stretchr/testify/assert"
	"testing"
)

func createPvc() corev1.PersistentVolumeClaim {
	store := "My-Store"

	return corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "hello",
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

func applyPatch(pvc corev1.PersistentVolumeClaim, patchJson []byte) (corev1.PersistentVolumeClaim, error) {
	patched := corev1.PersistentVolumeClaim{}

	pvcEncoded, err := json.Marshal(pvc)
	if err != nil {
		return patched, err
	}

	patch, err := jsonpatch.DecodePatch(patchJson)
	if err != nil {
		return patched, err
	}

	pvcPatched, err := patch.Apply(pvcEncoded)
	if err != nil {
		return patched, err
	}

	return patched, json.Unmarshal(pvcPatched, &patched)
}

func TestCreatePatch(t *testing.T) {
	pvc := createPvc()
	pvc.ObjectMeta.Annotations[annotationKey] = "test"

	patchJson, err := createPatch(pvc)
	assert.Nil(t, err)

	patched, err := applyPatch(pvc, patchJson)
	assert.Nil(t, err)

	assert.Equal(t, "snapshot.storage.k8s.io", *patched.Spec.DataSource.APIGroup)
	assert.Equal(t, "VolumeSnapshot", patched.Spec.DataSource.Kind)
	assert.Equal(t, "test", patched.Spec.DataSource.Name)
}

func TestCreatePatchNoAnnotation(t *testing.T) {
	pvc := createPvc()

	patchJson, err := createPatch(pvc)
	assert.Nil(t, err)

	patched, err := applyPatch(pvc, patchJson)
	assert.Nil(t, err)
	assert.Nil(t, patched.Spec.DataSource)
}
