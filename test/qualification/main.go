// The qualification program is a separate test-only entrypoint. It is not
// copied into the production container and cannot qualify a production image.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/controller"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"github.com/przemekhys/homeassistant-operator/test/utils"
)

type qualificationOptions struct {
	Target                                utils.BackupRestoreQualificationTarget
	Context, TargetName, Operation, HAURL string
}

func (o qualificationOptions) validate() error {
	if err := o.Target.Validate(o.Target.ImageDigest, o.Target.Namespace, o.Target.TargetUID); err != nil {
		return err
	}
	if !strings.HasPrefix(o.Context, "k3d-") || len(o.Context) == len("k3d-") {
		return fmt.Errorf("qualification requires an explicit k3d context")
	}
	if o.Target.Namespace == "default" || strings.HasPrefix(o.Target.Namespace, "kube-") ||
		o.TargetName == "" || o.Operation == "" {
		return fmt.Errorf("qualification requires a dedicated namespace, target name and operation")
	}
	u, err := url.Parse(o.HAURL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") {
		return fmt.Errorf("qualification requires a loopback HTTP port-forward URL")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() || u.Port() == "" {
		return fmt.Errorf("qualification requires a loopback HTTP port-forward URL")
	}
	return nil
}

func main() {
	o := qualificationOptions{}
	flag.StringVar(&o.Target.ImageDigest, "image-digest", os.Getenv("QUALIFICATION_IMAGE_DIGEST"),
		"Pinned platform manifest digest")
	flag.StringVar(
		&o.Target.Namespace,
		"namespace",
		os.Getenv("QUALIFICATION_NAMESPACE"),
		"Disposable labelled namespace",
	)
	flag.StringVar(
		&o.Target.TargetUID,
		"target-uid",
		os.Getenv("QUALIFICATION_TARGET_UID"),
		"Live disposable target UID",
	)
	flag.StringVar(&o.TargetName, "target", os.Getenv("QUALIFICATION_TARGET_NAME"), "Live disposable target name")
	flag.StringVar(&o.Operation, "operation", os.Getenv("QUALIFICATION_OPERATION"), "Existing HomeAssistantBackup name")
	flag.StringVar(
		&o.Context,
		"kube-context",
		os.Getenv("QUALIFICATION_KUBE_CONTEXT"),
		"Explicit disposable k3d context",
	)
	flag.StringVar(&o.HAURL, "ha-url", os.Getenv("QUALIFICATION_HA_URL"), "Loopback port-forward to the target service")
	flag.Parse()
	ctrl.SetLogger(zap.New())
	if err := run(ctrl.SetupSignalHandler(), o); err != nil {
		// Never print arbitrary API errors: responses can contain credentials.
		fmt.Fprintln(os.Stderr, "Backup qualification did not pass:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, o qualificationOptions) error {
	if err := o.validate(); err != nil {
		return err
	}
	loading := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{CurrentContext: o.Context}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading, overrides).ClientConfig()
	if err != nil {
		return fmt.Errorf("qualification kubeconfig is unavailable")
	}
	config.Timeout = 10 * time.Second
	scheme := runtime.NewScheme()
	additions := []func(*runtime.Scheme) error{
		corev1.AddToScheme, appsv1.AddToScheme, hav1.AddToScheme, hav1alpha1.AddToScheme,
	}
	for _, add := range additions {
		if err := add(scheme); err != nil {
			return fmt.Errorf("qualification scheme is unavailable")
		}
	}
	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("qualification API client is unavailable")
	}
	gate := scopedBackupGate{Reader: c, Target: o.Target}
	r := &controller.HomeAssistantBackupReconciler{
		Client: c, APIReader: c, Scheme: scheme, CapabilityGate: gate,
		KeyProtection: controller.RetainedBackupKeyProvider{Reader: c},
		NewHAClient:   func(string) *haclient.Client { return haclient.NewClient(o.HAURL) },
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	key := types.NamespacedName{Namespace: o.Target.Namespace, Name: o.Operation}
	for {
		step, finish := context.WithTimeout(ctx, 20*time.Second)
		err := reconcileCandidate(step, c, r, gate, o, key)
		finish()
		if err != nil {
			return err
		}
		operation := &hav1alpha1.HomeAssistantBackup{}
		if err := c.Get(ctx, key, operation); err != nil {
			return fmt.Errorf("qualification result is unavailable")
		}
		switch operation.Status.Phase {
		case hav1alpha1.BackupPhaseSucceeded:
			fmt.Println(
				"Native backup operation succeeded; download, restart and destination evidence remain required.",
				"No production capability was promoted.",
			)
			return nil
		case hav1alpha1.BackupPhaseFailed,
			hav1alpha1.BackupPhasePartiallySucceeded,
			hav1alpha1.BackupPhaseOutcomeUnknown:
			return fmt.Errorf("native backup did not establish full success; inspect retained evidence and safe status")
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("qualification observation ended; no retry authority or hold release was granted")
		case <-timer.C:
		}
	}
}

func reconcileCandidate(
	ctx context.Context, c client.Client, r *controller.HomeAssistantBackupReconciler,
	gate scopedBackupGate, o qualificationOptions, key types.NamespacedName,
) error {
	target := &hav1.HomeAssistant{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: o.Target.Namespace, Name: o.TargetName}, target); err != nil {
		return fmt.Errorf("qualification target is unavailable")
	}
	if err := gate.AuthorizeBackup(ctx, target); err != nil {
		return err
	}
	operation := &hav1alpha1.HomeAssistantBackup{}
	if err := c.Get(ctx, key, operation); err != nil {
		return fmt.Errorf("qualification operation is unavailable")
	}
	if operation.Spec.HomeAssistantRef.Name != o.TargetName ||
		operation.Spec.HomeAssistantRef.UID != o.Target.TargetUID {
		return fmt.Errorf("qualification operation target does not match")
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		if apierrors.IsConflict(err) {
			// The production manager can publish observation status concurrently.
			// A fresh level-triggered step still consults durable dispatch evidence.
			return nil
		}
		return fmt.Errorf("qualification reconcile failed; inspect retained evidence before proceeding")
	}
	return nil
}
