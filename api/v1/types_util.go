package v1

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	consts "github.com/infinispan/infinispan-operator/controllers/constants"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
)

type ImageType string

const (
	// Container image based on JDK
	ImageTypeJVM ImageType = "JVM"

	// Container image based on Quarkus native runtime
	ImageTypeNative ImageType = "Native"
)

const (
	// PodTargetLabels labels propagated to pods
	PodTargetLabels string = "infinispan.org/podTargetLabels"
	// TargetLabels labels propagated to services/ingresses/routes
	TargetLabels string = "infinispan.org/targetLabels"
	// OperatorPodTargetLabels labels propagated by the operator to pods
	OperatorPodTargetLabels string = "infinispan.org/operatorPodTargetLabels"
	// OperatorTargetLabels labels propagated by the operator to services/ingresses/routes
	OperatorTargetLabels string = "infinispan.org/operatorTargetLabels"
	// OperatorTargetLabelsEnvVarName is the name of the envvar containing operator label/value map for services/ingresses/routes
	OperatorTargetLabelsEnvVarName string = "INFINISPAN_OPERATOR_TARGET_LABELS"
	// OperatorPodTargetLabelsEnvVarName is the name of the envvar containing operator label/value map for pods
	OperatorPodTargetLabelsEnvVarName string = "INFINISPAN_OPERATOR_POD_TARGET_LABELS"

	MaxRouteObjectNameLength = 63

	// ServiceMonitoringAnnotation defines if we need to create ServiceMonitor or not
	ServiceMonitoringAnnotation string = "infinispan.org/monitoring"

	SiteServiceNameTemplate = "%v-site"
	SiteRouteNameSuffix     = "-route-site"
	SiteServiceFQNTemplate  = "%s.%s.svc.cluster.local"

	GossipRouterDeploymentNameTemplate = "%s-router"
)

type ExternalDependencyType string

// equals compares two ConditionType's case insensitive
func (a ConditionType) equals(b ConditionType) bool {
	return strings.EqualFold(strings.ToLower(string(a)), strings.ToLower(string(b)))
}

// GetCondition return the Status of the given condition or nil
// if condition is not present
func (ispn *Infinispan) GetCondition(condition ConditionType) InfinispanCondition {
	for _, c := range ispn.Status.Conditions {
		if c.Type.equals(condition) {
			return c
		}
	}
	// Absence of condition means `False` value
	return InfinispanCondition{Type: condition, Status: metav1.ConditionFalse}
}

// HasCondition return true if a given condition exists
func (ispn *Infinispan) HasCondition(condition ConditionType) bool {
	for _, c := range ispn.Status.Conditions {
		if c.Type.equals(condition) {
			return true
		}
	}
	return false
}

// SetCondition set condition to status
func (ispn *Infinispan) SetCondition(condition ConditionType, status metav1.ConditionStatus, message string) bool {
	changed := false
	for idx := range ispn.Status.Conditions {
		c := &ispn.Status.Conditions[idx]
		if c.Type.equals(condition) {
			if c.Status != status {
				c.Status = status
				changed = true
			}
			if c.Message != message {
				c.Message = message
				changed = true
			}

			return changed
		}
	}
	ispn.Status.Conditions = append(ispn.Status.Conditions, InfinispanCondition{Type: condition, Status: status, Message: message})
	return true
}

// SetConditions set provided conditions to status
func (ispn *Infinispan) SetConditions(conds []InfinispanCondition) bool {
	changed := false
	for _, c := range conds {
		changed = changed || ispn.SetCondition(c.Type, c.Status, c.Message)
	}
	return changed
}

// RemoveCondition remove condition from Status
func (ispn *Infinispan) RemoveCondition(condition ConditionType) bool {
	for idx := range ispn.Status.Conditions {
		c := &ispn.Status.Conditions[idx]
		if c.Type.equals(condition) {
			ispn.Status.Conditions = append(ispn.Status.Conditions[:idx], ispn.Status.Conditions[idx+1:]...)
			return true
		}
	}
	return false
}

