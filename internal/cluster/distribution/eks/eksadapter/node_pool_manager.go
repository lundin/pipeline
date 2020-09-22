// Copyright © 2020 Banzai Cloud
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package eksadapter

import (
	"context"
	"strings"
	"time"

	"emperror.dev/errors"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"go.uber.org/cadence/client"

	"github.com/banzaicloud/pipeline/internal/cluster"
	"github.com/banzaicloud/pipeline/internal/cluster/distribution/eks"
	"github.com/banzaicloud/pipeline/internal/cluster/distribution/eks/eksprovider/workflow"
	"github.com/banzaicloud/pipeline/internal/cluster/distribution/eks/eksworkflow"
	"github.com/banzaicloud/pipeline/pkg/kubernetes/custom/npls"
	sdkCloudFormation "github.com/banzaicloud/pipeline/pkg/sdk/providers/amazon/cloudformation"
)

const (
	// nodePoolStackNamePrefix is the prefix of CloudFormation stack names of
	// node pools managed by the pipeline.
	nodePoolStackNamePrefix = "pipeline-eks-nodepool-"
)

type nodePoolManager struct {
	awsFactory            workflow.AWSFactory
	cloudFormationFactory workflow.CloudFormationAPIFactory
	dynamicClientFactory  cluster.DynamicKubeClientFactory
	enterprise            bool
	namespace             string
	workflowClient        client.Client
}

// NewNodePoolManager returns a new eks.NodePoolManager
// that manages node pools asynchronously via Cadence workflows.
func NewNodePoolManager(
	awsFactory workflow.AWSFactory,
	cloudFormationFactory workflow.CloudFormationAPIFactory,
	dynamicClientFactory cluster.DynamicKubeClientFactory,
	enterprise bool,
	namespace string,
	workflowClient client.Client,
) eks.NodePoolManager {
	return nodePoolManager{
		awsFactory:            awsFactory,
		cloudFormationFactory: cloudFormationFactory,
		dynamicClientFactory:  dynamicClientFactory,
		enterprise:            enterprise,
		namespace:             namespace,
		workflowClient:        workflowClient,
	}
}

func (n nodePoolManager) UpdateNodePool(
	ctx context.Context,
	c cluster.Cluster,
	nodePoolName string,
	nodePoolUpdate eks.NodePoolUpdate,
) (string, error) {
	taskList := "pipeline"
	if n.enterprise {
		taskList = "pipeline-enterprise"
	}

	workflowOptions := client.StartWorkflowOptions{
		TaskList:                     taskList,
		ExecutionStartToCloseTimeout: 30 * 24 * 60 * time.Minute,
	}

	input := eksworkflow.UpdateNodePoolWorkflowInput{
		ProviderSecretID: c.SecretID.String(),
		Region:           c.Location,

		StackName: generateNodePoolStackName(c.Name, nodePoolName),

		ClusterID:       c.ID,
		ClusterSecretID: c.ConfigSecretID.String(),
		ClusterName:     c.Name,
		NodePoolName:    nodePoolName,
		OrganizationID:  c.OrganizationID,

		NodeVolumeSize: nodePoolUpdate.VolumeSize,
		NodeImage:      nodePoolUpdate.Image,

		Options: eks.NodePoolUpdateOptions{
			MaxSurge:       nodePoolUpdate.Options.MaxSurge,
			MaxBatchSize:   nodePoolUpdate.Options.MaxBatchSize,
			MaxUnavailable: nodePoolUpdate.Options.MaxUnavailable,
			Drain: eks.NodePoolUpdateDrainOptions{
				Timeout:     nodePoolUpdate.Options.Drain.Timeout,
				FailOnError: nodePoolUpdate.Options.Drain.FailOnError,
				PodSelector: nodePoolUpdate.Options.Drain.PodSelector,
			},
		},
		ClusterTags: c.Tags,
	}

	e, err := n.workflowClient.StartWorkflow(ctx, workflowOptions, eksworkflow.UpdateNodePoolWorkflowName, input)
	if err != nil {
		return "", errors.WrapWithDetails(err, "failed to start workflow", "workflow", eksworkflow.UpdateNodePoolWorkflowName)
	}

	return e.ID, nil
}

// TODO: this is temporary
func generateNodePoolStackName(clusterName string, poolName string) string {
	return nodePoolStackNamePrefix + clusterName + "-" + poolName
}

