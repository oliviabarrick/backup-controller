A Kubernetes mutating webhook whose job is to makes it easier to integrate a backup workflow using GitOps and the Volume Snapshot API.

If a `dataSource` is set on a PersistentVolumeClaim, the PersistentVolumeClaim is loaded from a snapshot managed by the CSI provisioner. Currently,
the `dataSource` setting on PersistentVolumeClaims is immutable, which means that the `dataSource` setting on a PersistentVolumeClaim will either be
empty (a new PVC) or the original snapshot that the PVC was created from.

This presents a challenge for a GitOps workflow: how can I ensure that the PVCs that I track in Git always reference the most up-to-date backup to aid
in a disaster recovery scenario without disturbing my existing PVCs?

That's where the snapshot-admission-controller comes in. The snapshot-admission-controller is a mutating webhook that will set the `dataSource` field on
a PersistentVolumeClaim to the snapshot referenced in its `snapshot-datasource` annotation. Since the webhook is only invoked when a PersistentVolumeClaim
is created and not when it is updated, this annotation can always be set to the most recent snapshot.

An imagined GitOps workflow is:

1. Checkout the git repository with Kubernetes manifests.
2. Create a VolumeSnapshot for each PersistentVolumeClaim.
3. Update the PersistentVolumeClaim's `snapshot-datasource` annotation to the name of the most recent snapshot.
3. Commit and push the VolumeSnapshot, PersistentVolumeClaim, and created VolumeSnapshotContents.

This can be combined with Flux to ensure that volumes can easily be restored in a disaster.

# Installation

To setup, apply the manifest:

```
kubectl apply -f deploy.yaml
```

# Usage

To use, create a PersistentVolumeClaim:

```
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-claim
  namespace: default
spec:
  accessModes:
  - ReadWriteOnce
  resources:
    requests:
      storage: 5Gi
```

Now create a snapshot:

```
apiVersion: snapshot.storage.k8s.io/v1alpha1
kind: VolumeSnapshot
metadata:
  name: my-snapshot 
spec:
  source:
    name: my-claim
    kind: PersistentVolumeClaim
```

Now, update the PersistentVolumeClaim's data source annotation:

```
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-claim
  namespace: default
  annotation:
    snapshot-datasource: snapshot.storage.k8s.io/VolumeSnapshot/my-snapshot
spec:
  accessModes:
  - ReadWriteOnce
  resources:
    requests:
      storage: 5Gi
```

At first, this will have no effect. But, if you delete and create the PVC, it will be restored from the snapshot.

# Credits

Code based on the [kube-mutating-webhook-tutorial](https://github.com/morvencao/kube-mutating-webhook-tutorial)
