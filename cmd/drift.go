package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/fluxcd/pkg/ssa/jsondiff"
	"os"

	helmaction "helm.sh/helm/v3/pkg/action"
	helmrelease "helm.sh/helm/v3/pkg/release"
	"k8s.io/cli-runtime/pkg/genericclioptions"

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
	GetRelease(name string) (*helmrelease.Release, error)
	DiffRelease(ctx context.Context, release *helmrelease.Release, controller string, ignoreRules []v2.IgnoreRule) (jsondiff.DiffSet, error)
}

type HelmActionClient struct {
	actionConfig *helmaction.Configuration
}

func NewHelmActionClient(namespace string, logger func(string, ...interface{})) (HelmClient, error) {
	settings := genericclioptions.NewConfigFlags(true)
	settings.Namespace = &namespace

	actionConfig := new(helmaction.Configuration)
	if err := actionConfig.Init(settings, namespace, os.Getenv("HELM_DRIVER"), logger); err != nil {
		return nil, err
	}

	return &HelmActionClient{
		actionConfig: actionConfig,
	}, nil
}

func (hc *HelmActionClient) GetRelease(name string) (*helmrelease.Release, error) {
	getter := helmaction.NewGet(hc.actionConfig)
	return getter.Run(name)
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

	release, err := h.HelmClient.GetRelease(releaseName)
	if err != nil {
		return fmt.Errorf("failed to get HelmRelease %s: %w", releaseName, err)
	}

	diffSet, err := h.HelmClient.DiffRelease(ctx, release, "helm-controller", []v2.IgnoreRule{})
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

	helmClient, err := NewHelmActionClient(namespace, log.Infof)
	if err != nil {
		logger.Fatalf("Failed to initialize Helm client: %v\n", err)
	}

	helmDriftDetect := NewHelmDriftDetect(log.StandardLogger(), helmClient)

	if err := helmDriftDetect.Run(context.Background(), releaseName, namespace); err != nil {
		logger.Fatalf("Application error: %v\n", err)
	}
}

