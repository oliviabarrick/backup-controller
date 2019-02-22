The backup-controller is a Kubernetes operator that uses the CSI Volume Snapshot API
to manage backups of your Persistent Volumes.

The backup-controller works by allowing you to specify a backup frequency, retention
period, and whether or not you want to automatically set the PersistentVolumeClaim to
use the latest snapshot when it is created. This ensures a painless and error free
workflow for managing backups of stateful services in Kubernetes.

# Usage

To use, set the following annotations on any PersistentVolumeClaim that should be
backed up:

* `snapshot-frequency`: if set, snapshots will automatically be taken at the interval
  specified (as a duration: `1d6h30m`).
* `snapshot-retention`: if set, snapshots will automatically be deleted when they are
  older than the specified expiration (as a duration: `1d6h30m`).
* `restore-latest`: if present, when the PersistentVolumeClaim is created, the
  operator will automatically set the `dataSource` to the latest snapshot.

An example PersistentVolumeClaim:

```
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: csi-do-test-pvc
  annotations:
    restore-latest: "true"
    snapshot-frequency: 2m
    snapshot-retention: 10m
spec:
  storageClassName: do-block-storage
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 5Gi
```

The backup-controller will create a snapshot of the volume every two minutes. Any
snapshots older than ten minutes will be deleted.

To see snapshots, list them with kubectl:

```
➜  ~ kubectl get volumesnapshots
NAME                                                   AGE
csi-do-test-pvc-31fac8c7-e8b6-449c-90da-b0e8f0cd8f6d   6m
csi-do-test-pvc-3749454d-5f45-405d-b1fc-94d080f85f59   2m
csi-do-test-pvc-4d18af84-5d68-4f69-8996-f1e6fefbb724   8m
csi-do-test-pvc-e86b3ae4-2700-4b81-8e3f-30832f7076b9   4m
csi-do-test-pvc-eb9d99af-95cc-4fc2-a0af-70fe29b02729   6s
➜  ~ 
```

If a `dataSource` is set on a PersistentVolumeClaim, the PersistentVolumeClaim is loaded from a snapshot managed by the CSI provisioner. Currently,
the `dataSource` setting on PersistentVolumeClaims is immutable, which means that the `dataSource` setting on a PersistentVolumeClaim will either be
empty (a new PVC) or the original snapshot that the PVC was created from.

Because `restore-latest` is set, if the PersistentVolumeClaim is deleted and recreated
it will automatically have the `dataSource` field set to the most recently created
snapshot:

```
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: csi-do-test-pvc
  annotations:
    restore-latest: "true"
    snapshot-frequency: 2m
    snapshot-retention: 10m
spec:
  storageClassName: do-block-storage
  accessModes:
    - ReadWriteOnce
  dataSource:
    name: csi-do-test-pvc-eb9d99af-95cc-4fc2-a0af-70fe29b02729
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
  resources:
    requests:
      storage: 5Gi
```

This feature ensures that your PersistentVolumeClaims can always be restored from the
most recent snapshot without any modification to the manifest (e.g., the latest
snapshot id does not have to be present).

Combined with [Flux](https://github.com/weaveworks/flux) any PersistentVolumeClaim
that is deleted will automatically be restored from the most recent snapshot.

To fully ensure that your snapshots are safe, also ensure that your VolumeSnapshots
and VolumeSnapshotContents are backed up.
