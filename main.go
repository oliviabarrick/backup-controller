package main

import (
	"sync"
	"fmt"
	"time"
	"context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	snapshots "github.com/kubernetes-csi/external-snapshotter/pkg/apis/volumesnapshot/v1alpha1"
	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	"log"
	"net/http"
	"k8s.io/apimachinery/pkg/api/errors"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/runtime/signals"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission/builder"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission/types"
	"github.com/google/uuid"
)

const (
	annotationKey    = "latest-snapshot"
	webhookNamespace = "snapshot-webhook"
	webhookName      = "snapshot-webhook"
)

// Represents the backup schedule for a single PVC and invokes a timer accordingly.
type BackupController struct {
	name string
	namespace string
	volumeCreated time.Time
	latest time.Time
	latestId string
	interval *time.Duration
	retentionPeriod *time.Duration
	timer *time.Timer
	client client.Client
}

// Create a new VolumeSnapshot for a PVC.
func (b *BackupController) Backup() {
	log.Printf("It is time for a backup of %s!", b.name)

	randId, err := uuid.NewRandom()
	if err != nil {
		log.Println("random error", err)
		return
	}

	err = b.client.Create(context.TODO(), &snapshots.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-%s", b.name, randId.String()),
			Namespace: b.namespace,
		},
		Spec: snapshots.VolumeSnapshotSpec{
			Source: &corev1.TypedLocalObjectReference{
				Kind: "PersistentVolumeClaim",
				Name: b.name,
			},
		},
	})
	if err != nil {
		log.Println("error creating VolumeSnapshot:", err)
		return
	}

	if err := b.GarbageCollectSnapshots(); err != nil {
		log.Println("error garbage collecting snapshots:", err)
		return
	}
}

// Delete any snapshots older than the retention period specified.
func (b *BackupController) GarbageCollectSnapshots() error {
	if b.retentionPeriod == nil {
		return nil
	}

	allSnapshots := &snapshots.VolumeSnapshotList{}

	err := b.client.List(context.TODO(), &client.ListOptions{
		Namespace: b.namespace,
	}, allSnapshots)
	if err != nil {
		return err
	}

	for _, snapshot := range allSnapshots.Items {
		snapExpiry := snapshot.ObjectMeta.CreationTimestamp.Time.Add(*b.retentionPeriod)

		if time.Now().Sub(snapExpiry).Seconds() < 0 {
			continue
		}

		err = b.client.Delete(context.TODO(), &snapshot)
		if err != nil {
			return err
		}
	}

	return nil
}

// Schedule a backup for a PVC.
func (b *BackupController) Schedule() {
	if b.interval == nil {
		return
	}

	if b.timer != nil {
		b.timer.Stop()
	}

	checkTime := b.volumeCreated

	if b.latest != (time.Time{}) {
		checkTime = b.latest
	}

	nextBackup := checkTime.Add(*b.interval).Sub(time.Now())

	b.timer = time.AfterFunc(nextBackup, b.Backup)

	log.Printf("Backup for %s scheduled for %s", b.name, nextBackup)
}

// If t is more recent than the current latest, set latest to t.
func (b *BackupController) SetLatest(t time.Time, snapshotId string) bool {
	if b.latest == (time.Time{}) {
		b.latest = t
		b.latestId = snapshotId
		return true
	}

	if t.Sub(b.latest).Seconds() > 0 {
		b.latest = t
		b.latestId = snapshotId
		return true
	}

	return false
}

// Set the interval and volumeCreated settings from the PVC.
func (b *BackupController) SetPersistentVolumeClaim(pvc *corev1.PersistentVolumeClaim) error {
	b.volumeCreated = pvc.ObjectMeta.CreationTimestamp.Time

	annotations := pvc.GetAnnotations()
	if annotations == nil || annotations["snapshot-frequency"] == "" {
		return nil
	}

	snapshotFrequency, err := time.ParseDuration(annotations["snapshot-frequency"])
	if err != nil {
		return err
	}

	b.interval = &snapshotFrequency

	if annotations["snapshot-retention"] == "" {
		return nil
	}

	snapshotRetention, err := time.ParseDuration(annotations["snapshot-retention"])
	if err != nil {
		return err
	}

	b.retentionPeriod = &snapshotRetention
	return nil
}

// A map and lock for controlling access to BackupControllers.
type BackupManager struct {
	backups map[string]*BackupController
	lock sync.Mutex
	client client.Client
}

// Retrieve a BackupController by key. If it does not exist, it will be initialized.
func (b *BackupManager) Get(namespace, name string) *BackupController {
	b.lock.Lock()
	defer b.lock.Unlock()

	if b.backups == nil {
		b.backups = map[string]*BackupController{}
	}

	key := fmt.Sprintf("%s/%s", namespace, name)

	if b.backups[key] == nil {
		b.backups[key] = &BackupController{
			name: name,
			namespace: namespace,
			client: b.client,
		}
	}

	return b.backups[key]
}

// Reconciler for reacting to VolumeSnapshot events.
type ReconcileVolumeSnapshots struct {
	client client.Client
	backups *BackupManager
}