func (ispn *Infinispan) ExpectConditionStatus(expected map[ConditionType]metav1.ConditionStatus) error {
	for key, value := range expected {
		c := ispn.GetCondition(key)
		if c.Status != value {
			if c.Message == "" {
				return fmt.Errorf("key '%s' has Status '%s', expected '%s'", key, c.Status, value)
			} else {
				return fmt.Errorf("key '%s' has Status '%s', expected '%s' Reason '%s", key, c.Status, value, c.Message)
			}
		}
	}
	return nil
}

// ApplyDefaults applies default values to the Infinispan instance
func (ispn *Infinispan) ApplyDefaults() {
	if ispn.Status.Conditions == nil {
		ispn.Status.Conditions = []InfinispanCondition{}
	}
	if ispn.Spec.Service.Type == "" {
		ispn.Spec.Service.Type = ServiceTypeCache
	}
	if ispn.Spec.Service.Type == ServiceTypeCache && ispn.Spec.Service.ReplicationFactor == 0 {
		ispn.Spec.Service.ReplicationFactor = 2
	}
	if ispn.Spec.Container.Memory == "" {
		ispn.Spec.Container.Memory = consts.DefaultMemorySize.String()
	}
	if ispn.IsDataGrid() {
		if ispn.Spec.Service.Container == nil {
			ispn.Spec.Service.Container = &InfinispanServiceContainerSpec{}
		}
		if ispn.Spec.Service.Container.Storage == nil {
			ispn.Spec.Service.Container.Storage = pointer.StringPtr(consts.DefaultPVSize.String())
		}
	}
	if ispn.Spec.Security.EndpointAuthentication == nil {
		ispn.Spec.Security.EndpointAuthentication = pointer.BoolPtr(true)
	}
	if *ispn.Spec.Security.EndpointAuthentication {
		ispn.Spec.Security.EndpointSecretName = ispn.GetSecretName()
	} else if ispn.IsGeneratedSecret() {
		ispn.Spec.Security.EndpointSecretName = ""
	}
	if ispn.Spec.Upgrades == nil {
		ispn.Spec.Upgrades = &InfinispanUpgradesSpec{
			Type: UpgradeTypeShutdown,
		}
	}
	if ispn.Spec.ConfigListener == nil {
		ispn.Spec.ConfigListener = &ConfigListenerSpec{
			Enabled: true,
		}
	}
}

func (ispn *Infinispan) ApplyMonitoringAnnotation() {
	if ispn.Annotations == nil {
		ispn.Annotations = make(map[string]string)
	}
	_, ok := ispn.GetAnnotations()[ServiceMonitoringAnnotation]
	if !ok {
		ispn.Annotations[ServiceMonitoringAnnotation] = strconv.FormatBool(true)
	}
}

// ApplyEndpointEncryptionSettings compute the EndpointEncryption object
func (ispn *Infinispan) ApplyEndpointEncryptionSettings(servingCertsMode string, reqLogger logr.Logger) {
	// Populate EndpointEncryption if serving cert service is available
	encryption := ispn.Spec.Security.EndpointEncryption
	if servingCertsMode == "openshift.io" && (!ispn.IsEncryptionCertSourceDefined() || ispn.IsEncryptionCertFromService()) {
		if encryption == nil {
			encryption = &EndpointEncryption{}
			ispn.Spec.Security.EndpointEncryption = encryption
		}
		if encryption.CertServiceName == "" || encryption.Type == "" {
			reqLogger.Info("Serving certificate service present. Configuring into Infinispan CR")
			encryption.Type = CertificateSourceTypeService
			encryption.CertServiceName = "service.beta.openshift.io"
		}
		if encryption.CertSecretName == "" {
			encryption.CertSecretName = ispn.Name + "-cert-secret"
		}
	}

	if encryption != nil {
		if encryption.ClientCert == "" {
			encryption.ClientCert = ClientCertNone
		}

		if encryption.ClientCert != ClientCertNone && encryption.ClientCertSecretName == "" {
			encryption.ClientCertSecretName = ispn.Name + "-client-cert-secret"
		}
	}
}

