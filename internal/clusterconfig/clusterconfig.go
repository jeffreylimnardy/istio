package clusterconfig

import (
	"context"
	"regexp"
	ctrl "sigs.k8s.io/controller-runtime"
	"strings"

	"github.com/imdario/mergo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	ProductionClusterCpuThreshold      int64 = 5
	ProductionClusterMemoryThresholdGi int64 = 10
)

type ClusterSize int

const (
	UnknownSize ClusterSize = iota
	Evaluation
	Production
)

func (s ClusterSize) String() string {
	switch s {
	case Evaluation:
		return "Evaluation"
	case Production:
		return "Production"
	default:
		return "Unknown"
	}
}

// EvaluateClusterSize counts the entire capacity of cpu and memory in the cluster and returns Evaluation
// if the total capacity of any of the resources is lower than ProductionClusterCpuThreshold or ProductionClusterMemoryThresholdGi
func EvaluateClusterSize(ctx context.Context, k8sClient client.Client) (ClusterSize, error) {
	nodeList := corev1.NodeList{}
	err := k8sClient.List(ctx, &nodeList)
	if err != nil {
		return UnknownSize, err
	}

	var cpuCapacity resource.Quantity
	var memoryCapacity resource.Quantity
	for _, node := range nodeList.Items {
		nodeCpuCap := node.Status.Capacity.Cpu()
		if nodeCpuCap != nil {
			cpuCapacity.Add(*nodeCpuCap)
		}
		nodeMemoryCap := node.Status.Capacity.Memory()
		if nodeMemoryCap != nil {
			memoryCapacity.Add(*nodeMemoryCap)
		}
	}
	if cpuCapacity.Cmp(*resource.NewQuantity(ProductionClusterCpuThreshold, resource.DecimalSI)) == -1 ||
		memoryCapacity.Cmp(*resource.NewScaledQuantity(ProductionClusterMemoryThresholdGi, resource.Giga)) == -1 {
		return Evaluation, nil
	}
	return Production, nil
}

type ClusterFlavour int

const (
	Unknown ClusterFlavour = iota
	k3d
	GKE
	Gardener
)

func (c ClusterFlavour) String() string {
	switch c {
	case k3d:
		return "k3d"
	case GKE:
		return "GKE"
	case Gardener:
		return "Gardener"
	}
	return "Unknown"
}

type ClusterConfiguration map[string]interface{}

func EvaluateClusterConfiguration(ctx context.Context, k8sClient client.Client) (ClusterConfiguration, error) {
	flavour, err := DiscoverClusterFlavour(ctx, k8sClient)
	if err != nil {
		return ClusterConfiguration{}, err
	}
	return flavour.clusterConfiguration()
}

// GetClusterProvider is a small hack that tries to determine the
// hyperscaler based on the first provider node.
func GetClusterProvider(ctx context.Context, k8sclient client.Client) (string, error) {
	nodes := corev1.NodeList{}
	err := k8sclient.List(ctx, &nodes)
	if err != nil {
		return "", err
	}
	// if we got OK response and node list is empty, we can't guess cloud provider
	// treat as "other" provider
	// in standard execution this should never be reached because if cluster
	// doesn't have any nodes, nothing can be run on it
	// this catches rare case where cluster doesn't have any nodes, but
	// client-go also doesn't return any error
	if len(nodes.Items) == 0 {
		ctrl.Log.Info("unable to determine cloud provider due to empty node list, using 'other' as provider")
		return "other", nil
	}

	// get 1st node since all nodes usually are backed by the same provider
	n := nodes.Items[0]
	provider := n.Spec.ProviderID
	switch {
	case strings.HasPrefix(provider, "aws://"):
		return "aws", nil
	default:
		return "other", nil
	}
}

func DiscoverClusterFlavour(ctx context.Context, k8sClient client.Client) (ClusterFlavour, error) {
	matcherGKE, err := regexp.Compile(`^v\d+\.\d+\.\d+-gke\.\d+$`)
	if err != nil {
		return Unknown, err
	}
	matcherk3d, err := regexp.Compile(`^v\d+\.\d+\.\d+\+k3s\d+$`)
	if err != nil {
		return Unknown, err
	}
	matcherGardener, err := regexp.Compile(`^Garden Linux \d+.\d+$`)
	if err != nil {
		return Unknown, err
	}
	nodeList := corev1.NodeList{}
	err = k8sClient.List(ctx, &nodeList)
	if err != nil {
		return Unknown, err
	}

	for _, node := range nodeList.Items {
		if matcherGKE.MatchString(node.Status.NodeInfo.KubeletVersion) {
			return GKE, nil
		} else if matcherk3d.MatchString(node.Status.NodeInfo.KubeletVersion) {
			return k3d, nil
		} else if matcherGardener.MatchString(node.Status.NodeInfo.OSImage) {
			return Gardener, nil
		}
	}

	return Unknown, nil
}

func (c ClusterFlavour) clusterConfiguration() (ClusterConfiguration, error) {
	switch c {
	case k3d:
		config := map[string]interface{}{
			"spec": map[string]interface{}{
				"values": map[string]interface{}{
					"cni": map[string]string{
						"cniBinDir":  "/bin",
						"cniConfDir": "/var/lib/rancher/k3s/agent/etc/cni/net.d",
					},
				},
			},
		}
		return config, nil
	case GKE:
		config := map[string]interface{}{
			"spec": map[string]interface{}{
				"values": map[string]interface{}{
					"cni": map[string]interface{}{
						"cniBinDir": "/home/kubernetes/bin",
						"resourceQuotas": map[string]bool{
							"enabled": true,
						},
					},
				},
			},
		}
		return config, nil
	case Gardener:
		return ClusterConfiguration{}, nil
	}
	return ClusterConfiguration{}, nil
}

func MergeOverrides(template []byte, overrides ClusterConfiguration) ([]byte, error) {
	var templateMap map[string]interface{}
	err := yaml.Unmarshal(template, &templateMap)
	if err != nil {
		return nil, err
	}

	err = mergo.Merge(&templateMap, map[string]interface{}(overrides), mergo.WithOverride)
	if err != nil {
		return nil, err
	}

	return yaml.Marshal(templateMap)
}