// Reconcile VolumeSnapshot events by updating the backups map with known snapshots.
func (r *ReconcileVolumeSnapshots) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	log.Println("Reconciling snapshot:", request.NamespacedName)
	snapshot := &snapshots.VolumeSnapshot{}

	err := r.client.Get(context.TODO(), request.NamespacedName, snapshot)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Println("Snapshot not found:", request.NamespacedName)
			return reconcile.Result{}, nil
		}

		return reconcile.Result{}, err
	}

	backup := r.backups.Get(snapshot.GetNamespace(), snapshot.Spec.Source.Name)
	isLatest := backup.SetLatest(snapshot.ObjectMeta.CreationTimestamp.Time, snapshot.GetName())
	backup.Schedule()

	if isLatest {
		pvc := &corev1.PersistentVolumeClaim{}
		if err := r.client.Get(context.TODO(), apitypes.NamespacedName{
			Name: snapshot.Spec.Source.Name,
			Namespace: snapshot.GetNamespace(),
		}, pvc); err != nil {
			if errors.IsNotFound(err) {
				return reconcile.Result{}, nil
			}

			return reconcile.Result{}, err
		}

		if pvc.ObjectMeta.Annotations == nil {
			return reconcile.Result{}, nil
		}

		pvc.ObjectMeta.Annotations[annotationKey] = snapshot.GetName()

		log.Println("Updating latest-snapshot annotation.")

		if err := r.client.Update(context.TODO(), pvc); err != nil {
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
}

// Reconciler for reacting to PersistentVolumeClaim events.
type ReconcilePersistentVolumeClaims struct {
	client client.Client
	backups *BackupManager
}

// Reconcile PersistentVolumeClaims by updating the backups map with information about the PVC.
func (r *ReconcilePersistentVolumeClaims) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	log.Println("Reconciling PVC:", request.NamespacedName)
	pvc := &corev1.PersistentVolumeClaim{}

	err := r.client.Get(context.TODO(), request.NamespacedName, pvc)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Println("PVC not found:", request.NamespacedName)
			return reconcile.Result{}, nil
		}

		return reconcile.Result{}, err
	}

	backup := r.backups.Get(pvc.GetNamespace(), pvc.GetName())
	backup.SetPersistentVolumeClaim(pvc)
	backup.Schedule()

	return reconcile.Result{}, nil
}

// Kubernetes validating webhook for updating PersistentVolumeClaims with a reference to the most recent backup taken.
type LatestSnapshotMutator struct {
	client  client.Client
	decoder types.Decoder
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

// Set the DataSource on a persistent volume claim if the latest-snapshot key is present.
func (v *LatestSnapshotMutator) MutatePvc(pvc *corev1.PersistentVolumeClaim) {
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

func main() {
	scheme := runtime.NewScheme()
	snapshots.AddToScheme(scheme)
	corev1.AddToScheme(scheme)
	admissionregistrationv1beta1.AddToScheme(scheme)

	mgr, err := manager.New(config.GetConfigOrDie(), manager.Options{
		Scheme: scheme,
	})
	if err != nil {
		log.Fatal("cannot create manager:", err)
	}

	backups := &BackupManager{client: mgr.GetClient()}

	snapshotController, err := controller.New("snapshot-controller", mgr, controller.Options{
		Reconciler: &ReconcileVolumeSnapshots{client: mgr.GetClient(), backups: backups},
	})
	if err != nil {
		log.Fatal("cannot create snapshot controller:", err)
	}

	if err := snapshotController.Watch(&source.Kind{Type: &snapshots.VolumeSnapshot{}}, &handler.EnqueueRequestForObject{}); err != nil {
		log.Fatal("cannot watch VolumeSnapshots:", err)
	}

	pvcController, err := controller.New("pvc-controller", mgr, controller.Options{
		Reconciler: &ReconcilePersistentVolumeClaims{client: mgr.GetClient(), backups: backups},
	})
	if err != nil {
		log.Fatal("cannot create PVC controller:", err)
	}

	if err := pvcController.Watch(&source.Kind{Type: &corev1.PersistentVolumeClaim{}}, &handler.EnqueueRequestForObject{}); err != nil {
		log.Fatal("cannot watch PVCs:", err)
	}

	mutatingWebhook, err := builder.NewWebhookBuilder().
		Mutating().
		WithManager(mgr).
		Path("/mutate").
		ForType(&corev1.PersistentVolumeClaim{}).
		Handlers(&LatestSnapshotMutator{}).
		Build()
	if err != nil {
		log.Fatal("cannot create webhook:", err)
	}

	as, err := webhook.NewServer(webhookName, mgr, webhook.ServerOptions{
		Port:    8443,
		CertDir: "/tmp/cert",
	})
	if err != nil {
		log.Fatal("cannot start webhook server:", err)
	}

	if err := as.Register(mutatingWebhook); err != nil {
		log.Fatal("cannot register webhook", err)
	}

	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		log.Fatal("cannot start manager:", err)
	}
}
