/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/open-policy-agent/cert-controller/pkg/rotator"
	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/communityrepo"
	"github.com/przemekhys/homeassistant-operator/internal/controller"
	"github.com/przemekhys/homeassistant-operator/internal/version"
	webhookhav1 "github.com/przemekhys/homeassistant-operator/internal/webhook/v1"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(hav1.AddToScheme(scheme))
	utilruntime.Must(hav1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// getenvOr returns the value of the environment variable key, or def when unset.
func getenvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ensureWebhookSecret creates an empty Opaque Secret for the self-managed webhook
// serving certificate when it does not already exist. cert-controller updates this
// Secret but never creates it, so the operator bootstraps it here — keeping it out
// of the chart/Kustomize avoids Helm ownership conflicts and cert-manager↔self-managed
// switch issues. A direct client is used because the manager cache is not started yet.
func ensureWebhookSecret(mgr ctrl.Manager, namespace, name string) error {
	c, err := client.New(mgr.GetConfig(), client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		return err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeOpaque,
	}
	if err := c.Create(context.Background(), secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	setupLog.Info("starting homeassistant-operator",
		"version", version.Version,
		"commit", version.GitCommit,
	)

	// WATCH_NAMESPACES: comma-separated list of namespaces to watch.
	// When empty, operator watches all namespaces (requires ClusterRoleBinding).
	// When set, operator watches only listed namespaces (RoleBindings per namespace sufficient).
	operatorNamespace := os.Getenv("OPERATOR_NAMESPACE")
	var defaultNamespaces map[string]cache.Config
	if watchNS := os.Getenv("WATCH_NAMESPACES"); watchNS != "" {
		defaultNamespaces = make(map[string]cache.Config)
		for _, ns := range strings.Split(watchNS, ",") {
			if ns = strings.TrimSpace(ns); ns != "" {
				defaultNamespaces[ns] = cache.Config{}
			}
		}
		if len(defaultNamespaces) == 0 {
			setupLog.Error(nil, "WATCH_NAMESPACES is set but contains no valid namespace "+
				"entries after trimming; refusing to start with empty scope")
			os.Exit(1)
		}
		setupLog.Info("watching specific namespaces", "namespaces", watchNS)
	} else {
		setupLog.Info("watching all namespaces (set WATCH_NAMESPACES to restrict)")
	}

	// COMMUNITY_REPOSITORY_CODELOAD_BASE_URL: redirects the HomeAssistantCommunityRepository
	// controller's GitHub tarball fetch (and the matching env var passed to the
	// generated init-container/sidecar) away from the real codeload.github.com.
	// Production deployments never set this; it exists solely so E2E tests can
	// point it at an in-cluster fixture server.
	if codeloadBaseURL := os.Getenv("COMMUNITY_REPOSITORY_CODELOAD_BASE_URL"); codeloadBaseURL != "" {
		communityrepo.CodeloadBaseURL = codeloadBaseURL
		setupLog.Info("overriding community repository codeload base URL (test-only)", "url", codeloadBaseURL)
	}

	// The self-managed webhook serving-cert Secret (and its ValidatingWebhookConfiguration
	// CA injection) always lives in the operator's own namespace, regardless of which
	// namespaces are watched for HomeAssistant CRs. Scope this to a Secret-only ByObject
	// override rather than adding it to defaultNamespaces, which would broaden the cache
	// to every namespaced type (StatefulSets, ConfigMaps, HA CRDs, ...) in that namespace.
	var cacheByObject map[client.Object]cache.ByObject
	if defaultNamespaces != nil && operatorNamespace != "" {
		if _, ok := defaultNamespaces[operatorNamespace]; !ok {
			secretNamespaces := make(map[string]cache.Config, len(defaultNamespaces)+1)
			for ns, cfg := range defaultNamespaces {
				secretNamespaces[ns] = cfg
			}
			secretNamespaces[operatorNamespace] = cache.Config{}
			cacheByObject = map[client.Object]cache.ByObject{
				&corev1.Secret{}: {Namespaces: secretNamespaces},
			}
		}
	}

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Webhook enablement (opt-out: on unless ENABLE_WEBHOOKS=false). The serving
	// certificate is self-managed by the operator (cert-controller) unless
	// cert-manager is selected via WEBHOOK_SELF_MANAGE_CERT=false — so the webhook
	// needs no cert-manager by default.
	enableWebhooks := os.Getenv("ENABLE_WEBHOOKS") != "false"
	webhookSelfManageCert := os.Getenv("WEBHOOK_SELF_MANAGE_CERT") != "false"
	// Self-managed webhook needs the operator namespace (to write the Secret and
	// patch the ValidatingWebhookConfiguration). When it is unset — typically local
	// `make run` — skip the webhook instead of failing, so out-of-cluster dev works.
	if enableWebhooks && webhookSelfManageCert && operatorNamespace == "" {
		setupLog.Info("OPERATOR_NAMESPACE not set; disabling the validating webhook " +
			"(set ENABLE_WEBHOOKS=false to silence, or run in-cluster)")
		enableWebhooks = false
	}
	webhookCertDir := webhookCertPath
	if enableWebhooks && webhookCertDir == "" {
		webhookCertDir = "/tmp/k8s-webhook-server/serving-certs"
	}

	// Create watchers for metrics and webhook certificates.
	var metricsCertWatcher, webhookCertWatcher *certwatcher.CertWatcher
	webhookTLSOpts := tlsOpts

	// cert-manager mode: watch the mounted serving-cert files. Self-managed mode:
	// cert-controller writes into webhookCertDir and the webhook server loads them
	// from CertDir, so no external watcher is needed (and the files may not exist
	// yet at startup).
	if enableWebhooks && !webhookSelfManageCert && len(webhookCertDir) > 0 {
		setupLog.Info("Initializing webhook certificate watcher (cert-manager mode)",
			"webhook-cert-path", webhookCertDir)
		var err error
		webhookCertWatcher, err = certwatcher.New(
			filepath.Join(webhookCertDir, webhookCertName),
			filepath.Join(webhookCertDir, webhookCertKey),
		)
		if err != nil {
			setupLog.Error(err, "Failed to initialize webhook certificate watcher")
			os.Exit(1)
		}
		webhookTLSOpts = append(webhookTLSOpts, func(config *tls.Config) {
			config.GetCertificate = webhookCertWatcher.GetCertificate
		})
	}

	// Self-managed mode: cert-controller writes the serving cert to webhookCertDir
	// at runtime. Load it lazily per-connection (via GetCertificate) instead of a
	// CertDir certwatcher, which would fail to start before the cert exists — the
	// operator would crash-loop and its webhook Service would have no endpoints.
	if enableWebhooks && webhookSelfManageCert && len(webhookCertDir) > 0 {
		crtPath := filepath.Join(webhookCertDir, webhookCertName)
		keyPath := filepath.Join(webhookCertDir, webhookCertKey)
		webhookTLSOpts = append(webhookTLSOpts, func(cfg *tls.Config) {
			cfg.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				crt, err := tls.LoadX509KeyPair(crtPath, keyPath)
				if err != nil {
					return nil, err
				}
				return &crt, nil
			}
		})
	}

	webhookServer := webhook.NewServer(webhook.Options{TLSOpts: webhookTLSOpts})

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		var err error
		metricsCertWatcher, err = certwatcher.New(
			filepath.Join(metricsCertPath, metricsCertName),
			filepath.Join(metricsCertPath, metricsCertKey),
		)
		if err != nil {
			setupLog.Error(err, "Failed to initialize metrics certificate watcher")
			os.Exit(1)
		}

		metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, func(config *tls.Config) {
			config.GetCertificate = metricsCertWatcher.GetCertificate
		})
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "a26346d9.homeassistant.io",
		Cache:                  cache.Options{DefaultNamespaces: defaultNamespaces, ByObject: cacheByObject},
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := (&controller.HomeAssistantReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Recorder:  mgr.GetEventRecorder("homeassistant-controller"),
		APIReader: mgr.GetAPIReader(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "HomeAssistant")
		os.Exit(1)
	}
	if err := (&controller.HomeAssistantSecretsReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "HomeAssistantSecrets")
		os.Exit(1)
	}
	if err := (&controller.HomeAssistantConfigurationReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("homeassistantconfiguration-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "HomeAssistantConfiguration")
		os.Exit(1)
	}
	if err := (&controller.HomeAssistantAutomationReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("homeassistantautomation-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "HomeAssistantAutomation")
		os.Exit(1)
	}
	if err := (&controller.HomeAssistantSceneReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("homeassistantscene-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "HomeAssistantScene")
		os.Exit(1)
	}
	if err := (&controller.HomeAssistantScriptReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("homeassistantscript-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "HomeAssistantScript")
		os.Exit(1)
	}
	if err := (&controller.HomeAssistantIntegrationReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("homeassistantintegration-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "HomeAssistantIntegration")
		os.Exit(1)
	}
	if err := (&controller.HomeAssistantFloorReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "HomeAssistantFloor")
		os.Exit(1)
	}
	if err := (&controller.HomeAssistantLabelReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "HomeAssistantLabel")
		os.Exit(1)
	}
	if err := (&controller.HomeAssistantAreaReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "HomeAssistantArea")
		os.Exit(1)
	}
	if err := (&controller.HomeAssistantCommunityRepositoryReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("homeassistantcommunityrepository-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "HomeAssistantCommunityRepository")
		os.Exit(1)
	}
	if err := (&controller.HomeAssistantDashboardReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "HomeAssistantDashboard")
		os.Exit(1)
	}
	// Webhook: self-provision the serving certificate (cert-controller writes a
	// self-signed cert to a Secret + CertDir and injects the caBundle into the
	// ValidatingWebhookConfiguration) unless cert-manager provides it. The webhook
	// is registered only once the certificate is ready.
	if enableWebhooks {
		setupFinished := make(chan struct{})
		if webhookSelfManageCert {
			ns := operatorNamespace
			svc := getenvOr("WEBHOOK_SERVICE_NAME", "homeassistant-operator-webhook-service")
			secretName := getenvOr("WEBHOOK_SECRET_NAME", "homeassistant-operator-webhook-server-cert")
			vwcName := getenvOr("WEBHOOK_CONFIG_NAME", "homeassistant-operator-validating-webhook-configuration")
			dnsName := fmt.Sprintf("%s.%s.svc", svc, ns)
			setupLog.Info("Setting up self-managed webhook certificate rotator",
				"secret", secretName, "dnsName", dnsName, "webhookConfig", vwcName)
			// cert-controller updates but never creates the Secret — bootstrap it.
			if err := ensureWebhookSecret(mgr, ns, secretName); err != nil {
				setupLog.Error(err, "unable to ensure webhook serving-cert Secret")
				os.Exit(1)
			}
			if err := rotator.AddRotator(mgr, &rotator.CertRotator{
				SecretKey:             types.NamespacedName{Namespace: ns, Name: secretName},
				CertDir:               webhookCertDir,
				CAName:                "homeassistant-operator-webhook-ca",
				CAOrganization:        "homeassistant-operator",
				DNSName:               dnsName,
				ExtraDNSNames:         []string{dnsName + ".cluster.local"},
				IsReady:               setupFinished,
				Webhooks:              []rotator.WebhookInfo{{Name: vwcName, Type: rotator.Validating}},
				RequireLeaderElection: false,
			}); err != nil {
				setupLog.Error(err, "unable to set up webhook certificate rotator")
				os.Exit(1)
			}
		} else {
			// cert-manager provides the serving cert and injects the CA — ready now.
			close(setupFinished)
		}

		go func() {
			<-setupFinished
			if err := webhookhav1.SetupHomeAssistantWebhookWithManager(mgr); err != nil {
				setupLog.Error(err, "unable to create webhook", "webhook", "HomeAssistant")
				os.Exit(1)
			}
			if err := webhookhav1.SetupHomeAssistantAutomationWebhookWithManager(mgr); err != nil {
				setupLog.Error(err, "unable to create webhook", "webhook", "HomeAssistantAutomation")
				os.Exit(1)
			}
			if err := webhookhav1.SetupHomeAssistantSceneWebhookWithManager(mgr); err != nil {
				setupLog.Error(err, "unable to create webhook", "webhook", "HomeAssistantScene")
				os.Exit(1)
			}
			if err := webhookhav1.SetupHomeAssistantScriptWebhookWithManager(mgr); err != nil {
				setupLog.Error(err, "unable to create webhook", "webhook", "HomeAssistantScript")
				os.Exit(1)
			}
			if err := webhookhav1.SetupHomeAssistantConfigurationWebhookWithManager(mgr); err != nil {
				setupLog.Error(err, "unable to create webhook", "webhook", "HomeAssistantConfiguration")
				os.Exit(1)
			}
			setupLog.Info("validating webhooks registered")
		}()
	}
	// +kubebuilder:scaffold:builder

	if metricsCertWatcher != nil {
		setupLog.Info("Adding metrics certificate watcher to manager")
		if err := mgr.Add(metricsCertWatcher); err != nil {
			setupLog.Error(err, "unable to add metrics certificate watcher to manager")
			os.Exit(1)
		}
	}

	if webhookCertWatcher != nil {
		setupLog.Info("Adding webhook certificate watcher to manager")
		if err := mgr.Add(webhookCertWatcher); err != nil {
			setupLog.Error(err, "unable to add webhook certificate watcher to manager")
			os.Exit(1)
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
