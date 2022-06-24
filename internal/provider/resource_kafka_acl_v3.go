// Copyright 2021 Confluent Inc. All Rights Reserved.
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

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/antihax/optional"
	kafkarestv3 "github.com/confluentinc/ccloud-sdk-go-v2/kafkarest/v3"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	paramResourceName = "resource_name"
	paramResourceType = "resource_type"
	paramPatternType  = "pattern_type"
	paramPrincipal    = "principal"
	paramHost         = "host"
	paramOperation    = "operation"
	paramPermission   = "permission"

	principalPrefix = "User:"
)

var acceptedResourceTypes = []string{"UNKNOWN", "ANY", "TOPIC", "GROUP", "CLUSTER", "TRANSACTIONAL_ID", "DELEGATION_TOKEN"}
var acceptedPatternTypes = []string{"UNKNOWN", "ANY", "MATCH", "LITERAL", "PREFIXED"}
var acceptedOperations = []string{"UNKNOWN", "ANY", "ALL", "READ", "WRITE", "CREATE", "DELETE", "ALTER", "DESCRIBE", "CLUSTER_ACTION", "DESCRIBE_CONFIGS", "ALTER_CONFIGS", "IDEMPOTENT_WRITE"}
var acceptedPermissions = []string{"UNKNOWN", "ANY", "DENY", "ALLOW"}

func extractAcl(d *schema.ResourceData) (Acl, error) {
	resourceType, err := stringToAclResourceType(d.Get(paramResourceType).(string))
	if err != nil {
		return Acl{}, err
	}
	patternType, err := stringToAclPatternType(d.Get(paramPatternType).(string))
	if err != nil {
		return Acl{}, err
	}
	operation, err := stringToAclOperation(d.Get(paramOperation).(string))
	if err != nil {
		return Acl{}, err
	}
	permission, err := stringToAclPermission(d.Get(paramPermission).(string))
	if err != nil {
		return Acl{}, err
	}
	return Acl{
		ResourceType: resourceType,
		ResourceName: d.Get(paramResourceName).(string),
		PatternType:  patternType,
		Principal:    d.Get(paramPrincipal).(string),
		Host:         d.Get(paramHost).(string),
		Operation:    operation,
		Permission:   permission,
	}, nil
}

func kafkaAclResource() *schema.Resource {
	return &schema.Resource{
		CreateContext: kafkaAclCreate,
		ReadContext:   kafkaAclRead,
		UpdateContext: kafkaAclUpdate,
		DeleteContext: kafkaAclDelete,
		Importer: &schema.ResourceImporter{
			StateContext: kafkaAclImport,
		},
		Schema: map[string]*schema.Schema{
			paramKafkaCluster: kafkaClusterBlockSchema(),
			paramResourceType: {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				Description:  "The type of the resource.",
				ValidateFunc: validation.StringInSlice(acceptedResourceTypes, false),
			},
			paramResourceName: {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The resource name for the ACL.",
			},
			paramPatternType: {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				Description:  "The pattern type for the ACL.",
				ValidateFunc: validation.StringInSlice(acceptedPatternTypes, false),
			},
			paramPrincipal: {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				Description:  "The principal for the ACL.",
				ValidateFunc: validation.StringMatch(regexp.MustCompile("^User:(sa|u)-"), "the principal must start with 'User:sa-' or 'User:u-'. Follow the upgrade guide at https://registry.terraform.io/providers/confluentinc/confluent/latest/docs/guides/upgrade-guide-0.4.0 to upgrade to the latest version of Terraform Provider for Confluent Cloud"),
			},
			paramHost: {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "The host for the ACL.",
			},
			paramOperation: {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				Description:  "The operation type for the ACL.",
				ValidateFunc: validation.StringInSlice(acceptedOperations, false),
			},
			paramPermission: {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				Description:  "The permission for the ACL.",
				ValidateFunc: validation.StringInSlice(acceptedPermissions, false),
			},
			paramRestEndpoint: {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				Description:  "The REST endpoint of the Kafka cluster (e.g., `https://pkc-00000.us-central1.gcp.confluent.cloud:443`).",
				ValidateFunc: validation.StringMatch(regexp.MustCompile("^http"), "the REST endpoint must start with 'https://'"),
			},
			paramCredentials: credentialsSchema(),
		},
		SchemaVersion: 1,
		StateUpgraders: []schema.StateUpgrader{
			{
				Type:    kafkaClusterBlockV0().CoreConfigSchema().ImpliedType(),
				Upgrade: kafkaClusterBlockStateUpgradeV0,
				Version: 0,
			},
		},
	}
}

func kafkaAclCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	restEndpoint := d.Get(paramRestEndpoint).(string)
	clusterId := extractStringValueFromBlock(d, paramKafkaCluster, paramId)
	clusterApiKey, clusterApiSecret := extractClusterApiKeyAndApiSecret(d)
	kafkaRestClient := meta.(*Client).kafkaRestClientFactory.CreateKafkaRestClient(restEndpoint, clusterId, clusterApiKey, clusterApiSecret)
	acl, err := extractAcl(d)
	if err != nil {
		return diag.FromErr(createDescriptiveError(err))
	}
	// APIF-2038: Kafka REST API only accepts integer ID at the moment
	c := meta.(*Client)
	principalWithIntegerId, err := principalWithResourceIdToPrincipalWithIntegerId(c, acl.Principal)
	if err != nil {
		return diag.FromErr(createDescriptiveError(err))
	}
	createAclRequest := kafkarestv3.CreateAclRequestData{
		ResourceType: acl.ResourceType,
		ResourceName: acl.ResourceName,
		PatternType:  acl.PatternType,
		Principal:    principalWithIntegerId,
		Host:         acl.Host,
		Operation:    acl.Operation,
		Permission:   acl.Permission,
	}
	createAclRequestJson, err := json.Marshal(createAclRequest)
	if err != nil {
		return diag.Errorf("error creating Kafka ACLs: error marshaling %#v to json: %s", createAclRequest, createDescriptiveError(err))
	}
	tflog.Debug(ctx, fmt.Sprintf("Creating new Kafka ACLs: %s", createAclRequestJson))

	_, err = executeKafkaAclCreate(ctx, kafkaRestClient, createAclRequest)

	if err != nil {
		return diag.Errorf("error creating Kafka ACLs: %s", createDescriptiveError(err))
	}
	kafkaAclId := createKafkaAclId(kafkaRestClient.clusterId, acl)
	d.SetId(kafkaAclId)

	// https://github.com/confluentinc/terraform-provider-confluent/issues/40#issuecomment-1048782379
	time.Sleep(kafkaRestAPIWaitAfterCreate)

	tflog.Debug(ctx, fmt.Sprintf("Finished creating Kafka ACLs %q", d.Id()), map[string]interface{}{kafkaAclLoggingKey: d.Id()})

	return kafkaAclRead(ctx, d, meta)
}

func executeKafkaAclCreate(ctx context.Context, c *KafkaRestClient, requestData kafkarestv3.CreateAclRequestData) (*http.Response, error) {
	opts := &kafkarestv3.CreateKafkaV3AclsOpts{
		CreateAclRequestData: optional.NewInterface(requestData),
	}
	return c.apiClient.ACLV3Api.CreateKafkaV3Acls(c.apiContext(ctx), c.clusterId, opts)
}

func kafkaAclDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	tflog.Debug(ctx, fmt.Sprintf("Deleting Kafka ACLs %q", d.Id()), map[string]interface{}{kafkaAclLoggingKey: d.Id()})

	restEndpoint := d.Get(paramRestEndpoint).(string)
	clusterId := extractStringValueFromBlock(d, paramKafkaCluster, paramId)
	clusterApiKey, clusterApiSecret := extractClusterApiKeyAndApiSecret(d)
	kafkaRestClient := meta.(*Client).kafkaRestClientFactory.CreateKafkaRestClient(restEndpoint, clusterId, clusterApiKey, clusterApiSecret)

	acl, err := extractAcl(d)
	if err != nil {
		return diag.FromErr(createDescriptiveError(err))
	}

	// APIF-2038: Kafka REST API only accepts integer ID at the moment
	client := meta.(*Client)
	principalWithIntegerId, err := principalWithResourceIdToPrincipalWithIntegerId(client, acl.Principal)
	if err != nil {
		return diag.FromErr(createDescriptiveError(err))
	}

	opts := &kafkarestv3.DeleteKafkaV3AclsOpts{
		ResourceType: optional.NewInterface(acl.ResourceType),
		ResourceName: optional.NewString(acl.ResourceName),
		PatternType:  optional.NewInterface(acl.PatternType),
		Principal:    optional.NewString(principalWithIntegerId),
		Host:         optional.NewString(acl.Host),
		Operation:    optional.NewInterface(acl.Operation),
		Permission:   optional.NewInterface(acl.Permission),
	}

	_, _, err = kafkaRestClient.apiClient.ACLV3Api.DeleteKafkaV3Acls(kafkaRestClient.apiContext(ctx), kafkaRestClient.clusterId, opts)

	if err != nil {
		return diag.Errorf("error deleting Kafka ACLs %q: %s", d.Id(), createDescriptiveError(err))
	}

	tflog.Debug(ctx, fmt.Sprintf("Finished deleting Kafka ACLs %q", d.Id()), map[string]interface{}{kafkaAclLoggingKey: d.Id()})

	return nil
}