func (ispn *Infinispan) ImageName() string {
	if ispn.Spec.Image != nil && *ispn.Spec.Image != "" {
		return *ispn.Spec.Image
	}
	return consts.DefaultImageName
}

func (ispn *Infinispan) ImageType() ImageType {
	if strings.Contains(ispn.ImageName(), consts.NativeImageMarker) {
		return ImageTypeNative
	}
	return ImageTypeJVM
}

func (ispn *Infinispan) IsDataGrid() bool {
	return ServiceTypeDataGrid == ispn.Spec.Service.Type
}

func (ispn *Infinispan) IsConditionTrue(name ConditionType) bool {
	return ispn.GetCondition(name).Status == metav1.ConditionTrue
}

func (ispn *Infinispan) IsUpgradeCondition() bool {
	return ispn.IsConditionTrue(ConditionUpgrade)
}

func (ispn *Infinispan) GetServiceExternalName() string {
	externalServiceName := fmt.Sprintf("%s-external", ispn.Name)
	if ispn.IsExposed() && ispn.GetExposeType() == ExposeTypeRoute && len(externalServiceName)+len(ispn.Namespace) >= MaxRouteObjectNameLength {
		return externalServiceName[0:MaxRouteObjectNameLength-len(ispn.Namespace)-2] + "a"
	}
	return externalServiceName
}

func (ispn *Infinispan) GetServiceName() string {
	return ispn.Name
}

func (ispn *Infinispan) GetAdminServiceName() string {
	return fmt.Sprintf("%s-admin", ispn.Name)
}

func (ispn *Infinispan) GetPingServiceName() string {
	return fmt.Sprintf("%s-ping", ispn.GetStatefulSetName())
}

// GetStatefulSetName returns the name of the StatefulSet associated with the CRD. After one or more live migrations,
// the name can change
func (ispn *Infinispan) GetStatefulSetName() string {
	statefulSetName := ispn.Status.StatefulSetName
	if statefulSetName != "" {
		return statefulSetName
	}
	return ispn.Name
}

func (ispn *Infinispan) IsCache() bool {
	return ServiceTypeCache == ispn.Spec.Service.Type
}

func (ispn *Infinispan) HasSites() bool {
	return ispn.IsDataGrid() && ispn.Spec.Service.Sites != nil
}

func (ispn *Infinispan) GetCrossSiteExposeType() CrossSiteExposeType {
	return ispn.Spec.Service.Sites.Local.Expose.Type
}

// GetRemoteSiteLocations returns remote site locations
func (ispn *Infinispan) GetRemoteSiteLocations() (remoteLocations map[string]InfinispanSiteLocationSpec) {
	remoteLocations = make(map[string]InfinispanSiteLocationSpec)
	for _, location := range ispn.Spec.Service.Sites.Locations {
		if ispn.Spec.Service.Sites.Local.Name != location.Name {
			remoteLocations[location.Name] = location
		}
	}
	return
}

// GetSiteLocationsName returns all site locations (remote and local) name
func (ispn *Infinispan) GetSiteLocationsName() (locations []string) {
	for _, location := range ispn.Spec.Service.Sites.Locations {
		if ispn.Spec.Service.Sites.Local.Name == location.Name {
			continue
		}
		locations = append(locations, location.Name)
	}
	locations = append(locations, ispn.Spec.Service.Sites.Local.Name)
	sort.Strings(locations)
	return
}

// IsExposed ...
func (ispn *Infinispan) IsExposed() bool {
	return ispn.Spec.Expose != nil && ispn.Spec.Expose.Type != ""
}

func (ispn *Infinispan) GetExposeType() ExposeType {
	return ispn.Spec.Expose.Type
}

func (ispn *Infinispan) GetSiteServiceName() string {
	return fmt.Sprintf(SiteServiceNameTemplate, ispn.Name)
}

func (ispn *Infinispan) GetRemoteSiteServiceName(locationName string) string {
	return fmt.Sprintf(SiteServiceNameTemplate, ispn.GetRemoteSiteClusterName(locationName))
}

