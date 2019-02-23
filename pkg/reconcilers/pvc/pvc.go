package pvc

import (
	"context"
	"github.com/justinbarrick/backup-controller/pkg/runtime"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	runtimeObj "k8s.io/apimachinery/pkg/runtime"
	"log"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Reconciler for reacting to PersistentVolumeClaim events.
type Reconciler struct {
	client  client.Client
	runtime *runtime.Runtime
}

// Returns the type that the reconciler watches.
func (r *Reconciler) GetType() []runtimeObj.Object {
	return []runtimeObj.Object{ &corev1.PersistentVolumeClaim{}, }
}

// Set the Kubernetes client.
func (r *Reconciler) SetClient(client client.Client) {
	r.client = client
}

// Set the backup-controller runtime.
func (r *Reconciler) SetRuntime(runtime *runtime.Runtime) {
	r.runtime = runtime
}

// Reconcile PersistentVolumeClaims by updating the backups map with information about the PVC.
func (r *Reconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
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

	backup := r.runtime.Get(pvc.GetNamespace(), pvc.GetName())
	backup.SetPersistentVolumeClaim(pvc)
	backup.Schedule()

	return reconcile.Result{}, nil
}
