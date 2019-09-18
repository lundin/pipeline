// Copyright © 2019 Banzai Cloud
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

package monitoring

import (
	"context"
	"fmt"

	"emperror.dev/errors"
	pkgEndpoints "github.com/banzaicloud/pipeline/internal/cluster/endpoints"
	"github.com/banzaicloud/pipeline/internal/clusterfeature"
	"github.com/banzaicloud/pipeline/internal/clusterfeature/clusterfeatureadapter"
	"github.com/banzaicloud/pipeline/internal/clusterfeature/features"
	"github.com/banzaicloud/pipeline/internal/common"
	pkgHelm "github.com/banzaicloud/pipeline/pkg/helm"
)

// FeatureManager implements the Monitoring feature manager
type FeatureManager struct {
	clusterGetter    clusterfeatureadapter.ClusterGetter
	secretStore      features.SecretStore
	endpointsManager *pkgEndpoints.EndpointManager
	helmService      features.HelmService
	logger           common.Logger
}

func MakeFeatureManager(
	clusterGetter clusterfeatureadapter.ClusterGetter,
	secretStore features.SecretStore,
	endpointsManager *pkgEndpoints.EndpointManager,
	helmService features.HelmService,
	logger common.Logger,
) FeatureManager {
	return FeatureManager{
		clusterGetter:    clusterGetter,
		secretStore:      secretStore,
		endpointsManager: endpointsManager,
		helmService:      helmService,
		logger:           logger,
	}
}

// Name returns the feature's name
func (m FeatureManager) Name() string {
	return featureName
}

// GetOutput returns the Monitoring feature's output
func (m FeatureManager) GetOutput(ctx context.Context, clusterID uint, spec clusterfeature.FeatureSpec) (clusterfeature.FeatureOutput, error) {
	boundSpec, err := bindFeatureSpec(spec)
	if err != nil {
		return nil, clusterfeature.InvalidFeatureSpecError{
			FeatureName: featureName,
			Problem:     err.Error(),
		}
	}

	cluster, err := m.clusterGetter.GetClusterByIDOnly(ctx, clusterID)
	if err != nil {
		return nil, errors.WrapIf(err, "failed to get cluster")
	}

	out := make(map[string]interface{})

	kubeConfig, err := cluster.GetK8sConfig()
	if err != nil {
		return nil, errors.WrapIf(err, "failed to get K8S config")
	}

	endpoints, err := m.endpointsManager.List(kubeConfig, prometheusOperatorReleaseName)
	if err != nil {
		m.logger.Warn(fmt.Sprintf("failed to list endpoints: %s", err.Error()))
	}

	pushgatewayDeployment, err := m.helmService.GetDeployment(ctx, clusterID, prometheusPushgatewayReleaseName)
	if err != nil {
		m.logger.Warn(fmt.Sprintf("failed to get pushgateway details: %s", err.Error()))
	}

	operatorDeployment, err := m.helmService.GetDeployment(ctx, clusterID, prometheusOperatorReleaseName)
	if err != nil {
		m.logger.Warn(fmt.Sprintf("failed to get deployment details: %s", err.Error()))
	}

	var operatorValues map[string]interface{}
	if operatorDeployment != nil {
		operatorValues = operatorDeployment.Values
	}

	var pushgatewayValues map[string]interface{}
	if pushgatewayDeployment != nil {
		pushgatewayValues = pushgatewayDeployment.Values
	}

	// set Grafana outputs
	out["grafana"] = m.getComponentOutput(ctx, clusterID, newGrafanaOutputHelper(boundSpec), endpoints, operatorValues)

	// set Prometheus outputs
	out["prometheus"] = m.getComponentOutput(ctx, clusterID, newPrometheusOutputHelper(boundSpec), endpoints, operatorValues)

	// set Alertmanager output
	out["alertmanager"] = m.getComponentOutput(ctx, clusterID, newAlertmanagerOutputHelper(boundSpec), endpoints, operatorValues)

	// set Pushgateway output
	out["pushgateway"] = m.getPushgatewayOutput(ctx, pushgatewayValues)

	// set version(s)
	_, chartVersion := getPrometheusOperatorChartParams()
	out["prometheusOperator"] = map[string]interface{}{
		"version": chartVersion,
	}

	return out, nil
}

// ValidateSpec validates a Monitoring feature specification
func (m FeatureManager) ValidateSpec(ctx context.Context, spec clusterfeature.FeatureSpec) error {
	boundSpec, err := bindFeatureSpec(spec)
	if err != nil {
		return clusterfeature.InvalidFeatureSpecError{
			FeatureName: featureName,
			Problem:     err.Error(),
		}
	}

	if err := boundSpec.Validate(); err != nil {
		return clusterfeature.InvalidFeatureSpecError{
			FeatureName: featureName,
			Problem:     err.Error(),
		}
	}

	return nil
}

// PrepareSpec makes certain preparations to the spec before it's sent to be applied
func (m FeatureManager) PrepareSpec(ctx context.Context, spec clusterfeature.FeatureSpec) (clusterfeature.FeatureSpec, error) {
	return spec, nil
}

func (m FeatureManager) BeforeSave(ctx context.Context, clusterID uint, spec clusterfeature.FeatureSpec) (clusterfeature.FeatureSpec, error) {
	return spec, nil
}

func (m FeatureManager) getComponentOutput(
	ctx context.Context,
	clusterID uint,
	helper outputHelper,
	endpoints []*pkgHelm.EndpointItem,
	deploymentValues map[string]interface{},
) map[string]interface{} {
	var out = make(map[string]interface{})

	o := outputManager{
		outputHelper: helper,
		secretStore:  m.secretStore,
		logger:       m.logger,
	}

	writeSecretID(ctx, o, clusterID, out)
	writeUrl(o, endpoints, out)
	writeVersion(o, deploymentValues, out)

	return out
}

func (m FeatureManager) getPushgatewayOutput(
	ctx context.Context,
	deploymentValues map[string]interface{},
) map[string]interface{} {
	if deploymentValues != nil {
		var output = make(map[string]interface{})
		// set Pushgateway version
		if image, ok := deploymentValues["image"].(map[string]interface{}); ok {
			output["version"] = image["tag"]
		}

		return output
	}

	return nil
}