// GetSiteRouteName returns the local Route name for cross-site replication
func (ispn *Infinispan) GetSiteRouteName() string {
	name := ispn.Name
	maxNameLength := MaxRouteObjectNameLength - len(SiteRouteNameSuffix)
	if len(name) >= maxNameLength {
		name = name[0 : maxNameLength-1]
	}
	return name + SiteRouteNameSuffix
}

// GetRemoteSiteRouteName return the remote Route name for cross-site replication
func (ispn *Infinispan) GetRemoteSiteRouteName(locationName string) string {
	name := ispn.GetRemoteSiteClusterName(locationName)
	maxNameLength := MaxRouteObjectNameLength - len(SiteRouteNameSuffix)
	if len(name) >= maxNameLength {
		name = name[0 : maxNameLength-1]
	}
	return name + SiteRouteNameSuffix
}

func (ispn *Infinispan) GetRemoteSiteServiceFQN(locationName string) string {
	return fmt.Sprintf(SiteServiceFQNTemplate, ispn.GetRemoteSiteServiceName(locationName), ispn.GetRemoteSiteNamespace(locationName))
}

func (ispn *Infinispan) GetRemoteSiteNamespace(locationName string) string {
	remoteLocation := ispn.GetRemoteSiteLocations()[locationName]
	return consts.GetWithDefault(remoteLocation.Namespace, ispn.Namespace)
}

func (ispn *Infinispan) GetRemoteSiteClusterName(locationName string) string {
	remoteLocation := ispn.GetRemoteSiteLocations()[locationName]
	return consts.GetWithDefault(remoteLocation.ClusterName, ispn.Name)
}

// GetEndpointScheme returns the protocol scheme used by the Infinispan cluster
func (ispn *Infinispan) GetEndpointScheme() string {
	endPointSchema := corev1.URISchemeHTTP
	if ispn.IsEncryptionEnabled() {
		endPointSchema = corev1.URISchemeHTTPS
	}
	return strings.ToLower(string(endPointSchema))
}

// GetSecretName returns the secret name associated with a server
func (ispn *Infinispan) GetSecretName() string {
	if ispn.Spec.Security.EndpointSecretName == "" {
		return ispn.GenerateSecretName()
	}
	return ispn.Spec.Security.EndpointSecretName
}

func (ispn *Infinispan) GenerateSecretName() string {
	return fmt.Sprintf("%v-%v", ispn.GetName(), consts.GeneratedSecretSuffix)
}

// GetAdminSecretName returns the admin secret name associated with a server
func (ispn *Infinispan) GetAdminSecretName() string {
	return fmt.Sprintf("%v-generated-operator-secret", ispn.GetName())
}

func (ispn *Infinispan) GetAuthorizationRoles() []AuthorizationRole {
	if !ispn.IsAuthorizationEnabled() {
		return make([]AuthorizationRole, 0)
	}
	return ispn.Spec.Security.Authorization.Roles
}

func (ispn *Infinispan) IsAuthorizationEnabled() bool {
	return ispn.Spec.Security.Authorization != nil && ispn.Spec.Security.Authorization.Enabled
}

func (ispn *Infinispan) IsAuthenticationEnabled() bool {
	return ispn.Spec.Security.EndpointAuthentication == nil || *ispn.Spec.Security.EndpointAuthentication
}

func (ispn *Infinispan) IsClientCertEnabled() bool {
	return ispn.IsEncryptionEnabled() && ispn.Spec.Security.EndpointEncryption.ClientCert != "" && ispn.Spec.Security.EndpointEncryption.ClientCert != ClientCertNone
}

// IsGeneratedSecret verifies that the Secret should be generated by the controller
func (ispn *Infinispan) IsGeneratedSecret() bool {
	return ispn.Spec.Security.EndpointSecretName == ispn.GenerateSecretName()
}

// GetConfigName returns the ConfigMap name for the cluster. It follows the StatefulSetName instead of the CRD name to support live migrations
func (ispn *Infinispan) GetConfigName() string {
	return fmt.Sprintf("%v-configuration", ispn.GetStatefulSetName())
}