// ListNodePools lists node pools from a cluster.
func (n nodePoolManager) ListNodePools(ctx context.Context, cluster cluster.Cluster, nodePoolNames []string) ([]eks.NodePool, error) {
	clusterClient, err := n.dynamicClientFactory.FromSecret(ctx, cluster.ConfigSecretID.String())
	if err != nil {
		return nil, errors.WrapWithDetails(err, "creating dynamic Kubernetes client factory failed", "cluster", cluster)
	}

	manager := npls.NewManager(clusterClient, n.namespace)
	labelSets, err := manager.GetAll(ctx)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "retrieving node pool label sets failed",
			"cluster", cluster,
			"namespace", n.namespace,
		)
	}

	awsClient, err := n.awsFactory.New(cluster.OrganizationID, cluster.SecretID.ResourceID, cluster.Location)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "creating aws factory failed", "cluster", cluster)
	}

	cfClient := n.cloudFormationFactory.New(awsClient)
	describeStacksInput := cloudformation.DescribeStacksInput{}
	var stackStatuses map[string]*cloudformation.StackSummary
	nodePools := make([]eks.NodePool, 0, len(nodePoolNames))
	for _, nodePoolName := range nodePoolNames {
		nodePools = append(nodePools, eks.NodePool{
			Name:   nodePoolName,
			Labels: labelSets[nodePoolName],
		})
		nodePool := &nodePools[len(nodePools)-1]

		stackName := generateNodePoolStackName(cluster.Name, nodePoolName)
		describeStacksInput.StackName = &stackName
		stackDescriptions, err := cfClient.DescribeStacks(&describeStacksInput)
		if err != nil {
			if stackStatuses == nil {
				listStacksOutput, err := cfClient.ListStacks(&cloudformation.ListStacksInput{
					StackStatusFilter: aws.StringSlice(getNonDescrabableStackStatuses()),
				})
				if err != nil {
					return nil, errors.WrapWithDetails(err, "listing node pool cloudformation stacks failed",
						"cluster", cluster,
					)
				}

				stackStatuses = make(map[string]*cloudformation.StackSummary, len(listStacksOutput.StackSummaries))
				for _, summary := range listStacksOutput.StackSummaries {
					stackStatuses[aws.StringValue(summary.StackName)] = summary
				}
			}

			summary, isExisting := stackStatuses[stackName]
			if !isExisting {
				nodePool.Status = eks.NodePoolStatusUnknown
				nodePool.StatusMessage = "Retrieving node pool information failed: CloudFormation stack information is not available."

				continue
			}

			nodePool.Status = NewNodePoolStatusFromCFStack(aws.StringValue(summary.StackStatus))
			nodePool.StatusMessage = aws.StringValue(summary.StackStatusReason)

			continue
		} else if len(stackDescriptions.Stacks) == 0 {
			nodePool.Status = eks.NodePoolStatusError
			nodePool.StatusMessage = "Retrieving node pool information failed: CloudFormation stack not found."

			continue
		}

		var nodePoolParameters struct {
			ClusterAutoscalerEnabled    bool   `mapstructure:"ClusterAutoscalerEnabled"`
			NodeAutoScalingGroupMaxSize int    `mapstructure:"NodeAutoScalingGroupMaxSize"`
			NodeAutoScalingGroupMinSize int    `mapstructure:"NodeAutoScalingGroupMinSize"`
			NodeAutoScalingInitSize     int    `mapstructure:"NodeAutoScalingInitSize"`
			NodeImageID                 string `mapstructure:"NodeImageId"`
			NodeInstanceType            string `mapstructure:"NodeInstanceType"`
			NodeSpotPrice               string `mapstructure:"NodeSpotPrice"`
			NodeVolumeSize              int    `mapstructure:"NodeVolumeSize"`
			Subnets                     string `mapstructure:"Subnets"`
		}

		err = sdkCloudFormation.ParseStackParameters(stackDescriptions.Stacks[0].Parameters, &nodePoolParameters)
		if err != nil {
			nodePool.Status = eks.NodePoolStatusError
			nodePool.StatusMessage = "Retrieving node pool information failed: invalid CloudFormation stack parameters."

			continue
		}

		nodePool.Size = nodePoolParameters.NodeAutoScalingInitSize
		nodePool.Autoscaling = eks.Autoscaling{
			Enabled: nodePoolParameters.ClusterAutoscalerEnabled,
			MinSize: nodePoolParameters.NodeAutoScalingGroupMinSize,
			MaxSize: nodePoolParameters.NodeAutoScalingGroupMaxSize,
		}
		nodePool.VolumeSize = nodePoolParameters.NodeVolumeSize
		nodePool.InstanceType = nodePoolParameters.NodeInstanceType
		nodePool.Image = nodePoolParameters.NodeImageID
		nodePool.SpotPrice = nodePoolParameters.NodeSpotPrice
		nodePool.SubnetID = nodePoolParameters.Subnets // Note: currently we ensure a single value at creation.
		nodePool.Status = NewNodePoolStatusFromCFStack(aws.StringValue(stackDescriptions.Stacks[0].StackStatus))
		nodePool.StatusMessage = aws.StringValue(stackDescriptions.Stacks[0].StackStatusReason)
	}

	return nodePools, nil
}

// getNonDescrabableStackStatuses returns the status values to filter for in a ListStacks
// request to retrieve information about non-describable stacks.
func getNonDescrabableStackStatuses() (listStatuses []string) {
	statuses := cloudformation.StackStatus_Values()
	listStatuses = make([]string, 0, len(statuses))
	for _, status := range cloudformation.StackStatus_Values() {
		if strings.HasSuffix(status, "_FAILED") || // Note: failed statuses such as create failed are potentially non-describable.
			status == cloudformation.StackStatusDeleteInProgress { // Note: for some reason delete operation returns errors on describe for a couple seconds at the end.
			listStatuses = append(listStatuses, status)
		}
	}

	return listStatuses
}

// NewNodePoolStatusFromCFStack translates a CloudFormation stack status into a
// node pool status.
func NewNodePoolStatusFromCFStack(cfStackStatus string) (nodePoolStatus eks.NodePoolStatus) {
	switch {
	case strings.HasSuffix(cfStackStatus, "_FAILED"):
		return eks.NodePoolStatusError
	case strings.HasSuffix(cfStackStatus, "_COMPLETE"):
		return eks.NodePoolStatusReady
	case strings.HasSuffix(cfStackStatus, "_IN_PROGRESS"):
		if cfStackStatus == cloudformation.StackStatusCreateInProgress {
			return eks.NodePoolStatusCreating
		} else if cfStackStatus == cloudformation.StackStatusDeleteInProgress {
			return eks.NodePoolStatusDeleting
		}

		return eks.NodePoolStatusUpdating
	default:
		return eks.NodePoolStatusUnknown
	}
}
