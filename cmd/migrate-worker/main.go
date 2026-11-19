// migrate-worker is a short-lived worker for an accepted, marked request.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	ha "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"github.com/przemekhys/homeassistant-operator/internal/migration"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "migration worker blocked:", err)
		os.Exit(1)
	}
}

func run() error {
	namespace := flag.String("namespace", "", "migration namespace")
	name := flag.String("migration", "", "migration name")
	stage := flag.String("stage", "", "accepted stage")
	id := flag.String("request-id", "", "accepted request ID")
	mode := flag.String("mode", "dispatch-backup", "dispatch-backup, observe-backup, dispatch-onboarding, or onboarding-proxy")
	flag.Parse()
	if *mode == "onboarding-proxy" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return runOnboardingProxy(ctx)
	}
	if *namespace == "" || *name == "" || *stage == "" || *id == "" {
		return fmt.Errorf("missing worker identity")
	}
	if *mode != "dispatch-backup" && *mode != "observe-backup" && *mode != "dispatch-onboarding" {
		return fmt.Errorf("unsupported worker mode")
	}
	scheme := runtime.NewScheme()
	if err := ha.AddToScheme(scheme); err != nil {
		return fmt.Errorf("worker scheme unavailable")
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("worker scheme unavailable")
	}
	cfg, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("kubernetes configuration unavailable")
	}
	kube, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("kubernetes client unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Minute)
	defer cancel()
	request, err := migration.ReadWorkerRequest(ctx, kube, *namespace, *name, *stage, *id)
	if err != nil {
		return fmt.Errorf("request unavailable or invalid")
	}
	if *mode == "dispatch-onboarding" {
		if request.Stage != "upload" && request.Stage != "restore" {
			return fmt.Errorf("onboarding worker stage unavailable")
		}
		onboarding, err := migrationOnboardingClient()
		if err != nil {
			return err
		}
		return migration.RunOnboardingWorker(ctx, kube, scheme, *namespace, *name, *stage, *id, onboarding)
	}
	if request.Stage != "backup" {
		return fmt.Errorf("stage executor is not yet available")
	}
	source := haclient.NewClient(request.SourceEndpoint)
	if *mode == "dispatch-backup" {
		if err := migration.RunBackupWorker(ctx, kube, scheme, *namespace, *name, *id, source); err != nil {
			return fmt.Errorf("backup dispatch blocked or outcome unknown: %w", err)
		}
		return nil
	}
	err = migration.ArchiveDispatchedBackup(ctx, kube, source, *namespace, *name, *id, "/archive", 2*time.Second)
	if err != nil {
		return fmt.Errorf("backup archive is not ready: %w", err)
	}
	return migration.WriteArchiveValidationProof(ctx, kube, *namespace, *name, *id, "/dev/termination-log")
}

func migrationOnboardingClient() (*haclient.Client, error) {
	const dir = "/var/run/migration-worker"
	endpoint := os.Getenv("MIGRATION_ONBOARDING_ENDPOINT")
	if endpoint == "" {
		return nil, fmt.Errorf("migration onboarding endpoint unavailable")
	}
	caPEM, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("migration worker CA unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("migration worker CA invalid")
	}
	serverPEM, err := os.ReadFile(filepath.Join(dir, "server.crt"))
	if err != nil {
		return nil, fmt.Errorf("migration worker server certificate unavailable")
	}
	block, _ := pem.Decode(serverPEM)
	if block == nil {
		return nil, fmt.Errorf("migration worker server certificate invalid")
	}
	server, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("migration worker server certificate invalid")
	}
	worker, err := tls.LoadX509KeyPair(filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key"))
	if err != nil {
		return nil, fmt.Errorf("migration worker credential unavailable")
	}
	return haclient.NewMigrationOnboardingClient(endpoint, roots, server, worker, migration.ProxyServerName)
}