// GetInfinispanSecuritySecretName returns the Secret containing the server certs and auth props
func (ispn *Infinispan) GetInfinispanSecuritySecretName() string {
	return fmt.Sprintf("%v-infinispan-security", ispn.Name)
}

// GetServiceMonitorName returns the ServiceMonitor name for the cluster
func (ispn *Infinispan) GetServiceMonitorName() string {
	return fmt.Sprintf("%v-monitor", ispn.Name)
}

// GetKeystoreSecretName ...
func (ispn *Infinispan) GetKeystoreSecretName() string {
	if ispn.Spec.Security.EndpointEncryption == nil {
		return ""
	}
	return ispn.Spec.Security.EndpointEncryption.CertSecretName
}

func (ispn *Infinispan) GetTruststoreSecretName() string {
	if ispn.Spec.Security.EndpointEncryption == nil {
		return ""
	}
	return ispn.Spec.Security.EndpointEncryption.ClientCertSecretName
}

// GetCpuResources returns the CPU request and limit values to be used by pods
func (spec *InfinispanContainerSpec) GetCpuResources() (requests resource.Quantity, limits resource.Quantity, err error) {
	return getRequestLimits(spec.CPU)
}

// GetMemoryResources returns the Memory request and limit values to be used by pods
func (spec *InfinispanContainerSpec) GetMemoryResources() (requests resource.Quantity, limits resource.Quantity, err error) {
	return getRequestLimits(spec.Memory)
}

func getRequestLimits(str string) (requests resource.Quantity, limits resource.Quantity, err error) {
	if str == "" {
		err = fmt.Errorf("resource string cannot be empty")
		return
	}

	parts := strings.Split(str, ":")
	if len(parts) > 2 {
		err = fmt.Errorf("unexpected resource format. Expected a string of '<limit>:<request>' or '<limit>', received: '%s'", str)
		return
	}

	limits, err = resource.ParseQuantity(parts[0])
	if err != nil {
		return
	}

	if len(parts) > 1 {
		requests, err = resource.ParseQuantity(parts[1])
		if err != nil {
			return
		}

	} else {
		requests = limits
	}
	return
}

func (ispn *Infinispan) GetJavaOptions() string {
	switch ispn.Spec.Service.Type {
	case ServiceTypeDataGrid:
		return ispn.Spec.Container.ExtraJvmOpts
	case ServiceTypeCache:
		switch ispn.ImageType() {
		case ImageTypeJVM:
			return fmt.Sprintf(consts.CacheServiceJavaOptions, consts.CacheServiceFixedMemoryXmxMb, consts.CacheServiceFixedMemoryXmxMb, consts.CacheServiceMaxRamMb,
				consts.CacheServiceMinHeapFreeRatio, consts.CacheServiceMaxHeapFreeRatio, ispn.Spec.Container.ExtraJvmOpts)
		case ImageTypeNative:
			return fmt.Sprintf(consts.CacheServiceNativeJavaOptions, consts.CacheServiceFixedMemoryXmxMb, consts.CacheServiceFixedMemoryXmxMb, ispn.Spec.Container.ExtraJvmOpts)
		}
	}
	return ""
}

// GetLogCategoriesForConfig return a map of log category for the Infinispan configuration
func (ispn *Infinispan) GetLogCategoriesForConfig() map[string]string {
	var categories map[string]LoggingLevelType
	if ispn.Spec.Logging != nil {
		categories = ispn.Spec.Logging.Categories
	}
	copied := make(map[string]string, len(categories)+1)
	copied["org.infinispan.server.core.backup"] = "debug"
	for category, level := range categories {
		copied[category] = string(level)
	}
	return copied
}

// IsWellFormed return true if cluster is well formed
func (ispn *Infinispan) IsWellFormed() bool {
	return ispn.EnsureClusterStability() == nil
}

// NotClusterFormed return true is cluster is not well formed
func (ispn *Infinispan) NotClusterFormed(pods, replicas int) bool {
	notFormed := !ispn.IsWellFormed()
	notEnoughMembers := pods < replicas
	return notFormed || notEnoughMembers
}

