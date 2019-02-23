package snapshot

import (
	"context"
	"github.com/justinbarrick/backup-controller/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	"log"
	"net/http"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission/types"
)

const (
	annotationKey = "restore-latest"
)

// Kubernetes validating webhook for updating PersistentVolumeClaims with a reference to the most recent backup taken.
type LatestSnapshotMutator struct {
	client  client.Client
	decoder types.Decoder
	runtime *runtime.Runtime
}

// Implement InjectClient
func (v *LatestSnapshotMutator) InjectClient(c client.Client) error {
	v.client = c
	return nil
}

// Implement InjectDecoder
func (v *LatestSnapshotMutator) InjectDecoder(d types.Decoder) error {
	v.decoder = d
	return nil
}

// Set the DataSource on a persistent volume claim if the restore-latest key is present.
func (v *LatestSnapshotMutator) MutatePvc(pvc *corev1.PersistentVolumeClaim) {
	annotations := pvc.ObjectMeta.GetAnnotations()
	if annotations == nil || annotations[annotationKey] == "" {
		return
	}

	backup := v.runtime.Get(pvc.GetNamespace(), pvc.GetName())
	if backup.GetLatest() == "" {
		log.Println("Not restoring PVC, no latest backup.")
		return
	}

	log.Println("Restoring PVC from", backup.GetLatest())

	apiGroup := "snapshot.storage.k8s.io"

	pvc.Spec.DataSource = &corev1.TypedLocalObjectReference{
		APIGroup: &apiGroup,
		Kind:     "VolumeSnapshot",
		Name:     backup.GetLatest(),
	}
}

// Handle an incoming webhook.
func (v *LatestSnapshotMutator) Handle(ctx context.Context, req types.Request) types.Response {
	pvc := &corev1.PersistentVolumeClaim{}

	err := v.decoder.Decode(req, pvc)
	if err != nil {
		return admission.ErrorResponse(http.StatusBadRequest, err)
	}

	newPvc := pvc.DeepCopy()
	v.MutatePvc(newPvc)

	return admission.PatchResponse(pvc, newPvc)
}

// Set the backup-controller runtime for the webhook.
func (v *LatestSnapshotMutator) SetRuntime(runtime *runtime.Runtime) {
	v.runtime = runtime
}