func executeKafkaAclRead(ctx context.Context, c *KafkaRestClient, opts *kafkarestv3.GetKafkaV3AclsOpts) (kafkarestv3.AclDataList, *http.Response, error) {
	return c.apiClient.ACLV3Api.GetKafkaV3Acls(c.apiContext(ctx), c.clusterId, opts)
}

func kafkaAclRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	tflog.Debug(ctx, fmt.Sprintf("Reading Kafka ACLs %q", d.Id()), map[string]interface{}{kafkaAclLoggingKey: d.Id()})

	restEndpoint := d.Get(paramRestEndpoint).(string)
	clusterId := extractStringValueFromBlock(d, paramKafkaCluster, paramId)
	clusterApiKey, clusterApiSecret := extractClusterApiKeyAndApiSecret(d)
	client := meta.(*Client)
	kafkaRestClient := meta.(*Client).kafkaRestClientFactory.CreateKafkaRestClient(restEndpoint, clusterId, clusterApiKey, clusterApiSecret)
	acl, err := extractAcl(d)
	if err != nil {
		return diag.FromErr(createDescriptiveError(err))
	}

	// APIF-2043: TEMPORARY CODE for v0.x.0 -> v0.4.0 migration
	// Destroy the resource in terraform state if it uses integerId for a principal.
	// This hack is necessary since terraform plan will use the principal's value (integerId) from terraform.state
	// instead of using the new provided resourceId from main.tf (the user will be forced to replace integerId with resourceId
	// that we have an input validation for using "User:sa-" for principal attribute.
	if !(strings.HasPrefix(acl.Principal, "User:sa-") || strings.HasPrefix(acl.Principal, "User:u-")) {
		d.SetId("")
		return nil
	}

	_, err = readAclAndSetAttributes(ctx, d, client, kafkaRestClient, acl)

	tflog.Debug(ctx, fmt.Sprintf("Finished reading Kafka ACLs %q", d.Id()), map[string]interface{}{kafkaAclLoggingKey: d.Id()})

	return diag.FromErr(createDescriptiveError(err))
}

func createKafkaAclId(clusterId string, acl Acl) string {
	return fmt.Sprintf("%s/%s", clusterId, strings.Join([]string{
		string(acl.ResourceType),
		acl.ResourceName,
		string(acl.PatternType),
		acl.Principal,
		acl.Host,
		string(acl.Operation),
		string(acl.Permission),
	}, "#"))
}

func readAclAndSetAttributes(ctx context.Context, d *schema.ResourceData, client *Client, c *KafkaRestClient, acl Acl) ([]*schema.ResourceData, error) {
	// APIF-2038: Kafka REST API only accepts integer ID at the moment
	principalWithIntegerId, err := principalWithResourceIdToPrincipalWithIntegerId(client, acl.Principal)
	if err != nil {
		return nil, err
	}

	opts := &kafkarestv3.GetKafkaV3AclsOpts{
		ResourceType: optional.NewInterface(acl.ResourceType),
		ResourceName: optional.NewString(acl.ResourceName),
		PatternType:  optional.NewInterface(acl.PatternType),
		Principal:    optional.NewString(principalWithIntegerId),
		Host:         optional.NewString(acl.Host),
		Operation:    optional.NewInterface(acl.Operation),
		Permission:   optional.NewInterface(acl.Permission),
	}

	remoteAcls, resp, err := executeKafkaAclRead(ctx, c, opts)
	if err != nil {
		tflog.Warn(ctx, fmt.Sprintf("Error reading Kafka ACLs %q: %s", d.Id(), createDescriptiveError(err)), map[string]interface{}{kafkaAclLoggingKey: d.Id()})

		isResourceNotFound := ResponseHasExpectedStatusCode(resp, http.StatusNotFound)
		if isResourceNotFound && !d.IsNewResource() {
			tflog.Warn(ctx, fmt.Sprintf("Removing Kafka ACLs %q in TF state because Kafka ACLs could not be found on the server", d.Id()), map[string]interface{}{kafkaAclLoggingKey: d.Id()})
			d.SetId("")
			return nil, nil
		}

		return nil, err
	}
	if len(remoteAcls.Data) == 0 {
		return nil, fmt.Errorf("error reading Kafka ACLs %q: no Kafka ACLs were matched", d.Id())
	} else if len(remoteAcls.Data) > 1 {
		// TODO: use remoteAcls.Data
		return nil, fmt.Errorf("error reading Kafka ACLs %q: multiple Kafka ACLs were matched", d.Id())
	}
	matchedAcl := remoteAcls.Data[0]
	matchedAclJson, err := json.Marshal(matchedAcl)
	if err != nil {
		return nil, fmt.Errorf("error reading Kafka ACLs: error marshaling %#v to json: %s", matchedAcl, createDescriptiveError(err))
	}
	tflog.Debug(ctx, fmt.Sprintf("Fetched Kafka ACLs %q: %s", d.Id(), matchedAclJson), map[string]interface{}{kafkaAclLoggingKey: d.Id()})

	if err := setStringAttributeInListBlockOfSizeOne(paramKafkaCluster, paramId, c.clusterId, d); err != nil {
		return nil, err
	}
	if err := d.Set(paramResourceType, matchedAcl.ResourceType); err != nil {
		return nil, err
	}
	if err := d.Set(paramResourceName, matchedAcl.ResourceName); err != nil {
		return nil, err
	}
	if err := d.Set(paramPatternType, matchedAcl.PatternType); err != nil {
		return nil, err
	}
	// Use principal with resource ID
	if err := d.Set(paramPrincipal, acl.Principal); err != nil {
		return nil, err
	}
	if err := d.Set(paramHost, matchedAcl.Host); err != nil {
		return nil, err
	}
	if err := d.Set(paramOperation, matchedAcl.Operation); err != nil {
		return nil, err
	}
	if err := d.Set(paramPermission, matchedAcl.Permission); err != nil {
		return nil, err
	}
	if err := setKafkaCredentials(c.clusterApiKey, c.clusterApiSecret, d); err != nil {
		return nil, err
	}
	if err := d.Set(paramRestEndpoint, c.restEndpoint); err != nil {
		return nil, err
	}
	d.SetId(createKafkaAclId(c.clusterId, acl))

	return []*schema.ResourceData{d}, nil
}

