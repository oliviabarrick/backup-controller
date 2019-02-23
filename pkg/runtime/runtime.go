package runtime

import (
	"context"
	"github.com/justinbarrick/backup-controller/pkg/backup_controller"
	snapshots "github.com/kubernetes-csi/external-snapshotter/pkg/apis/volumesnapshot/v1alpha1"

	"fmt"
	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/runtime/signals"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission/builder"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission/types"
)

// Interface that must be implemented by a reconciler.
type Reconciler interface {
	SetClient(client.Client)
	SetRuntime(*Runtime)
	GetType() []runtime.Object
	Reconcile(request reconcile.Request) (reconcile.Result, error)
}

// Interface that must be implemented by a webhook.
type Webhook interface {
	Handle(ctx context.Context, req types.Request) types.Response
	InjectClient(c client.Client) error
	InjectDecoder(d types.Decoder) error
	SetRuntime(*Runtime)
}

// An object that contains information about all known PersistentVolumeClaims, a Kubernetes client,
// and other globally useful resources.
type Runtime struct {
	Name      string
	Namespace string
	mgr       manager.Manager
	backups   map[string]*backup_controller.BackupController
	lock      sync.Mutex
	client    client.Client
}

// Create a new runtime.
func NewRuntime(name, namespace string) (*Runtime, error) {
	scheme := runtime.NewScheme()
	snapshots.AddToScheme(scheme)
	corev1.AddToScheme(scheme)
	admissionregistrationv1beta1.AddToScheme(scheme)

	mgr, err := manager.New(config.GetConfigOrDie(), manager.Options{
		Scheme: scheme,
	})
	if err != nil {
		return nil, err
	}

	return &Runtime{
		Name:      name,
		Namespace: namespace,
		client:    mgr.GetClient(),
		mgr:       mgr,
	}, nil
}

// Start the cnotroller.
func (b *Runtime) Start() error {
	return b.mgr.Start(signals.SetupSignalHandler())
}

// Retrieve a BackupController by key. If it does not exist, it will be initialized.
func (b *Runtime) Get(namespace, name string) *backup_controller.BackupController {
	b.lock.Lock()
	defer b.lock.Unlock()

	if b.backups == nil {
		b.backups = map[string]*backup_controller.BackupController{}
	}

	key := fmt.Sprintf("%s/%s", namespace, name)

	if b.backups[key] == nil {
		b.backups[key] = &backup_controller.BackupController{
			Name:      name,
			Namespace: namespace,
		}
		b.backups[key].SetClient(b.client)
	}

	return b.backups[key]
}

// Registers a new mutating webhook.
func (b *Runtime) RegisterWebhook(handler Webhook) error {
	handler.SetRuntime(b)

	mutatingWebhook, err := builder.NewWebhookBuilder().
		Mutating().
		Path(fmt.Sprintf("/mutate-%s", b.Name)).
		Name(fmt.Sprintf("%s.codesink.net", b.Name)).
		ForType(&corev1.PersistentVolumeClaim{}).
		FailurePolicy(admissionregistrationv1beta1.Fail).
		Operations(admissionregistrationv1beta1.Create).
		Handlers(handler).
		WithManager(b.mgr).
		Build()
	if err != nil {
		return err
	}

	as, err := webhook.NewServer(b.Name, b.mgr, webhook.ServerOptions{
		BootstrapOptions: &webhook.BootstrapOptions{
			Service: &webhook.Service{
				Name:      b.Name,
				Namespace: b.Namespace,
				Selectors: map[string]string{
					"app": b.Name,
				},
			},
		},
		Port:    8443,
		CertDir: fmt.Sprintf("/tmp/cert-%s", b.Name),
	})
	if err != nil {
		return err
	}

	return as.Register(mutatingWebhook)
}

// Registers a new controller.
func (b *Runtime) RegisterController(name string, reconciler Reconciler) error {
	reconciler.SetRuntime(b)
	reconciler.SetClient(b.GetClient())

	ctrlr, err := controller.New(fmt.Sprintf("%s-controller", name), b.mgr, controller.Options{
		Reconciler: reconciler,
	})
	if err != nil {
		return err
	}

	for _, kind := range reconciler.GetType() {
		if err := ctrlr.Watch(&source.Kind{
			Type: kind,
		}, &handler.EnqueueRequestForObject{}); err != nil {
			return err
		}
	}

	return nil
}

// Return the Kubernetes client for the runtime.
func (b *Runtime) GetClient() client.Client {
	return b.mgr.GetClient()
}
