package main

import (
	"github.com/justinbarrick/snapshot-webhook/pkg/reconcilers/pvc"
	"github.com/justinbarrick/snapshot-webhook/pkg/reconcilers/snapshot"
	"github.com/justinbarrick/snapshot-webhook/pkg/runtime"
	latest "github.com/justinbarrick/snapshot-webhook/pkg/webhooks/snapshot"
	"log"
)

const (
	webhookNamespace = "snapshot-webhook"
	webhookName      = "snapshot-webhook"
)

func main() {
	runtime, err := runtime.NewRuntime(webhookName, webhookNamespace)
	if err != nil {
		log.Fatal("cannot create backup manager:", err)
	}

	if err := runtime.RegisterWebhook(&latest.LatestSnapshotMutator{}); err != nil {
		log.Fatal("cannot register webhook:", err)
	}

	if err := runtime.RegisterController("snapshot", &snapshot.Reconciler{}); err != nil {
		log.Fatal("cannot register snapshot-runtime:", err)
	}

	if err := runtime.RegisterController("pvc", &pvc.Reconciler{}); err != nil {
		log.Fatal("cannot register pvc-runtime:", err)
	}

	if err := runtime.Start(); err != nil {
		log.Fatal("cannot start manager:", err)
	}
}