func (ispn *Infinispan) EnsureClusterStability() error {
	conditions := map[ConditionType]metav1.ConditionStatus{
		ConditionGracefulShutdown:   metav1.ConditionFalse,
		ConditionPrelimChecksPassed: metav1.ConditionTrue,
		ConditionUpgrade:            metav1.ConditionFalse,
		ConditionStopping:           metav1.ConditionFalse,
		ConditionWellFormed:         metav1.ConditionTrue,
	}
	return ispn.ExpectConditionStatus(conditions)
}

func (ispn *Infinispan) IsUpgradeNeeded(logger logr.Logger) bool {
	if ispn.IsUpgradeCondition() {
		if ispn.GetCondition(ConditionStopping).Status == metav1.ConditionFalse {
			if ispn.Status.ReplicasWantedAtRestart > 0 {
				logger.Info("graceful shutdown after upgrade completed, continue upgrade process")
				return true
			}
			logger.Info("replicas to restart with not yet set, wait for graceful shutdown to complete")
			return false
		}
		logger.Info("wait for graceful shutdown before update to complete")
		return false
	}

	return false
}

func (ispn *Infinispan) IsEncryptionEnabled() bool {
	ee := ispn.Spec.Security.EndpointEncryption
	return ee != nil && ee.Type != CertificateSourceTypeNoneNoEncryption
}

// IsEncryptionCertFromService returns true if encryption certificates comes from a cluster service
func (ispn *Infinispan) IsEncryptionCertFromService() bool {
	ee := ispn.Spec.Security.EndpointEncryption
	return ee != nil && (ee.Type == CertificateSourceTypeService || ee.Type == CertificateSourceTypeServiceLowCase)
}

// IsEncryptionCertSourceDefined returns true if encryption certificates source is defined
func (ispn *Infinispan) IsEncryptionCertSourceDefined() bool {
	ee := ispn.Spec.Security.EndpointEncryption
	return ee != nil && ee.Type != ""
}

// IsEphemeralStorage returns the value of ephemeralStorage if it is defined.
func (ispn *Infinispan) IsEphemeralStorage() bool {
	cont := ispn.Spec.Service.Container
	if cont != nil {
		return cont.EphemeralStorage
	}
	return false
}

// StorageClassName returns a storage class name if it defined
func (ispn *Infinispan) StorageClassName() string {
	sc := ispn.Spec.Service.Container
	if sc != nil {
		return sc.StorageClassName
	}
	return ""
}

// StorageSize returns persistence storage size if it defined
func (ispn *Infinispan) StorageSize() string {
	sc := ispn.Spec.Service.Container
	if sc != nil && sc.Storage != nil {
		return *sc.Storage
	}
	return ""
}

// AddLabelsForPods adds to the user maps the labels defined for pods in the infinispan CR. New values override old ones in map.
func (ispn *Infinispan) AddLabelsForPods(uMap map[string]string) {
	addLabelsFor(ispn, PodTargetLabels, uMap)
}

func (ispn *Infinispan) AddStatefulSetLabelForPods(uMap map[string]string) {
	uMap[consts.StatefulSetPodLabel] = ispn.Name
}

// AddLabelsForServices adds to the user maps the labels defined for services and ingresses/routes in the infinispan CR. New values override old ones in map.
func (ispn *Infinispan) AddLabelsForServices(uMap map[string]string) {
	addLabelsFor(ispn, TargetLabels, uMap)
}

// AddOperatorLabelsForPods adds to the user maps the labels defined for pods in the infinispan CR by the operator. New values override old ones in map.
func (ispn *Infinispan) AddOperatorLabelsForPods(uMap map[string]string) {
	addLabelsFor(ispn, OperatorPodTargetLabels, uMap)
}

// AddOperatorLabelsForServices adds to the user maps the labels defined for services and ingresses/routes in the infinispan CR. New values override old ones in map.
func (ispn *Infinispan) AddOperatorLabelsForServices(uMap map[string]string) {
	addLabelsFor(ispn, OperatorTargetLabels, uMap)
}

