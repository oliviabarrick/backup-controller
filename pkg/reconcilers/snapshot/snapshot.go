package snapshot

import (
	"context"
	"github.com/justinbarrick/backup-controller/pkg/runtime"
	snapshots "github.com/kubernetes-csi/external-snapshotter/pkg/apis/volumesnapshot/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	runtimeObj "k8s.io/apimachinery/pkg/runtime"
	"log"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Reconciler for reacting to VolumeSnapshot events.
type Reconciler struct {
	client  client.Client
	runtime *runtime.Runtime
}

func (r *Reconciler) GetType() runtimeObj.Object {
	return &snapshots.VolumeSnapshot{}
}

func (r *Reconciler) SetClient(client client.Client) {
	r.client = client
}

func (r *Reconciler) SetRuntime(runtime *runtime.Runtime) {
	r.runtime = runtime
}

// Reconcile VolumeSnapshot events by updating the backups map with known snapshots.
func (r *Reconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
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

	backup := r.runtime.Get(snapshot.GetNamespace(), snapshot.Spec.Source.Name)
	backup.SetLatest(snapshot.ObjectMeta.CreationTimestamp.Time, snapshot.GetName())
	backup.Schedule()

	return reconcile.Result{}, nil
}
