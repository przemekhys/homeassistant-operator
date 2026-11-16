package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	hav1 "github.com/przemekhys/homeassistant-operator/api/v1"
	hav1alpha1 "github.com/przemekhys/homeassistant-operator/api/v1alpha1"
	"github.com/przemekhys/homeassistant-operator/internal/haclient"
	"github.com/przemekhys/homeassistant-operator/internal/migration"
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "ha-operator-migrate",
		Short: "Safely scan and import an existing Home Assistant",
	}
	root.AddCommand(newScanCommand(), newImportCommand())
	return root
}

func migrationKubeClient() (client.Client, *runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, nil, err
	}
	if err := hav1.AddToScheme(scheme); err != nil {
		return nil, nil, err
	}
	if err := hav1alpha1.AddToScheme(scheme); err != nil {
		return nil, nil, err
	}
	config, err := ctrlconfig.GetConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	kube, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return nil, nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	return kube, scheme, nil
}

func sourceToken(
	ctx context.Context,
	kube client.Client,
	namespace, name, key string,
) (string, error) {
	secret := &corev1.Secret{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, secret); err != nil {
		return "", fmt.Errorf("read source token reference: %w", err)
	}
	token, ok := secret.Data[key]
	if !ok || len(token) == 0 {
		return "", fmt.Errorf("source token reference has no value")
	}
	return string(token), nil
}

func safeSourceID(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("source URL must be HTTP(S) and not contain credentials, query parameters, or fragments")
	}
	return parsed.String(), nil
}

func newScanCommand() *cobra.Command {
	var sourceURL, tokenSecret, tokenKey, namespace, targetName string
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Read a source without writing HA or Kubernetes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			kube, _, err := migrationKubeClient()
			if err != nil {
				return err
			}
			token, err := sourceToken(ctx, kube, namespace, tokenSecret, tokenKey)
			if err != nil {
				return err
			}
			target := &hav1.HomeAssistant{}
			if err := kube.Get(ctx, client.ObjectKey{Namespace: namespace, Name: targetName}, target); err != nil {
				return fmt.Errorf("read target: %w", err)
			}
			sourceID, err := safeSourceID(sourceURL)
			if err != nil {
				return err
			}
			report, err := migration.Scan(
				ctx,
				haclient.NewClient(sourceID),
				migration.ScanRequest{
					SourceID: sourceID,
					Token:    token,
					Target: migration.Target{
						Namespace: namespace,
						Name:      target.Name,
						UID:       string(target.UID),
					},
				},
			)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
		},
	}
	cmd.Flags().StringVar(&sourceURL, "source-url", "", "Home Assistant source URL")
	cmd.Flags().
		StringVar(&tokenSecret, "source-token-secret", "", "Secret containing the source token")
	cmd.Flags().
		StringVar(&tokenKey, "source-token-key", "token", "Key in the source token Secret")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Target namespace")
	cmd.Flags().StringVar(&targetName, "target", "", "Target HomeAssistant name")
	_ = cmd.MarkFlagRequired("source-url")
	_ = cmd.MarkFlagRequired("source-token-secret")
	_ = cmd.MarkFlagRequired("namespace")
	_ = cmd.MarkFlagRequired("target")
	return cmd
}

func newImportCommand() *cobra.Command {
	var reportPath, reportID, namespace, targetName, migrationName string
	var tokenSecret, tokenKey string
	var recoverySecret, recoveryKey string
	var confirm bool
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Create a paused migration from an approved report",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !confirm {
				return fmt.Errorf("import requires --confirm")
			}
			contents, err := os.ReadFile(reportPath)
			if err != nil {
				return fmt.Errorf("read report: %w", err)
			}
			var report migration.Report
			if err := json.Unmarshal(contents, &report); err != nil {
				return fmt.Errorf("parse report: %w", err)
			}
			if report.Target.Namespace != namespace || report.Target.Name != targetName {
				return fmt.Errorf("report target does not match command target")
			}
			kube, scheme, err := migrationKubeClient()
			if err != nil {
				return err
			}
			var tokenRef *corev1.SecretKeySelector
			if tokenSecret != "" {
				tokenRef = &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: tokenSecret}, Key: tokenKey}
			}
			var recoveryRef *corev1.SecretKeySelector
			if recoverySecret != "" {
				recoveryRef = &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: recoverySecret}, Key: recoveryKey,
				}
			}
			result, err := migration.Import(
				cmd.Context(),
				kube,
				migration.ImportRequest{
					Report:               &report,
					ReportID:             reportID,
					Namespace:            namespace,
					Name:                 migrationName,
					Confirmed:            confirm,
					SourceTokenSecretRef: tokenRef,
					RecoveryKeySecretRef: recoveryRef,
					Scheme:               scheme,
				},
			)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
	cmd.Flags().StringVar(&reportPath, "report", "", "Path to the approved scan report")
	cmd.Flags().StringVar(&reportID, "report-id", "", "Approved report content ID")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Target namespace")
	cmd.Flags().StringVar(&targetName, "target", "", "Target HomeAssistant name")
	cmd.Flags().StringVar(&migrationName, "name", "", "Name for HomeAssistantMigration")
	cmd.Flags().StringVar(&tokenSecret, "source-token-secret", "", "Source token Secret in the target namespace")
	cmd.Flags().StringVar(&tokenKey, "source-token-key", "token", "Key in the source token Secret")
	cmd.Flags().StringVar(&recoverySecret, "recovery-key-secret", "", "Recovery key Secret in the target namespace")
	cmd.Flags().StringVar(&recoveryKey, "recovery-key-key", "key", "Key in the recovery key Secret")
	cmd.Flags().
		BoolVar(&confirm, "confirm", false, "Acknowledge creation of the migration operation")
	_ = cmd.MarkFlagRequired("report")
	_ = cmd.MarkFlagRequired("report-id")
	_ = cmd.MarkFlagRequired("namespace")
	_ = cmd.MarkFlagRequired("target")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}