func addLabelsFor(ispn *Infinispan, target string, uMap map[string]string) {
	if ispn.Annotations == nil {
		return
	}
	labels := ispn.Annotations[target]
	for _, label := range strings.Split(labels, ",") {
		tLabel := strings.Trim(label, " ")
		if lval := strings.Trim(ispn.Labels[tLabel], " "); lval != "" {
			uMap[tLabel] = lval
		}
	}
}

// ApplyOperatorLabels applies operator labels to be propagated to pods and services
// Env vars INFINISPAN_OPERATOR_TARGET_LABELS, INFINISPAN_OPERATOR_POD_TARGET_LABELS
// must contain a json map of labels, the former will be applied to services/ingresses/routes, the latter to pods
func (ispn *Infinispan) ApplyOperatorLabels() error {
	var errStr string
	err := applyLabels(ispn, OperatorTargetLabelsEnvVarName, OperatorTargetLabels)
	if err != nil {
		errStr = fmt.Sprintf("Error unmarshalling %s environment variable: %v\n", OperatorTargetLabelsEnvVarName, err)
	}
	err = applyLabels(ispn, OperatorPodTargetLabelsEnvVarName, OperatorPodTargetLabels)
	if err != nil {
		errStr = errStr + fmt.Sprintf("Error unmarshalling %s environment variable: %v", OperatorPodTargetLabelsEnvVarName, err)
	}
	if errStr != "" {
		return errors.New(errStr)
	}
	return nil
}

func applyLabels(ispn *Infinispan, envvar, annotationName string) error {
	labels := os.Getenv(envvar)
	if labels == "" {
		return nil
	}
	labelMap := make(map[string]string)
	err := json.Unmarshal([]byte(labels), &labelMap)
	if err == nil {
		if len(labelMap) > 0 {
			if ispn.Labels == nil {
				ispn.Labels = make(map[string]string, len(labelMap))
			}
			keys := make([]string, len(labelMap))
			i := 0
			for k := range labelMap {
				keys[i] = k
				i++
			}
			sort.Strings(keys)
			var svcLabels string
			for _, k := range keys {
				ispn.Labels[k] = labelMap[k]
				svcLabels += k + ","
			}
			if ispn.Annotations == nil {
				ispn.Annotations = make(map[string]string)
			}
			ispn.Annotations[annotationName] = strings.TrimRight(svcLabels, ", ")
		}
	}
	return err
}

// HasDependenciesVolume true if custom dependencies are defined via PersistenceVolumeClaim
func (ispn *Infinispan) HasDependenciesVolume() bool {
	return ispn.Spec.Dependencies != nil && ispn.Spec.Dependencies.VolumeClaimName != ""
}

// HasExternalArtifacts true if external artifacts are defined
func (ispn *Infinispan) HasExternalArtifacts() bool {
	return ispn.Spec.Dependencies != nil && len(ispn.Spec.Dependencies.Artifacts) > 0
}

// IsServiceMonitorEnabled validates that "infinispan.org/monitoring":true annotation defines or not
func (ispn *Infinispan) IsServiceMonitorEnabled() bool {
	monitor, ok := ispn.GetAnnotations()[ServiceMonitoringAnnotation]
	if ok {
		isMonitor, err := strconv.ParseBool(monitor)
		return err == nil && isMonitor
	}
	return false
}

// GetGossipRouterDeploymentName returns the Gossip Router deployment name
func (ispn *Infinispan) GetGossipRouterDeploymentName() string {
	return fmt.Sprintf(GossipRouterDeploymentNameTemplate, ispn.Name)
}

// IsSiteTLSEnabled returns true if the TLS is enabled for cross-site replication communicate
func (ispn *Infinispan) IsSiteTLSEnabled() bool {
	return ispn.HasSites() && ispn.Spec.Service.Sites.Local.Encryption != nil && ispn.Spec.Service.Sites.Local.Encryption.TransportKeyStore != CrossSiteKeyStore{}
}

