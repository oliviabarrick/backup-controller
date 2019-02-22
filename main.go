package main

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	"log"
	"net/http"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/runtime/signals"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission/builder"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission/types"
)

const (
	annotationKey    = "latest-snapshot"
	webhookNamespace = "snapshot-webhook"
	webhookName      = "snapshot-webhook"
)

type latestSnapshotMutator struct {
	client  client.Client
	decoder types.Decoder
}

func (v *latestSnapshotMutator) InjectClient(c client.Client) error {
	v.client = c
	return nil
}

func (v *latestSnapshotMutator) InjectDecoder(d types.Decoder) error {
	v.decoder = d
	return nil
}

func (v *latestSnapshotMutator) mutatePvc(pvc *corev1.PersistentVolumeClaim) {
	annotations := pvc.ObjectMeta.GetAnnotations()
	if annotations == nil || annotations[annotationKey] == "" {
		return
	}

	apiGroup := "snapshot.storage.k8s.io"

	pvc.Spec.DataSource = &corev1.TypedLocalObjectReference{
		APIGroup: &apiGroup,
		Kind:     "VolumeSnapshot",
		Name:     annotations[annotationKey],
	}
}

func (v *latestSnapshotMutator) Handle(ctx context.Context, req types.Request) types.Response {
	pvc := &corev1.PersistentVolumeClaim{}

	err := v.decoder.Decode(req, pvc)
	if err != nil {
		return admission.ErrorResponse(http.StatusBadRequest, err)
	}

	newPvc := pvc.DeepCopy()
	v.mutatePvc(newPvc)

	return admission.PatchResponse(pvc, newPvc)
}

func main() {
	mgr, err := manager.New(config.GetConfigOrDie(), manager.Options{})
	if err != nil {
		log.Fatal(err)
	}

	mutatingWebhook, err := builder.NewWebhookBuilder().
		Mutating().
		WithManager(mgr).
		Path("/mutate").
		ForType(&corev1.PersistentVolumeClaim{}).
		Handlers(&latestSnapshotMutator{}).
		Build()
	if err != nil {
		log.Fatal(err)
	}

	as, err := webhook.NewServer(webhookName, mgr, webhook.ServerOptions{
		Port:    8443,
		CertDir: "/tmp/cert",
	})
	if err != nil {
		log.Fatal(err)
	}

	err = as.Register(mutatingWebhook)
	if err != nil {
		log.Fatal(err)
	}

	err = mgr.Start(signals.SetupSignalHandler())
	if err != nil {
		log.Fatal(err)
	}
}
