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

// Represents the backup schedule for a single PVC and invokes a timer accordingly.
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

func (b *BackupController) SetClient(client client.Client) {
	b.client = client
}

// Create a new VolumeSnapshot for a PVC.
func (b *BackupController) Backup() {
	log.Printf("It is time for a backup of %s!", b.Name)

	randId, err := uuid.NewRandom()
	if err != nil {
		log.Println("random error", err)
		return
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

	log.Printf("Backup for %s scheduled for %s", b.Name, nextBackup)
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

// If t is more recent than the current latest, set latest to t.
func (b *BackupController) GetLatest() string {
	return b.latestId
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