// GetSiteTLSProtocol returns the TLS protocol to be used to encrypt cross-site replication communication
func (ispn *Infinispan) GetSiteTLSProtocol() string {
	if !ispn.IsSiteTLSEnabled() {
		return ""
	}
	return consts.GetWithDefault(string(ispn.Spec.Service.Sites.Local.Encryption.Protocol), string(TLSVersion12))
}

// GetSiteTransportSecretName returns the secret name for the transport TLS keystore
func (ispn *Infinispan) GetSiteTransportSecretName() string {
	if !ispn.IsSiteTLSEnabled() {
		return ""
	}
	return ispn.Spec.Service.Sites.Local.Encryption.TransportKeyStore.SecretName
}

// GetSiteTransportKeyStoreFileName returns the keystore filename for the transport TLS configuration
func (ispn *Infinispan) GetSiteTransportKeyStoreFileName() string {
	if !ispn.IsSiteTLSEnabled() {
		return ""
	}
	return consts.GetWithDefault(ispn.Spec.Service.Sites.Local.Encryption.TransportKeyStore.Filename, consts.DefaultSiteKeyStoreFileName)
}

// GetSiteTransportKeyStoreAlias return the key alias in the keystore for the transport TLS configuration
func (ispn *Infinispan) GetSiteTransportKeyStoreAlias() string {
	if !ispn.IsSiteTLSEnabled() {
		return ""
	}
	return consts.GetWithDefault(ispn.Spec.Service.Sites.Local.Encryption.TransportKeyStore.Alias, consts.DefaultSiteTransportKeyStoreAlias)
}

// GetSiteRouterSecretName returns the secret name for the router TLS keystore
func (ispn *Infinispan) GetSiteRouterSecretName() string {
	if !ispn.IsSiteTLSEnabled() {
		return ""
	}
	return ispn.Spec.Service.Sites.Local.Encryption.RouterKeyStore.SecretName
}

// GetSiteRouterKeyStoreFileName returns the keystore filename for the router TLS configuration
func (ispn *Infinispan) GetSiteRouterKeyStoreFileName() string {
	if !ispn.IsSiteTLSEnabled() {
		return ""
	}
	return consts.GetWithDefault(ispn.Spec.Service.Sites.Local.Encryption.RouterKeyStore.Filename, consts.DefaultSiteKeyStoreFileName)
}

// GetSiteRouterKeyStoreAlias return the key alias in the keystore for the router TLS configuration
func (ispn *Infinispan) GetSiteRouterKeyStoreAlias() string {
	if !ispn.IsSiteTLSEnabled() {
		return ""
	}
	return consts.GetWithDefault(ispn.Spec.Service.Sites.Local.Encryption.RouterKeyStore.Alias, consts.DefaultSiteRouterKeyStoreAlias)
}

// GetSiteTrustoreSecretName returns the secret name with the truststore for the transport and router TLS keystore
func (ispn *Infinispan) GetSiteTrustoreSecretName() string {
	if !ispn.IsSiteTLSEnabled() || ispn.Spec.Service.Sites.Local.Encryption.TrustStore == nil {
		return ""
	}
	return ispn.Spec.Service.Sites.Local.Encryption.TrustStore.SecretName
}

// GetSiteTrustStoreFileName returns the truststore filename for the transport and router TLS configuration
func (ispn *Infinispan) GetSiteTrustStoreFileName() string {
	if !ispn.IsSiteTLSEnabled() {
		return ""
	}
	tls := ispn.Spec.Service.Sites.Local.Encryption
	if tls.TrustStore == nil {
		return consts.DefaultSiteTrustStoreFileName
	}
	return consts.GetWithDefault(tls.TrustStore.Filename, consts.DefaultSiteTrustStoreFileName)
}

func (ispn *Infinispan) IsConfigListenerEnabled() bool {
	return ispn.Spec.ConfigListener != nil && ispn.Spec.ConfigListener.Enabled
}

func (ispn *Infinispan) GetConfigListenerName() string {
	return fmt.Sprintf("%s-config-listener", ispn.Name)
}