func kafkaAclImport(ctx context.Context, d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	tflog.Debug(ctx, fmt.Sprintf("Importing Kafka ACLs %q", d.Id()), map[string]interface{}{kafkaAclLoggingKey: d.Id()})

	kafkaImportEnvVars, err := checkEnvironmentVariablesForKafkaImportAreSet()
	if err != nil {
		return nil, err
	}

	clusterIdAndSerializedAcl := d.Id()

	parts := strings.Split(clusterIdAndSerializedAcl, "/")

	if len(parts) != 2 {
		return nil, fmt.Errorf("error importing Kafka ACLs: invalid format: expected '<Kafka cluster ID>/<resource type>#<resource name>#<pattern type>#<principal>#<host>#<operation>#<permission>'")
	}

	clusterId := parts[0]
	serializedAcl := parts[1]

	acl, err := deserializeAcl(serializedAcl)
	if err != nil {
		return nil, err
	}

	client := meta.(*Client)
	kafkaRestClient := meta.(*Client).kafkaRestClientFactory.CreateKafkaRestClient(kafkaImportEnvVars.kafkaHttpEndpoint, clusterId, kafkaImportEnvVars.kafkaApiKey, kafkaImportEnvVars.kafkaApiSecret)

	// Mark resource as new to avoid d.Set("") when getting 404
	d.MarkNewResource()
	if _, err := readAclAndSetAttributes(ctx, d, client, kafkaRestClient, acl); err != nil {
		return nil, fmt.Errorf("error importing Kafka ACLs %q: %s", d.Id(), createDescriptiveError(err))
	}
	tflog.Debug(ctx, fmt.Sprintf("Finished importing Kafka ACLs %q", d.Id()), map[string]interface{}{kafkaAclLoggingKey: d.Id()})
	return []*schema.ResourceData{d}, nil
}

func deserializeAcl(serializedAcl string) (Acl, error) {
	parts := strings.Split(serializedAcl, "#")
	if len(parts) != 7 {
		return Acl{}, fmt.Errorf("invalid format for kafka ACL import: expected '<Kafka cluster ID>/<resource type>#<resource name>#<pattern type>#<principal>#<host>#<operation>#<permission>'")
	}

	resourceType, err := stringToAclResourceType(parts[0])
	if err != nil {
		return Acl{}, err
	}
	patternType, err := stringToAclPatternType(parts[2])
	if err != nil {
		return Acl{}, err
	}
	operation, err := stringToAclOperation(parts[5])
	if err != nil {
		return Acl{}, err
	}
	permission, err := stringToAclPermission(parts[6])
	if err != nil {
		return Acl{}, err
	}

	return Acl{
		ResourceType: resourceType,
		ResourceName: parts[1],
		PatternType:  patternType,
		Principal:    parts[3],
		Host:         parts[4],
		Operation:    operation,
		Permission:   permission,
	}, nil
}

func kafkaAclUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	if d.HasChangesExcept(paramCredentials) {
		return diag.Errorf("error updating Kafka ACLs %q: only %q block can be updated for Kafka ACLs", d.Id(), paramCredentials)
	}
	return kafkaAclRead(ctx, d, meta)
}