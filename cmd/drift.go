package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/fluxcd/pkg/ssa/jsondiff"
	helmaction "helm.sh/helm/v3/pkg/action"
	helmrelease "helm.sh/helm/v3/pkg/release"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"os"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/helm-controller/internal/action"
	log "github.com/sirupsen/logrus"
)

type Logger interface {
	Infof(format string, args ...interface{})
	Fatalf(format string, args ...interface{})
}

type LogrusAdapter struct{}

func (l *LogrusAdapter) Format(entry *log.Entry) ([]byte, error) {
	return []byte(entry.Message), nil
}

func (l *LogrusAdapter) Infof(format string, args ...interface{}) {
	log.Infof(format, args...)
}

func (l *LogrusAdapter) Fatalf(format string, args ...interface{}) {
	log.Fatalf(format, args...)
}

type HelmClient interface {
	GetHelmRelease(ctx context.Context, name, namespace string) (*v2.HelmRelease, error)
	GetRelease(name, storageNamespace string) (*helmrelease.Release, error)
	DiffRelease(ctx context.Context, release *helmrelease.Release, controller string, ignoreRules []v2.IgnoreRule) (jsondiff.DiffSet, error)
}

type HelmActionClient struct {
	logger       Logger
	settings     *genericclioptions.ConfigFlags
	client       *kubernetes.Clientset
	actionConfig *helmaction.Configuration
}

func NewHelmActionClient(settings *genericclioptions.ConfigFlags, logger Logger) (HelmClient, error) {
	config, err := settings.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get kubernetes config: %w", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}
	return &HelmActionClient{
		logger:   logger,
		settings: settings,
		client:   client,
	}, nil
}

func (hc *HelmActionClient) getActionConfig(storageNamespace string) error {
	if hc.actionConfig != nil {
		return nil
	}

	hc.actionConfig = new(helmaction.Configuration)
	if err := hc.actionConfig.Init(hc.settings, storageNamespace, os.Getenv("HELM_DRIVER"), hc.logger.Infof); err != nil {
		return err
	}

	return nil
}

func (hc *HelmActionClient) GetHelmRelease(ctx context.Context, name, namespace string) (*v2.HelmRelease, error) {
	raw, err := hc.client.RESTClient().Get().
		AbsPath("/apis/helm.toolkit.fluxcd.io/v2").
		Namespace(namespace).
		Resource("helmreleases").
		Name(name).
		DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get HelmRelease %s/%s: %w", namespace, name, err)
	}

	var helmRelease v2.HelmRelease
	if err := json.Unmarshal(raw, &helmRelease); err != nil {
		return nil, fmt.Errorf("failed to unmarshal HelmRelease: %w", err)
	}

	return &helmRelease, nil
}

func (hc *HelmActionClient) GetRelease(name, storageNamespace string) (*helmrelease.Release, error) {
	if err := hc.getActionConfig(storageNamespace); err != nil {
		return nil, fmt.Errorf("failed to initialize action configuration: %w", err)
	}
	return action.LastRelease(hc.actionConfig, name)
}

func (hc *HelmActionClient) DiffRelease(ctx context.Context, release *helmrelease.Release, controller string, ignoreRules []v2.IgnoreRule) (jsondiff.DiffSet, error) {
	return action.Diff(ctx, hc.actionConfig, release, controller, ignoreRules...)
}

type HelmDriftDetect struct {
	Logger     Logger
	HelmClient HelmClient
}

func NewHelmDriftDetect(logger Logger, helmClient HelmClient) *HelmDriftDetect {
	return &HelmDriftDetect{
		Logger:     logger,
		HelmClient: helmClient,
	}
}

func (h *HelmDriftDetect) Run(ctx context.Context, releaseName, namespace string) error {
	const (
		indent  = "    "
		newline = "\n"
	)

	helmRelease, err := h.HelmClient.GetHelmRelease(ctx, releaseName, namespace)
	if err != nil {
		return fmt.Errorf("failed to get HelmRelease %s/%s: %w", namespace, releaseName, err)
	}

	if helmRelease.Spec.ReleaseName != "" {
		releaseName = helmRelease.Spec.ReleaseName
	}

	storageNamespace := namespace
	if helmRelease.Spec.StorageNamespace != "" {
		storageNamespace = helmRelease.Spec.StorageNamespace
	}

	release, err := h.HelmClient.GetRelease(releaseName, storageNamespace)
	if err != nil {
		return fmt.Errorf("failed to get Helm release %s: %w", releaseName, err)
	}

	var ignoreRules []v2.IgnoreRule
	if helmRelease.Spec.DriftDetection != nil {
		ignoreRules = helmRelease.Spec.DriftDetection.Ignore
	}

	diffSet, err := h.HelmClient.DiffRelease(ctx, release, "helm-controller", ignoreRules)
	if err != nil {
		return fmt.Errorf("failed to detect drift: %w", err)
	}

	if diffSet.HasChanges() {
		h.Logger.Infof("Detected drift in HelmRelease %s/%s:%s", namespace, releaseName, newline)
		i := 1
		for _, d := range diffSet {
			switch d.Type {
			case jsondiff.DiffTypeCreate:
				h.Logger.Infof("%s%d - Resource: %s/%s%s", newline, i, d.GroupVersionKind().Kind, d.GetName(), newline)
				h.Logger.Infof("%sReason: removed%s", indent, newline)
				i++
			case jsondiff.DiffTypeUpdate:
				h.Logger.Infof("%s%d - Resource: %s/%s%s", newline, i, d.GroupVersionKind().Kind, d.GetName(), newline)
				h.Logger.Infof("%sReason: changed%s", indent, newline)
				for j, p := range d.Patch {
					h.Logger.Infof("%s%d - Path: %s%s", indent, j+1, p.Path, newline)
					h.Logger.Infof("%sRecovery Operation: %s%s", indent+indent, p.Type, newline)
					if p.Value != nil {
						h.Logger.Infof("%sOriginal Value: %v%s", indent+indent, p.Value, newline)
					}
				}
				i++
			}
		}
	} else {
		h.Logger.Infof("No drift detected in HelmRelease %s/%s%s", namespace, releaseName, newline)
	}
	return nil
}

func main() {
	logger := &LogrusAdapter{}
	log.SetFormatter(logger)

	ctrlLogger := zap.New(zap.UseDevMode(true))
	ctrl.SetLogger(ctrlLogger)

	var namespace, releaseName string
	flag.StringVar(&namespace, "n", "", "namespace of the HelmRelease")
	flag.StringVar(&releaseName, "r", "", "name of the HelmRelease")
	flag.Parse()

	if releaseName == "" {
		logger.Fatalf("Usage: %s [-n namespace] -r release-name\n", os.Args[0])
	}

	if namespace == "" {
		namespace = "default"
	}
	settings := genericclioptions.NewConfigFlags(true)
	helmClient, err := NewHelmActionClient(settings, logger)
	if err != nil {
		logger.Fatalf("Failed to initialize Helm client: %v\n", err)
	}

	helmDriftDetect := NewHelmDriftDetect(logger, helmClient)

	if err := helmDriftDetect.Run(context.Background(), releaseName, namespace); err != nil {
		logger.Fatalf("Application error: %v\n", err)
	}
}
