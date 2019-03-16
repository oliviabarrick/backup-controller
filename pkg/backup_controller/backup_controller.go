package backup_controller

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	snapshots "github.com/kubernetes-csi/external-snapshotter/pkg/apis/volumesnapshot/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"log"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

// Controls the backups for a single persistent volume claim. It keeps track of the latest snapshot,
// when to expire snapshots, and when to take snaphots.
type BackupController struct {
	Name            string
	Namespace       string
	volumeCreated   time.Time
	latest          time.Time
	latestId        string
	interval        *time.Duration
	retentionPeriod *time.Duration
	timer           *time.Timer
	client          client.Client
}

// Allow passing a Kubernetes client to the BackupController.
func (b *BackupController) SetClient(client client.Client) {
	b.client = client
}

// Create a new VolumeSnapshot for a PVC.
func (b *BackupController) Backup() error {
	needsSnapshot, err := b.NeedsSnapshot()
	if err != nil {
		return err
	}

	if !needsSnapshot {
		return nil
	}

	log.Printf("It is time for a backup of %s!", b.Name)

	randId, err := uuid.NewRandom()
	if err != nil {
		log.Println("random error", err)
		return err
	}

	err = b.client.Create(context.TODO(), &snapshots.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s", b.Name, randId.String()),
			Namespace: b.Namespace,
		},
		Spec: snapshots.VolumeSnapshotSpec{
			Source: &corev1.TypedLocalObjectReference{
				Kind: "PersistentVolumeClaim",
				Name: b.Name,
			},
		},
	})
	if err != nil {
		log.Println("error creating VolumeSnapshot:", err)
		return err
	}

	if err := b.GarbageCollectSnapshots(); err != nil {
		log.Println("error garbage collecting snapshots:", err)
		return err
	}

	return nil
}

// Delete any snapshots older than the retention period specified.
func (b *BackupController) GarbageCollectSnapshots() error {
	if b.retentionPeriod == nil {
		return nil
	}

	allSnapshots := &snapshots.VolumeSnapshotList{}

	err := b.client.List(context.TODO(), &client.ListOptions{
		Namespace: b.Namespace,
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

func (b *BackupController) GetLatest() (*snapshots.VolumeSnapshot, error) {
	allSnapshots := &snapshots.VolumeSnapshotList{}

	err := b.client.List(context.TODO(), &client.ListOptions{
		Namespace: b.Namespace,
	}, allSnapshots)
	if err != nil {
		return nil, err
	}

	mostRecentSnapshot := snapshots.VolumeSnapshot{}
	gotSnapshot := false

	for _, snapshot := range allSnapshots.Items {
		creationTime := snapshot.ObjectMeta.CreationTimestamp.Time
		mostRecentTime := mostRecentSnapshot.ObjectMeta.CreationTimestamp.Time

		if creationTime.Before(mostRecentTime) {
			continue
		}

		if snapshot.Spec.Source.Name != b.Name {
			continue
		}

		gotSnapshot = true
		mostRecentSnapshot = snapshot
	}

	if gotSnapshot {
		return &mostRecentSnapshot, nil
	} else {
		return nil, nil
	}
}

// Return true if this is a PVC that does get backups.
func (b *BackupController) HasSnapshotSchedule() bool {
	return b.interval != nil
}

// Delete any snapshots older than the retention period specified.
func (b *BackupController) NeedsSnapshot() (bool, error) {
	if !b.HasSnapshotSchedule() {
		return false, nil
	}

	mostRecentSnapshot, err := b.GetLatest()
	if err != nil {
		return false, err
	}

	mostRecentTime := b.volumeCreated
	if mostRecentSnapshot != nil {
		mostRecentSnapshotTime := mostRecentSnapshot.ObjectMeta.CreationTimestamp.Time

		if mostRecentSnapshotTime.After(mostRecentTime) {
			mostRecentTime = mostRecentSnapshotTime
		}
	}

	return mostRecentTime.Add(*b.interval).Before(time.Now()), nil
}

// Set the PersistentVolumeClaim that this BackupController is for.
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
