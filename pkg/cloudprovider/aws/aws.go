/*
Copyright 2014 The Kubernetes Authors All rights reserved.

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

package aws_cloud

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"code.google.com/p/gcfg"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/elb"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/api/resource"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/cloudprovider"

	"github.com/golang/glog"
)

const ProviderName = "aws"

// The tag name we use to differentiate multiple logically independent clusters running in the same AZ
const TagNameKubernetesCluster = "KubernetesCluster"

// Abstraction over AWS, to allow mocking/other implementations
type AWSServices interface {
	Compute(region string) (EC2, error)
	LoadBalancing(region string) (ELB, error)
	Metadata() AWSMetadata
}

// TODO: Should we rename this to AWS (EBS & ELB are not technically part of EC2)
// Abstraction over EC2, to allow mocking/other implementations
type EC2 interface {
	// Query EC2 for instances matching the filter
	Instances(instanceIds []string, filter *ec2InstanceFilter) (instances []*ec2.Instance, err error)

	// Attach a volume to an instance
	AttachVolume(volumeID, instanceId, mountDevice string) (resp *ec2.VolumeAttachment, err error)
	// Detach a volume from an instance it is attached to
	DetachVolume(request *ec2.DetachVolumeInput) (resp *ec2.VolumeAttachment, err error)
	// Lists volumes
	Volumes(volumeIDs []string, filter *ec2.Filter) (resp *ec2.DescribeVolumesOutput, err error)
	// Create an EBS volume
	CreateVolume(request *ec2.CreateVolumeInput) (resp *ec2.Volume, err error)
	// Delete an EBS volume
	DeleteVolume(volumeID string) (resp *ec2.DeleteVolumeOutput, err error)

	DescribeSecurityGroups(groupIds []string, filterName string, filterVPCId string) ([]*ec2.SecurityGroup, error)

	// TODO(justinsb): Make all of these into pass-through methods, now that we have a much better binding
	CreateSecurityGroup(*ec2.CreateSecurityGroupInput) (*ec2.CreateSecurityGroupOutput, error)
	AuthorizeSecurityGroupIngress(*ec2.AuthorizeSecurityGroupIngressInput) (*ec2.AuthorizeSecurityGroupIngressOutput, error)

	DescribeVPCs(*ec2.DescribeVPCsInput) (*ec2.DescribeVPCsOutput, error)

	DescribeSubnets(*ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error)
}

// This is a simple pass-through of the ELB client interface, which allows for testing
type ELB interface {
	CreateLoadBalancer(*elb.CreateLoadBalancerInput) (*elb.CreateLoadBalancerOutput, error)
	DeleteLoadBalancer(*elb.DeleteLoadBalancerInput) (*elb.DeleteLoadBalancerOutput, error)
	DescribeLoadBalancers(*elb.DescribeLoadBalancersInput) (*elb.DescribeLoadBalancersOutput, error)
	RegisterInstancesWithLoadBalancer(*elb.RegisterInstancesWithLoadBalancerInput) (*elb.RegisterInstancesWithLoadBalancerOutput, error)
	DeregisterInstancesFromLoadBalancer(*elb.DeregisterInstancesFromLoadBalancerInput) (*elb.DeregisterInstancesFromLoadBalancerOutput, error)
}

// Abstraction over the AWS metadata service
type AWSMetadata interface {
	// Query the EC2 metadata service (used to discover instance-id etc)
	GetMetaData(key string) ([]byte, error)
}

type VolumeOptions struct {
	CapacityMB int
}

// Volumes is an interface for managing cloud-provisioned volumes
type Volumes interface {
	// Attach the disk to the specified instance
	// instanceName can be empty to mean "the instance on which we are running"
	// Returns the device (e.g. /dev/xvdf) where we attached the volume
	AttachDisk(instanceName string, volumeName string, readOnly bool) (string, error)
	// Detach the disk from the specified instance
	// instanceName can be empty to mean "the instance on which we are running"
	DetachDisk(instanceName string, volumeName string) error

	// Create a volume with the specified options
	CreateVolume(volumeOptions *VolumeOptions) (volumeName string, err error)
	DeleteVolume(volumeName string) error
}

// AWSCloud is an implementation of Interface, TCPLoadBalancer and Instances for Amazon Web Services.
type AWSCloud struct {
	awsServices      AWSServices
	ec2              EC2
	cfg              *AWSCloudConfig
	availabilityZone string
	region           string

	filterTags map[string]string

	// The AWS instance that we are running on
	selfAWSInstance *awsInstance

	mutex sync.Mutex
	// Protects elbClients
	elbClients map[string]ELB
}

type AWSCloudConfig struct {
	Global struct {
		// TODO: Is there any use for this?  We can get it from the instance metadata service
		// Maybe if we're not running on AWS, e.g. bootstrap; for now it is not very useful
		Zone string

		KubernetesClusterTag string
	}
}

// Similar to ec2.Filter, but the filter values can be read from tests
// (ec2.Filter only has private members)
type ec2InstanceFilter struct {
	PrivateDNSName string
}

// True if the passed instance matches the filter
func (f *ec2InstanceFilter) Matches(instance *ec2.Instance) bool {
	if f.PrivateDNSName != "" && orEmpty(instance.PrivateDNSName) != f.PrivateDNSName {
		return false
	}
	return true
}

// awsSdkEC2 is an implementation of the EC2 interface, backed by aws-sdk-go
type awsSdkEC2 struct {
	ec2 *ec2.EC2
}

type awsSDKProvider struct {
	creds *credentials.Credentials
}

func (p *awsSDKProvider) Compute(regionName string) (EC2, error) {
	ec2 := &awsSdkEC2{
		ec2: ec2.New(&aws.Config{
			Region:      regionName,
			Credentials: p.creds,
		}),
	}
	return ec2, nil
}

func (p *awsSDKProvider) LoadBalancing(regionName string) (ELB, error) {
	elbClient := elb.New(&aws.Config{
		Region:      regionName,
		Credentials: p.creds,
	})
	return elbClient, nil
}

func (p *awsSDKProvider) Metadata() AWSMetadata {
	return &awsSdkMetadata{}
}

// Builds an ELB client for the specified region
func (s *AWSCloud) getELBClient(regionName string) (ELB, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	elbClient, found := s.elbClients[regionName]
	if !found {
		var err error
		elbClient, err = s.awsServices.LoadBalancing(regionName)
		if err != nil {
			return nil, err
		}
		s.elbClients[regionName] = elbClient
	}
	return elbClient, nil
}

func stringPointerArray(orig []string) []*string {
	if orig == nil {
		return nil
	}
	n := make([]*string, len(orig))
	for i := range orig {
		n[i] = &orig[i]
	}
	return n
}

func isNilOrEmpty(s *string) bool {
	return s == nil || *s == ""
}

func orEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func newEc2Filter(name string, value string) *ec2.Filter {
	filter := &ec2.Filter{
		Name: aws.String(name),
		Values: []*string{
			aws.String(value),
		},
	}
	return filter
}

// Implementation of EC2.Instances
func (self *awsSdkEC2) Instances(instanceIds []string, filter *ec2InstanceFilter) (resp []*ec2.Instance, err error) {
	var filters []*ec2.Filter
	if filter != nil && filter.PrivateDNSName != "" {
		filters = []*ec2.Filter{
			newEc2Filter("private-dns-name", filter.PrivateDNSName),
		}
	}

	fetchedInstances := []*ec2.Instance{}
	var nextToken *string

	for {
		res, err := self.ec2.DescribeInstances(&ec2.DescribeInstancesInput{
			InstanceIDs: stringPointerArray(instanceIds),
			Filters:     filters,
			NextToken:   nextToken,
		})

		if err != nil {
			return nil, err
		}

		for _, reservation := range res.Reservations {
			fetchedInstances = append(fetchedInstances, reservation.Instances...)
		}

		nextToken = res.NextToken
		if isNilOrEmpty(nextToken) {
			break
		}
	}

	return fetchedInstances, nil
}

type awsSdkMetadata struct {
}

var metadataClient = http.Client{
	Timeout: time.Second * 10,
}

// Implements AWSMetadata.GetMetaData
func (self *awsSdkMetadata) GetMetaData(key string) ([]byte, error) {
	// TODO Get an implementation of this merged into aws-sdk-go
	url := "http://169.254.169.254/latest/meta-data/" + key

	res, err := metadataClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		err = fmt.Errorf("Code %d returned for url %s", res.StatusCode, url)
		return nil, fmt.Errorf("Error querying AWS metadata for key %s: %v", key, err)
	}

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("Error querying AWS metadata for key %s: %v", key, err)
	}

	return []byte(body), nil
}

// Implements EC2.DescribeSecurityGroups
func (s *awsSdkEC2) DescribeSecurityGroups(securityGroupIds []string, filterName string, filterVPCId string) ([]*ec2.SecurityGroup, error) {
	filters := []*ec2.Filter{}
	if filterName != "" {
		filters = append(filters, newEc2Filter("group-name", filterName))
	}
	if filterVPCId != "" {
		filters = append(filters, newEc2Filter("vpc-id", filterVPCId))
	}

	request := &ec2.DescribeSecurityGroupsInput{}
	if len(securityGroupIds) != 0 {
		request.GroupIDs = []*string{}
		for _, securityGroupId := range securityGroupIds {
			request.GroupIDs = append(request.GroupIDs, &securityGroupId)
		}
	}
	if len(filters) != 0 {
		request.Filters = filters
	}

	response, err := s.ec2.DescribeSecurityGroups(request)
	if err != nil {
		glog.Error("error describing groups: ", err)
		return nil, err
	}
	return response.SecurityGroups, nil
}

func (s *awsSdkEC2) AttachVolume(volumeID, instanceId, device string) (resp *ec2.VolumeAttachment, err error) {

	request := ec2.AttachVolumeInput{
		Device:     &device,
		InstanceID: &instanceId,
		VolumeID:   &volumeID,
	}
	return s.ec2.AttachVolume(&request)
}

func (s *awsSdkEC2) DetachVolume(request *ec2.DetachVolumeInput) (*ec2.VolumeAttachment, error) {
	return s.ec2.DetachVolume(request)
}

func (s *awsSdkEC2) Volumes(volumeIDs []string, filter *ec2.Filter) (resp *ec2.DescribeVolumesOutput, err error) {
	request := ec2.DescribeVolumesInput{
		VolumeIDs: stringPointerArray(volumeIDs),
	}
	return s.ec2.DescribeVolumes(&request)
}

func (s *awsSdkEC2) CreateVolume(request *ec2.CreateVolumeInput) (resp *ec2.Volume, err error) {
	return s.ec2.CreateVolume(request)
}

func (s *awsSdkEC2) DeleteVolume(volumeID string) (resp *ec2.DeleteVolumeOutput, err error) {
	request := ec2.DeleteVolumeInput{VolumeID: &volumeID}
	return s.ec2.DeleteVolume(&request)
}

func (s *awsSdkEC2) DescribeVPCs(request *ec2.DescribeVPCsInput) (*ec2.DescribeVPCsOutput, error) {
	return s.ec2.DescribeVPCs(request)
}

func (s *awsSdkEC2) DescribeSubnets(request *ec2.DescribeSubnetsInput) (*ec2.DescribeSubnetsOutput, error) {
	return s.ec2.DescribeSubnets(request)
}

func (s *awsSdkEC2) CreateSecurityGroup(request *ec2.CreateSecurityGroupInput) (*ec2.CreateSecurityGroupOutput, error) {
	return s.ec2.CreateSecurityGroup(request)
}

func (s *awsSdkEC2) AuthorizeSecurityGroupIngress(request *ec2.AuthorizeSecurityGroupIngressInput) (*ec2.AuthorizeSecurityGroupIngressOutput, error) {
	return s.ec2.AuthorizeSecurityGroupIngress(request)
}

func init() {
	cloudprovider.RegisterCloudProvider(ProviderName, func(config io.Reader) (cloudprovider.Interface, error) {
		creds := credentials.NewChainCredentials(
			[]credentials.Provider{
				&credentials.EnvProvider{},
				&credentials.EC2RoleProvider{},
			})
		aws := &awsSDKProvider{creds: creds}
		return newAWSCloud(config, aws)
	})
}

// readAWSCloudConfig reads an instance of AWSCloudConfig from config reader.
func readAWSCloudConfig(config io.Reader, metadata AWSMetadata) (*AWSCloudConfig, error) {
	var cfg AWSCloudConfig
	var err error

	if config != nil {
		err = gcfg.ReadInto(&cfg, config)
		if err != nil {
			return nil, err
		}
	}

	if cfg.Global.Zone == "" {
		if metadata != nil {
			glog.Info("Zone not specified in configuration file; querying AWS metadata service")
			cfg.Global.Zone, err = getAvailabilityZone(metadata)
			if err != nil {
				return nil, err
			}
		}
		if cfg.Global.Zone == "" {
			return nil, fmt.Errorf("no zone specified in configuration file")
		}
	}

	return &cfg, nil
}

func getAvailabilityZone(metadata AWSMetadata) (string, error) {
	availabilityZoneBytes, err := metadata.GetMetaData("placement/availability-zone")
	if err != nil {
		return "", err
	}
	if availabilityZoneBytes == nil || len(availabilityZoneBytes) == 0 {
		return "", fmt.Errorf("Unable to determine availability-zone from instance metadata")
	}
	return string(availabilityZoneBytes), nil
}

func isRegionValid(region string) bool {
	regions := [...]string{
		"us-east-1",
		"us-west-1",
		"us-west-2",
		"eu-west-1",
		"eu-central-1",
		"ap-southeast-1",
		"ap-southeast-2",
		"ap-northeast-1",
		"sa-east-1",
	}
	for _, r := range regions {
		if r == region {
			return true
		}
	}
	return false
}

// newAWSCloud creates a new instance of AWSCloud.
// AWSProvider and instanceId are primarily for tests
func newAWSCloud(config io.Reader, awsServices AWSServices) (*AWSCloud, error) {
	metadata := awsServices.Metadata()
	cfg, err := readAWSCloudConfig(config, metadata)
	if err != nil {
		return nil, fmt.Errorf("unable to read AWS cloud provider config file: %v", err)
	}

	zone := cfg.Global.Zone
	if len(zone) <= 1 {
		return nil, fmt.Errorf("invalid AWS zone in config file: %s", zone)
	}
	regionName := zone[:len(zone)-1]

	valid := isRegionValid(regionName)
	if !valid {
		return nil, fmt.Errorf("not a valid AWS zone (unknown region): %s", zone)
	}

	ec2, err := awsServices.Compute(regionName)

	awsCloud := &AWSCloud{
		awsServices:      awsServices,
		ec2:              ec2,
		cfg:              cfg,
		region:           regionName,
		availabilityZone: zone,
		elbClients:       map[string]ELB{},
	}

	filterTags := map[string]string{}
	if cfg.Global.KubernetesClusterTag != "" {
		filterTags[TagNameKubernetesCluster] = cfg.Global.KubernetesClusterTag
	} else {
		selfInstance, err := awsCloud.getSelfAWSInstance()
		if err != nil {
			return nil, err
		}
		selfInstanceInfo, err := selfInstance.getInfo()
		if err != nil {
			return nil, err
		}
		for _, tag := range selfInstanceInfo.Tags {
			if orEmpty(tag.Key) == TagNameKubernetesCluster {
				filterTags[TagNameKubernetesCluster] = orEmpty(tag.Value)
			}
		}
	}

	awsCloud.filterTags = filterTags
	if len(filterTags) > 0 {
		glog.Infof("AWS cloud filtering on tags: %v", filterTags)
	} else {
		glog.Infof("AWS cloud - no tag filtering")
	}

	return awsCloud, nil
}

func (aws *AWSCloud) Clusters() (cloudprovider.Clusters, bool) {
	return nil, false
}

// ProviderName returns the cloud provider ID.
func (aws *AWSCloud) ProviderName() string {
	return ProviderName
}

// TCPLoadBalancer returns an implementation of TCPLoadBalancer for Amazon Web Services.
func (s *AWSCloud) TCPLoadBalancer() (cloudprovider.TCPLoadBalancer, bool) {
	return s, true
}

// Instances returns an implementation of Instances for Amazon Web Services.
func (aws *AWSCloud) Instances() (cloudprovider.Instances, bool) {
	return aws, true
}

// Zones returns an implementation of Zones for Amazon Web Services.
func (aws *AWSCloud) Zones() (cloudprovider.Zones, bool) {
	return aws, true
}

// Routes returns an implementation of Routes for Amazon Web Services.
func (aws *AWSCloud) Routes() (cloudprovider.Routes, bool) {
	return nil, false
}

// NodeAddresses is an implementation of Instances.NodeAddresses.
func (aws *AWSCloud) NodeAddresses(name string) ([]api.NodeAddress, error) {
	instance, err := aws.getInstanceByDnsName(name)
	if err != nil {
		return nil, err
	}

	addresses := []api.NodeAddress{}

	if !isNilOrEmpty(instance.PrivateIPAddress) {
		ipAddress := *instance.PrivateIPAddress
		ip := net.ParseIP(ipAddress)
		if ip == nil {
			return nil, fmt.Errorf("EC2 instance had invalid private address: %s (%s)", orEmpty(instance.InstanceID), ipAddress)
		}
		addresses = append(addresses, api.NodeAddress{Type: api.NodeInternalIP, Address: ip.String()})

		// Legacy compatibility: the private ip was the legacy host ip
		addresses = append(addresses, api.NodeAddress{Type: api.NodeLegacyHostIP, Address: ip.String()})
	}

	// TODO: Other IP addresses (multiple ips)?
	if !isNilOrEmpty(instance.PublicIPAddress) {
		ipAddress := *instance.PublicIPAddress
		ip := net.ParseIP(ipAddress)
		if ip == nil {
			return nil, fmt.Errorf("EC2 instance had invalid public address: %s (%s)", orEmpty(instance.InstanceID), ipAddress)
		}
		addresses = append(addresses, api.NodeAddress{Type: api.NodeExternalIP, Address: ip.String()})
	}

	return addresses, nil
}

// ExternalID returns the cloud provider ID of the specified instance (deprecated).
func (aws *AWSCloud) ExternalID(name string) (string, error) {
	inst, err := aws.getInstanceByDnsName(name)
	if err != nil {
		return "", err
	}
	return orEmpty(inst.InstanceID), nil
}

// InstanceID returns the cloud provider ID of the specified instance.
func (aws *AWSCloud) InstanceID(name string) (string, error) {
	inst, err := aws.getInstanceByDnsName(name)
	if err != nil {
		return "", err
	}
	// In the future it is possible to also return an endpoint as:
	// <endpoint>/<zone>/<instanceid>
	return "/" + orEmpty(inst.Placement.AvailabilityZone) + "/" + orEmpty(inst.InstanceID), nil
}

// Return the instances matching the relevant private dns name.
func (s *AWSCloud) getInstanceByDnsName(name string) (*ec2.Instance, error) {
	f := &ec2InstanceFilter{}
	f.PrivateDNSName = name

	instances, err := s.ec2.Instances(nil, f)
	if err != nil {
		return nil, err
	}

	matchingInstances := []*ec2.Instance{}
	for _, instance := range instances {
		// TODO: Push running logic down into filter?
		if !isAlive(instance) {
			continue
		}

		if orEmpty(instance.PrivateDNSName) != name {
			// TODO: Should we warn here? - the filter should have caught this
			// (this will happen in the tests if they don't fully mock the EC2 API)
			continue
		}

		matchingInstances = append(matchingInstances, instance)
	}

	if len(matchingInstances) == 0 {
		return nil, fmt.Errorf("no instances found for host: %s", name)
	}
	if len(matchingInstances) > 1 {
		return nil, fmt.Errorf("multiple instances found for host: %s", name)
	}
	return matchingInstances[0], nil
}

// Check if the instance is alive (running or pending)
// We typically ignore instances that are not alive
func isAlive(instance *ec2.Instance) bool {
	if instance.State == nil {
		glog.Warning("Instance state was unexpectedly nil: ", instance)
		return false
	}
	stateName := orEmpty(instance.State.Name)
	switch stateName {
	case "shutting-down", "terminated", "stopping", "stopped":
		return false
	case "pending", "running":
		return true
	default:
		glog.Errorf("unknown EC2 instance state: %s", stateName)
		return false
	}
}

// Return a list of instances matching regex string.
func (aws *AWSCloud) getInstancesByRegex(regex string) ([]string, error) {
	instances, err := aws.ec2.Instances(nil, nil)
	if err != nil {
		return []string{}, err
	}
	if len(instances) == 0 {
		return []string{}, fmt.Errorf("no instances returned")
	}

	if strings.HasPrefix(regex, "'") && strings.HasSuffix(regex, "'") {
		glog.Infof("Stripping quotes around regex (%s)", regex)
		regex = regex[1 : len(regex)-1]
	}

	re, err := regexp.Compile(regex)
	if err != nil {
		return []string{}, err
	}

	matchingInstances := []string{}
	for _, instance := range instances {
		// TODO: Push filtering down into EC2 API filter?
		if !isAlive(instance) {
			continue
		}

		// Only return fully-ready instances when listing instances
		// (vs a query by name, where we will return it if we find it)
		if orEmpty(instance.State.Name) == "pending" {
			glog.V(2).Infof("skipping EC2 instance (pending): %s", *instance.InstanceID)
			continue
		}

		privateDNSName := orEmpty(instance.PrivateDNSName)
		if privateDNSName == "" {
			glog.V(2).Infof("skipping EC2 instance (no PrivateDNSName): %s",
				orEmpty(instance.InstanceID))
			continue
		}

		for _, tag := range instance.Tags {
			if orEmpty(tag.Key) == "Name" && re.MatchString(orEmpty(tag.Value)) {
				matchingInstances = append(matchingInstances, privateDNSName)
				break
			}
		}
	}
	glog.V(2).Infof("Matched EC2 instances: %s", matchingInstances)
	return matchingInstances, nil
}

// List is an implementation of Instances.List.
func (aws *AWSCloud) List(filter string) ([]string, error) {
	// TODO: Should really use tag query. No need to go regexp.
	return aws.getInstancesByRegex(filter)
}

// GetNodeResources implements Instances.GetNodeResources
func (aws *AWSCloud) GetNodeResources(name string) (*api.NodeResources, error) {
	instance, err := aws.getInstanceByDnsName(name)
	if err != nil {
		return nil, err
	}

	resources, err := getResourcesByInstanceType(orEmpty(instance.InstanceType))
	if err != nil {
		return nil, err
	}

	return resources, nil
}

// Builds an api.NodeResources
// cpu is in ecus, memory is in GiB
// We pass the family in so that we could provide more info (e.g. GPU or not)
func makeNodeResources(family string, cpu float64, memory float64) (*api.NodeResources, error) {
	return &api.NodeResources{
		Capacity: api.ResourceList{
			api.ResourceCPU:    *resource.NewMilliQuantity(int64(cpu*1000), resource.DecimalSI),
			api.ResourceMemory: *resource.NewQuantity(int64(memory*1024*1024*1024), resource.BinarySI),
		},
	}, nil
}

// Maps an EC2 instance type to k8s resource information
func getResourcesByInstanceType(instanceType string) (*api.NodeResources, error) {
	// There is no API for this (that I know of)
	switch instanceType {
	// t2: Burstable
	// TODO: The ECUs are fake values (because they are burstable), so this is just a guess...
	case "t1.micro":
		return makeNodeResources("t1", 0.125, 0.615)

		// t2: Burstable
		// TODO: The ECUs are fake values (because they are burstable), so this is just a guess...
	case "t2.micro":
		return makeNodeResources("t2", 0.25, 1)
	case "t2.small":
		return makeNodeResources("t2", 0.5, 2)
	case "t2.medium":
		return makeNodeResources("t2", 1, 4)

		// c1: Compute optimized
	case "c1.medium":
		return makeNodeResources("c1", 5, 1.7)
	case "c1.xlarge":
		return makeNodeResources("c1", 20, 7)

		// cc2: Compute optimized
	case "cc2.8xlarge":
		return makeNodeResources("cc2", 88, 60.5)

		// cg1: GPU instances
	case "cg1.4xlarge":
		return makeNodeResources("cg1", 33.5, 22.5)

		// cr1: Memory optimized
	case "cr1.8xlarge":
		return makeNodeResources("cr1", 88, 244)

		// c3: Compute optimized
	case "c3.large":
		return makeNodeResources("c3", 7, 3.75)
	case "c3.xlarge":
		return makeNodeResources("c3", 14, 7.5)
	case "c3.2xlarge":
		return makeNodeResources("c3", 28, 15)
	case "c3.4xlarge":
		return makeNodeResources("c3", 55, 30)
	case "c3.8xlarge":
		return makeNodeResources("c3", 108, 60)

		// c4: Compute optimized
	case "c4.large":
		return makeNodeResources("c4", 8, 3.75)
	case "c4.xlarge":
		return makeNodeResources("c4", 16, 7.5)
	case "c4.2xlarge":
		return makeNodeResources("c4", 31, 15)
	case "c4.4xlarge":
		return makeNodeResources("c4", 62, 30)
	case "c4.8xlarge":
		return makeNodeResources("c4", 132, 60)

		// g2: GPU instances
	case "g2.2xlarge":
		return makeNodeResources("g2", 26, 15)

		// hi1: Storage optimized (SSD)
	case "hi1.4xlarge":
		return makeNodeResources("hs1", 35, 60.5)

		// hs1: Storage optimized (HDD)
	case "hs1.8xlarge":
		return makeNodeResources("hs1", 35, 117)

		// d2: Dense instances (next-gen of hs1)
	case "d2.xlarge":
		return makeNodeResources("d2", 14, 30.5)
	case "d2.2xlarge":
		return makeNodeResources("d2", 28, 61)
	case "d2.4xlarge":
		return makeNodeResources("d2", 56, 122)
	case "d2.8xlarge":
		return makeNodeResources("d2", 116, 244)

		// m1: General purpose
	case "m1.small":
		return makeNodeResources("m1", 1, 1.7)
	case "m1.medium":
		return makeNodeResources("m1", 2, 3.75)
	case "m1.large":
		return makeNodeResources("m1", 4, 7.5)
	case "m1.xlarge":
		return makeNodeResources("m1", 8, 15)

		// m2: Memory optimized
	case "m2.xlarge":
		return makeNodeResources("m2", 6.5, 17.1)
	case "m2.2xlarge":
		return makeNodeResources("m2", 13, 34.2)
	case "m2.4xlarge":
		return makeNodeResources("m2", 26, 68.4)

		// m3: General purpose
	case "m3.medium":
		return makeNodeResources("m3", 3, 3.75)
	case "m3.large":
		return makeNodeResources("m3", 6.5, 7.5)
	case "m3.xlarge":
		return makeNodeResources("m3", 13, 15)
	case "m3.2xlarge":
		return makeNodeResources("m3", 26, 30)

		// i2: Storage optimized (SSD)
	case "i2.xlarge":
		return makeNodeResources("i2", 14, 30.5)
	case "i2.2xlarge":
		return makeNodeResources("i2", 27, 61)
	case "i2.4xlarge":
		return makeNodeResources("i2", 53, 122)
	case "i2.8xlarge":
		return makeNodeResources("i2", 104, 244)

		// r3: Memory optimized
	case "r3.large":
		return makeNodeResources("r3", 6.5, 15)
	case "r3.xlarge":
		return makeNodeResources("r3", 13, 30.5)
	case "r3.2xlarge":
		return makeNodeResources("r3", 26, 61)
	case "r3.4xlarge":
		return makeNodeResources("r3", 52, 122)
	case "r3.8xlarge":
		return makeNodeResources("r3", 104, 244)

	default:
		glog.Errorf("unknown instanceType: %s", instanceType)
		return nil, nil
	}
}

// GetZone implements Zones.GetZone
func (self *AWSCloud) GetZone() (cloudprovider.Zone, error) {
	if self.availabilityZone == "" {
		// Should be unreachable
		panic("availabilityZone not set")
	}
	return cloudprovider.Zone{
		FailureDomain: self.availabilityZone,
		Region:        self.region,
	}, nil
}

// Abstraction around AWS Instance Types
// There isn't an API to get information for a particular instance type (that I know of)
type awsInstanceType struct {
}

// TODO: Also return number of mounts allowed?
func (self *awsInstanceType) getEBSMountDevices() []string {
	// See: https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/block-device-mapping-concepts.html
	devices := []string{}
	for c := 'f'; c <= 'p'; c++ {
		devices = append(devices, fmt.Sprintf("/dev/sd%c", c))
	}
	return devices
}

type awsInstance struct {
	ec2 EC2

	// id in AWS
	awsID string

	mutex sync.Mutex

	// We must cache because otherwise there is a race condition,
	// where we assign a device mapping and then get a second request before we attach the volume
	deviceMappings map[string]string
}

func newAWSInstance(ec2 EC2, awsID string) *awsInstance {
	self := &awsInstance{ec2: ec2, awsID: awsID}

	// We lazy-init deviceMappings
	self.deviceMappings = nil

	return self
}

// Gets the awsInstanceType that models the instance type of this instance
func (self *awsInstance) getInstanceType() *awsInstanceType {
	// TODO: Make this real
	awsInstanceType := &awsInstanceType{}
	return awsInstanceType
}

// Gets the full information about this instance from the EC2 API
func (self *awsInstance) getInfo() (*ec2.Instance, error) {
	instances, err := self.ec2.Instances([]string{self.awsID}, nil)
	if err != nil {
		return nil, fmt.Errorf("error querying ec2 for instance info: %v", err)
	}
	if len(instances) == 0 {
		return nil, fmt.Errorf("no instances found for instance: %s", self.awsID)
	}
	if len(instances) > 1 {
		return nil, fmt.Errorf("multiple instances found for instance: %s", self.awsID)
	}
	return instances[0], nil
}

// Assigns an unused mount device for the specified volume.
// If the volume is already assigned, this will return the existing mount device and true
func (self *awsInstance) assignMountDevice(volumeID string) (mountDevice string, alreadyAttached bool, err error) {
	instanceType := self.getInstanceType()
	if instanceType == nil {
		return "", false, fmt.Errorf("could not get instance type for instance: %s", self.awsID)
	}

	// We lock to prevent concurrent mounts from conflicting
	// We may still conflict if someone calls the API concurrently,
	// but the AWS API will then fail one of the two attach operations
	self.mutex.Lock()
	defer self.mutex.Unlock()

	// We cache both for efficiency and correctness
	if self.deviceMappings == nil {
		info, err := self.getInfo()
		if err != nil {
			return "", false, err
		}
		deviceMappings := map[string]string{}
		for _, blockDevice := range info.BlockDeviceMappings {
			deviceMappings[orEmpty(blockDevice.DeviceName)] = orEmpty(blockDevice.EBS.VolumeID)
		}
		self.deviceMappings = deviceMappings
	}

	// Check to see if this volume is already assigned a device on this machine
	for deviceName, mappingVolumeID := range self.deviceMappings {
		if volumeID == mappingVolumeID {
			glog.Warningf("Got assignment call for already-assigned volume: %s@%s", deviceName, mappingVolumeID)
			return deviceName, true, nil
		}
	}

	// Check all the valid mountpoints to see if any of them are free
	valid := instanceType.getEBSMountDevices()
	chosen := ""
	for _, device := range valid {
		_, found := self.deviceMappings[device]
		if !found {
			chosen = device
			break
		}
	}

	if chosen == "" {
		glog.Warningf("Could not assign a mount device (all in use?).  mappings=%v, valid=%v", self.deviceMappings, valid)
		return "", false, nil
	}

	self.deviceMappings[chosen] = volumeID
	glog.V(2).Infof("Assigned mount device %s -> volume %s", chosen, volumeID)

	return chosen, false, nil
}

func (self *awsInstance) releaseMountDevice(volumeID string, mountDevice string) {
	self.mutex.Lock()
	defer self.mutex.Unlock()

	existingVolumeID, found := self.deviceMappings[mountDevice]
	if !found {
		glog.Errorf("releaseMountDevice on non-allocated device")
		return
	}
	if volumeID != existingVolumeID {
		glog.Errorf("releaseMountDevice on device assigned to different volume")
		return
	}
	glog.V(2).Infof("Releasing mount device mapping: %s -> volume %s", mountDevice, volumeID)
	delete(self.deviceMappings, mountDevice)
}

type awsDisk struct {
	ec2 EC2

	// Name in k8s
	name string
	// id in AWS
	awsID string
	// az which holds the volume
	az string
}

func newAWSDisk(ec2 EC2, name string) (*awsDisk, error) {
	// name looks like aws://availability-zone/id
	url, err := url.Parse(name)
	if err != nil {
		// TODO: Maybe we should pass a URL into the Volume functions
		return nil, fmt.Errorf("Invalid disk name (%s): %v", name, err)
	}
	if url.Scheme != "aws" {
		return nil, fmt.Errorf("Invalid scheme for AWS volume (%s)", name)
	}

	awsID := url.Path
	if len(awsID) > 1 && awsID[0] == '/' {
		awsID = awsID[1:]
	}

	// TODO: Regex match?
	if strings.Contains(awsID, "/") || !strings.HasPrefix(awsID, "vol-") {
		return nil, fmt.Errorf("Invalid format for AWS volume (%s)", name)
	}
	az := url.Host
	// TODO: Better validation?
	// TODO: Default to our AZ?  Look it up?
	// TODO: Should this be a region or an AZ?
	if az == "" {
		return nil, fmt.Errorf("Invalid format for AWS volume (%s)", name)
	}
	disk := &awsDisk{ec2: ec2, name: name, awsID: awsID, az: az}
	return disk, nil
}

// Gets the full information about this volume from the EC2 API
func (self *awsDisk) getInfo() (*ec2.Volume, error) {
	resp, err := self.ec2.Volumes([]string{self.awsID}, nil)
	if err != nil {
		return nil, fmt.Errorf("error querying ec2 for volume info: %v", err)
	}
	if len(resp.Volumes) == 0 {
		return nil, fmt.Errorf("no volumes found for volume: %s", self.awsID)
	}
	if len(resp.Volumes) > 1 {
		return nil, fmt.Errorf("multiple volumes found for volume: %s", self.awsID)
	}
	return resp.Volumes[0], nil
}

func (self *awsDisk) waitForAttachmentStatus(status string) error {
	// TODO: There may be a faster way to get this when we're attaching locally
	attempt := 0
	maxAttempts := 60

	for {
		info, err := self.getInfo()
		if err != nil {
			return err
		}
		if len(info.Attachments) > 1 {
			glog.Warningf("Found multiple attachments for volume: %v", info)
		}
		attachmentStatus := ""
		for _, attachment := range info.Attachments {
			if attachmentStatus != "" {
				glog.Warning("Found multiple attachments: ", info)
			}
			if attachment.State != nil {
				attachmentStatus = *attachment.State
			} else {
				// Shouldn't happen, but don't panic...
				glog.Warning("Ignoring nil attachment state: ", attachment)
			}
		}
		if attachmentStatus == "" {
			attachmentStatus = "detached"
		}
		if attachmentStatus == status {
			return nil
		}

		glog.V(2).Infof("Waiting for volume state: actual=%s, desired=%s", attachmentStatus, status)

		attempt++
		if attempt > maxAttempts {
			glog.Warningf("Timeout waiting for volume state: actual=%s, desired=%s", attachmentStatus, status)
			return errors.New("Timeout waiting for volume state")
		}

		time.Sleep(1 * time.Second)
	}
}

// Deletes the EBS disk
func (self *awsDisk) delete() error {
	_, err := self.ec2.DeleteVolume(self.awsID)
	if err != nil {
		return fmt.Errorf("error delete EBS volumes: %v", err)
	}
	return nil
}

// Gets the awsInstance for the EC2 instance on which we are running
// may return nil in case of error
func (s *AWSCloud) getSelfAWSInstance() (*awsInstance, error) {
	// Note that we cache some state in awsInstance (mountpoints), so we must preserve the instance

	s.mutex.Lock()
	defer s.mutex.Unlock()

	i := s.selfAWSInstance
	if i == nil {
		metadata := s.awsServices.Metadata()
		instanceIdBytes, err := metadata.GetMetaData("instance-id")
		if err != nil {
			return nil, fmt.Errorf("error fetching instance-id from ec2 metadata service: %v", err)
		}
		i = newAWSInstance(s.ec2, string(instanceIdBytes))
		s.selfAWSInstance = i
	}

	return i, nil
}

// Gets the awsInstance named instanceName, or the 'self' instance if instanceName == ""
func (aws *AWSCloud) getAwsInstance(instanceName string) (*awsInstance, error) {
	var awsInstance *awsInstance
	var err error
	if instanceName == "" {
		awsInstance, err = aws.getSelfAWSInstance()
		if err != nil {
			return nil, fmt.Errorf("error getting self-instance: %v", err)
		}
	} else {
		instance, err := aws.getInstanceByDnsName(instanceName)
		if err != nil {
			return nil, fmt.Errorf("error finding instance: %v", err)
		}

		awsInstance = newAWSInstance(aws.ec2, orEmpty(instance.InstanceID))
	}

	return awsInstance, nil
}

// Implements Volumes.AttachDisk
func (aws *AWSCloud) AttachDisk(instanceName string, diskName string, readOnly bool) (string, error) {
	disk, err := newAWSDisk(aws.ec2, diskName)
	if err != nil {
		return "", err
	}

	awsInstance, err := aws.getAwsInstance(instanceName)
	if err != nil {
		return "", err
	}

	if readOnly {
		// TODO: We could enforce this when we mount the volume (?)
		// TODO: We could also snapshot the volume and attach copies of it
		return "", errors.New("AWS volumes cannot be mounted read-only")
	}

	mountDevice, alreadyAttached, err := awsInstance.assignMountDevice(disk.awsID)
	if err != nil {
		return "", err
	}

	attached := false
	defer func() {
		if !attached {
			awsInstance.releaseMountDevice(disk.awsID, mountDevice)
		}
	}()

	if !alreadyAttached {
		attachResponse, err := aws.ec2.AttachVolume(disk.awsID, awsInstance.awsID, mountDevice)
		if err != nil {
			// TODO: Check if the volume was concurrently attached?
			return "", fmt.Errorf("Error attaching EBS volume: %v", err)
		}

		glog.V(2).Info("AttachVolume request returned %v", attachResponse)
	}

	err = disk.waitForAttachmentStatus("attached")
	if err != nil {
		return "", err
	}

	attached = true

	hostDevice := mountDevice
	if strings.HasPrefix(hostDevice, "/dev/sd") {
		// Inside the instance, the mountpoint /dev/sdf looks like /dev/xvdf
		hostDevice = "/dev/xvd" + hostDevice[7:]
	}
	return hostDevice, nil
}

// Implements Volumes.DetachDisk
func (aws *AWSCloud) DetachDisk(instanceName string, diskName string) error {
	disk, err := newAWSDisk(aws.ec2, diskName)
	if err != nil {
		return err
	}

	awsInstance, err := aws.getAwsInstance(instanceName)
	if err != nil {
		return err
	}

	request := ec2.DetachVolumeInput{
		InstanceID: &awsInstance.awsID,
		VolumeID:   &disk.awsID,
	}

	response, err := aws.ec2.DetachVolume(&request)
	if err != nil {
		return fmt.Errorf("error detaching EBS volume: %v", err)
	}
	if response == nil {
		return errors.New("no response from DetachVolume")
	}
	err = disk.waitForAttachmentStatus("detached")
	if err != nil {
		return err
	}

	return err
}

// Implements Volumes.CreateVolume
func (aws *AWSCloud) CreateVolume(volumeOptions *VolumeOptions) (string, error) {
	request := &ec2.CreateVolumeInput{}
	request.AvailabilityZone = &aws.availabilityZone
	volSize := (int64(volumeOptions.CapacityMB) + 1023) / 1024
	request.Size = &volSize
	response, err := aws.ec2.CreateVolume(request)
	if err != nil {
		return "", err
	}

	az := orEmpty(response.AvailabilityZone)
	awsID := orEmpty(response.VolumeID)

	volumeName := "aws://" + az + "/" + awsID

	return volumeName, nil
}

// Implements Volumes.DeleteVolume
func (aws *AWSCloud) DeleteVolume(volumeName string) error {
	awsDisk, err := newAWSDisk(aws.ec2, volumeName)
	if err != nil {
		return err
	}
	return awsDisk.delete()
}

func (v *AWSCloud) Configure(name string, spec *api.NodeSpec) error {
	return nil
}

func (v *AWSCloud) Release(name string) error {
	return nil
}

// Gets the current load balancer state
func (s *AWSCloud) describeLoadBalancer(region, name string) (*elb.LoadBalancerDescription, error) {
	elbClient, err := s.getELBClient(region)
	if err != nil {
		return nil, err
	}

	request := &elb.DescribeLoadBalancersInput{}
	request.LoadBalancerNames = []*string{&name}

	response, err := elbClient.DescribeLoadBalancers(request)
	if err != nil {
		if awsError, ok := err.(awserr.Error); ok {
			if awsError.Code() == "LoadBalancerNotFound" {
				return nil, nil
			}
		}
		return nil, err
	}

	var ret *elb.LoadBalancerDescription
	for _, loadBalancer := range response.LoadBalancerDescriptions {
		if ret != nil {
			glog.Errorf("Found multiple load balancers with name: %s", name)
		}
		ret = loadBalancer
	}
	return ret, nil
}

// TCPLoadBalancerExists implements TCPLoadBalancer.TCPLoadBalancerExists.
func (self *AWSCloud) TCPLoadBalancerExists(name, region string) (bool, error) {
	lb, err := self.describeLoadBalancer(name, region)
	if err != nil {
		return false, err
	}

	if lb != nil {
		return true, nil
	}
	return false, nil
}

// Find the kubernetes VPC
func (self *AWSCloud) findVPC() (*ec2.VPC, error) {
	request := &ec2.DescribeVPCsInput{}

	// TODO: How do we want to identify our VPC?  Issue #6006
	name := "kubernetes-vpc"
	request.Filters = []*ec2.Filter{newEc2Filter("tag:Name", name)}

	response, err := self.ec2.DescribeVPCs(request)
	if err != nil {
		glog.Error("error listing VPCs", err)
		return nil, err
	}

	vpcs := response.VPCs
	if err != nil {
		return nil, err
	}
	if len(vpcs) == 0 {
		return nil, nil
	}
	if len(vpcs) == 1 {
		return vpcs[0], nil
	}
	return nil, fmt.Errorf("Found multiple matching VPCs for name: %s", name)
}

// Makes sure the security group allows ingress on the specified ports (with sourceIp & protocol)
// Returns true iff changes were made
// The security group must already exist
func (s *AWSCloud) ensureSecurityGroupIngess(securityGroupId string, sourceIp string, ports []*api.ServicePort) (bool, error) {
	groups, err := s.ec2.DescribeSecurityGroups([]string{securityGroupId}, "", "")
	if err != nil {
		glog.Warning("error retrieving security group", err)
		return false, err
	}

	if len(groups) == 0 {
		// We require that the security group already exist
		return false, fmt.Errorf("security group not found")
	}
	if len(groups) != 1 {
		// This should not be possible - ids should be unique
		return false, fmt.Errorf("multiple security groups found with same id")
	}
	group := groups[0]

	newPermissions := []*ec2.IPPermission{}

	for _, port := range ports {
		found := false
		portInt64 := int64(port.Port)
		protocol := strings.ToLower(string(port.Protocol))
		for _, permission := range group.IPPermissions {
			if permission.FromPort == nil || *permission.FromPort != portInt64 {
				continue
			}
			if permission.ToPort == nil || *permission.ToPort != portInt64 {
				continue
			}
			if permission.IPProtocol == nil || *permission.IPProtocol != protocol {
				continue
			}
			if len(permission.IPRanges) != 1 {
				continue
			}
			if orEmpty(permission.IPRanges[0].CIDRIP) != sourceIp {
				continue
			}
			found = true
			break
		}

		if !found {
			newPermission := &ec2.IPPermission{}
			newPermission.FromPort = &portInt64
			newPermission.ToPort = &portInt64
			newPermission.IPRanges = []*ec2.IPRange{{CIDRIP: &sourceIp}}
			newPermission.IPProtocol = &protocol

			newPermissions = append(newPermissions, newPermission)
		}
	}

	if len(newPermissions) == 0 {
		return false, nil
	}

	request := &ec2.AuthorizeSecurityGroupIngressInput{}
	request.GroupID = &securityGroupId
	request.IPPermissions = newPermissions

	_, err = s.ec2.AuthorizeSecurityGroupIngress(request)
	if err != nil {
		glog.Warning("error authorizing security group ingress", err)
		return false, err
	}

	return true, nil
}

// CreateTCPLoadBalancer implements TCPLoadBalancer.CreateTCPLoadBalancer
// TODO(justinsb): This must be idempotent
// TODO(justinsb) It is weird that these take a region.  I suspect it won't work cross-region anwyay.
func (s *AWSCloud) CreateTCPLoadBalancer(name, region string, publicIP net.IP, ports []*api.ServicePort, hosts []string, affinity api.ServiceAffinity) (*api.LoadBalancerStatus, error) {
	glog.V(2).Infof("CreateTCPLoadBalancer(%v, %v, %v, %v, %v)", name, region, publicIP, ports, hosts)

	elbClient, err := s.getELBClient(region)
	if err != nil {
		return nil, err
	}

	if affinity != api.ServiceAffinityNone {
		// ELB supports sticky sessions, but only when configured for HTTP/HTTPS
		return nil, fmt.Errorf("unsupported load balancer affinity: %v", affinity)
	}

	if publicIP != nil {
		return nil, fmt.Errorf("publicIP cannot be specified for AWS ELB")
	}

	instances, err := s.getInstancesByDnsNames(hosts)
	if err != nil {
		return nil, err
	}

	vpc, err := s.findVPC()
	if err != nil {
		glog.Error("error finding VPC", err)
		return nil, err
	}
	if vpc == nil {
		return nil, fmt.Errorf("Unable to find VPC")
	}

	// Construct list of configured subnets
	subnetIds := []*string{}
	{
		request := &ec2.DescribeSubnetsInput{}
		filters := []*ec2.Filter{}
		filters = append(filters, newEc2Filter("vpc-id", orEmpty(vpc.VPCID)))
		request.Filters = filters

		response, err := s.ec2.DescribeSubnets(request)
		if err != nil {
			glog.Error("error describing subnets: ", err)
			return nil, err
		}

		//	zones := []string{}
		for _, subnet := range response.Subnets {
			subnetIds = append(subnetIds, subnet.SubnetID)
			if !strings.HasPrefix(orEmpty(subnet.AvailabilityZone), region) {
				glog.Error("found AZ that did not match region", orEmpty(subnet.AvailabilityZone), " vs ", region)
				return nil, fmt.Errorf("invalid AZ for region")
			}
			//		zones = append(zones, subnet.AvailabilityZone)
		}
	}

	// Build the load balancer itself
	var loadBalancerName, dnsName *string
	{
		loadBalancer, err := s.describeLoadBalancer(region, name)
		if err != nil {
			return nil, err
		}

		if loadBalancer == nil {
			createRequest := &elb.CreateLoadBalancerInput{}
			createRequest.LoadBalancerName = aws.String(name)

			listeners := []*elb.Listener{}
			for _, port := range ports {
				if port.NodePort == 0 {
					glog.Errorf("Ignoring port without NodePort defined: %v", port)
					continue
				}
				instancePort := int64(port.NodePort)
				loadBalancerPort := int64(port.Port)
				protocol := strings.ToLower(string(port.Protocol))

				listener := &elb.Listener{}
				listener.InstancePort = &instancePort
				listener.LoadBalancerPort = &loadBalancerPort
				listener.Protocol = &protocol
				listener.InstanceProtocol = &protocol

				listeners = append(listeners, listener)
			}

			createRequest.Listeners = listeners

			// TODO: Should we use a better identifier (the kubernetes uuid?)

			// We are supposed to specify one subnet per AZ.
			// TODO: What happens if we have more than one subnet per AZ?
			createRequest.Subnets = subnetIds

			sgName := "k8s-elb-" + name
			sgDescription := "Security group for Kubernetes ELB " + name

			{
				// TODO: Should we do something more reliable ?? .Where("tag:kubernetes-id", kubernetesId)
				securityGroups, err := s.ec2.DescribeSecurityGroups(nil, sgName, orEmpty(vpc.VPCID))
				if err != nil {
					return nil, err
				}
				var securityGroupId *string
				for _, securityGroup := range securityGroups {
					if securityGroupId != nil {
						glog.Warning("Found multiple security groups with name:", sgName)
					}
					securityGroupId = securityGroup.GroupID
				}
				if securityGroupId == nil {
					createSecurityGroupRequest := &ec2.CreateSecurityGroupInput{}
					createSecurityGroupRequest.VPCID = vpc.VPCID
					createSecurityGroupRequest.GroupName = &sgName
					createSecurityGroupRequest.Description = &sgDescription

					createSecurityGroupResponse, err := s.ec2.CreateSecurityGroup(createSecurityGroupRequest)
					if err != nil {
						glog.Error("error creating security group: ", err)
						return nil, err
					}

					securityGroupId = createSecurityGroupResponse.GroupID
					if isNilOrEmpty(securityGroupId) {
						return nil, fmt.Errorf("created security group, but id was not returned")
					}
				}
				_, err = s.ensureSecurityGroupIngess(*securityGroupId, "0.0.0.0/0", ports)
				if err != nil {
					return nil, err
				}
				createRequest.SecurityGroups = []*string{securityGroupId}
			}

			glog.Info("Creating load balancer with name: ", createRequest.LoadBalancerName)
			createResponse, err := elbClient.CreateLoadBalancer(createRequest)
			if err != nil {
				return nil, err
			}
			dnsName = createResponse.DNSName
			loadBalancerName = createRequest.LoadBalancerName
		} else {
			// TODO: Verify that load balancer configuration matches?
			dnsName = loadBalancer.DNSName
			loadBalancerName = loadBalancer.LoadBalancerName
		}
	}

	registerRequest := &elb.RegisterInstancesWithLoadBalancerInput{}
	registerRequest.LoadBalancerName = loadBalancerName
	for _, instance := range instances {
		registerInstance := &elb.Instance{}
		registerInstance.InstanceID = instance.InstanceID
		registerRequest.Instances = append(registerRequest.Instances, registerInstance)
	}

	registerResponse, err := elbClient.RegisterInstancesWithLoadBalancer(registerRequest)
	if err != nil {
		// TODO: Is it better to delete the load balancer entirely?
		glog.Warningf("Error registering instances with load-balancer %s: %v", name, err)
	}

	glog.V(1).Infof("Updated instances registered with load-balancer %s: %v", name, registerResponse.Instances)
	glog.V(1).Infof("Loadbalancer %s has DNS name %s", name, dnsName)

	// TODO: Wait for creation?

	status := toStatus(loadBalancerName, dnsName)
	return status, nil
}

// GetTCPLoadBalancer is an implementation of TCPLoadBalancer.GetTCPLoadBalancer
func (s *AWSCloud) GetTCPLoadBalancer(name, region string) (*api.LoadBalancerStatus, bool, error) {
	lb, err := s.describeLoadBalancer(region, name)
	if err != nil {
		return nil, false, err
	}

	if lb == nil {
		return nil, false, nil
	}

	status := toStatus(lb.LoadBalancerName, lb.DNSName)
	return status, true, nil
}

func toStatus(loadBalancerName *string, dnsName *string) *api.LoadBalancerStatus {
	status := &api.LoadBalancerStatus{}

	if !isNilOrEmpty(dnsName) {
		var ingress api.LoadBalancerIngress
		ingress.Hostname = *dnsName
		status.Ingress = []api.LoadBalancerIngress{ingress}
	}

	return status
}

// EnsureTCPLoadBalancerDeleted implements TCPLoadBalancer.EnsureTCPLoadBalancerDeleted.
func (s *AWSCloud) EnsureTCPLoadBalancerDeleted(name, region string) error {
	// TODO(justinsb): Delete security group

	elbClient, err := s.getELBClient(region)
	if err != nil {
		return err
	}

	lb, err := s.describeLoadBalancer(region, name)
	if err != nil {
		return err
	}

	if lb == nil {
		glog.Info("Load balancer already deleted: ", name)
		return nil
	}

	request := &elb.DeleteLoadBalancerInput{}
	request.LoadBalancerName = lb.LoadBalancerName

	_, err = elbClient.DeleteLoadBalancer(request)
	if err != nil {
		// TODO: Check if error was because load balancer was concurrently deleted
		glog.Error("error deleting load balancer: ", err)
		return err
	}
	return nil
}

// UpdateTCPLoadBalancer implements TCPLoadBalancer.UpdateTCPLoadBalancer
func (s *AWSCloud) UpdateTCPLoadBalancer(name, region string, hosts []string) error {
	instances, err := s.getInstancesByDnsNames(hosts)
	if err != nil {
		return err
	}

	elbClient, err := s.getELBClient(region)
	if err != nil {
		return err
	}

	lb, err := s.describeLoadBalancer(region, name)
	if err != nil {
		return err
	}

	if lb == nil {
		return fmt.Errorf("Load balancer not found")
	}

	existingInstances := map[string]*elb.Instance{}
	for _, instance := range lb.Instances {
		existingInstances[orEmpty(instance.InstanceID)] = instance
	}

	wantInstances := map[string]*ec2.Instance{}
	for _, instance := range instances {
		wantInstances[orEmpty(instance.InstanceID)] = instance
	}

	addInstances := []*elb.Instance{}
	for instanceId := range wantInstances {
		addInstance := &elb.Instance{}
		addInstance.InstanceID = aws.String(instanceId)
		addInstances = append(addInstances, addInstance)
	}

	removeInstances := []*elb.Instance{}
	for instanceId := range existingInstances {
		_, found := wantInstances[instanceId]
		if !found {
			removeInstance := &elb.Instance{}
			removeInstance.InstanceID = aws.String(instanceId)
			removeInstances = append(removeInstances, removeInstance)
		}
	}

	if len(addInstances) > 0 {
		registerRequest := &elb.RegisterInstancesWithLoadBalancerInput{}
		registerRequest.Instances = addInstances
		registerRequest.LoadBalancerName = lb.LoadBalancerName
		_, err = elbClient.RegisterInstancesWithLoadBalancer(registerRequest)
		if err != nil {
			return err
		}
	}

	if len(removeInstances) > 0 {
		deregisterRequest := &elb.DeregisterInstancesFromLoadBalancerInput{}
		deregisterRequest.Instances = removeInstances
		deregisterRequest.LoadBalancerName = lb.LoadBalancerName
		_, err = elbClient.DeregisterInstancesFromLoadBalancer(deregisterRequest)
		if err != nil {
			return err
		}
	}

	return nil
}

// TODO: Make efficient
func (a *AWSCloud) getInstancesByDnsNames(names []string) ([]*ec2.Instance, error) {
	instances := []*ec2.Instance{}
	for _, name := range names {
		instance, err := a.getInstanceByDnsName(name)
		if err != nil {
			return nil, err
		}
		if instance == nil {
			return nil, fmt.Errorf("unable to find instance " + name)
		}
		instances = append(instances, instance)
	}
	return instances, nil
}
