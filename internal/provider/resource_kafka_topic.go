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
	paramKafkaCluster           = "kafka_cluster"
	paramTopicName              = "topic_name"
	paramCredentials            = "credentials"
	paramPartitionsCount        = "partitions_count"
	paramKey                    = "key"
	paramSecret                 = "secret"
	paramConfigs                = "config"
	kafkaRestAPIWaitAfterCreate = 10 * time.Second
	docsUrl                     = "https://registry.terraform.io/providers/confluentinc/confluent/latest/docs/resources/confluent_kafka_topic"
)

// https://docs.confluent.io/cloud/current/clusters/broker-config.html#custom-topic-settings-for-all-cluster-types
var editableTopicSettings = []string{"delete.retention.ms", "max.message.bytes", "max.compaction.lag.ms",
	"message.timestamp.difference.max.ms", "message.timestamp.type", "min.compaction.lag.ms", "min.insync.replicas",
	"retention.bytes", "retention.ms", "segment.bytes", "segment.ms"}

func extractConfigs(configs map[string]interface{}) []kafkarestv3.CreateTopicRequestDataConfigs {
	configResult := make([]kafkarestv3.CreateTopicRequestDataConfigs, len(configs))

	i := 0
	for name, value := range configs {
		v := value.(string)
		configResult[i] = kafkarestv3.CreateTopicRequestDataConfigs{
			Name:  name,
			Value: &v,
		}
		i += 1
	}

	return configResult
}

// TODO: remove
func extractClusterApiKeyAndApiSecret(d *schema.ResourceData) (string, string, bool) {
	clusterApiKey := extractStringValueFromBlock(d, paramCredentials, paramKey)
	clusterApiSecret := extractStringValueFromBlock(d, paramCredentials, paramSecret)
	return clusterApiKey, clusterApiSecret, clusterApiKey != ""
}

func kafkaTopicResource() *schema.Resource {
	return &schema.Resource{
		CreateContext: kafkaTopicCreate,
		ReadContext:   kafkaTopicRead,
		UpdateContext: kafkaTopicUpdate,
		DeleteContext: kafkaTopicDelete,
		Importer: &schema.ResourceImporter{
			StateContext: kafkaTopicImport,
		},
		Schema: map[string]*schema.Schema{
			paramKafkaCluster: kafkaClusterBlockSchema(),
			paramTopicName: {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				Description:  "The name of the topic, for example, `orders-1`.",
				ValidateFunc: validation.StringMatch(regexp.MustCompile(`^[a-zA-Z0-9\\._\-]+$`), "The topic name can be up to 249 characters in length, and can include the following characters: a-z, A-Z, 0-9, . (dot), _ (underscore), and - (dash)."),
			},
			paramPartitionsCount: {
				Type:         schema.TypeInt,
				Optional:     true,
				Default:      6,
				ForceNew:     true,
				Description:  "The number of partitions to create in the topic.",
				ValidateFunc: validation.IntAtLeast(1),
			},
			paramHttpEndpoint: {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				Description:  "The REST endpoint of the Kafka cluster (e.g., `https://pkc-00000.us-central1.gcp.confluent.cloud:443`).",
				ValidateFunc: validation.StringMatch(regexp.MustCompile("^http"), "the REST endpoint must start with 'https://'"),
			},
			paramConfigs: {
				Type: schema.TypeMap,
				Elem: &schema.Schema{
					Type: schema.TypeString,
				},
				Optional:    true,
				Computed:    true,
				Description: "The custom topic settings to set (e.g., `\"cleanup.policy\" = \"compact\"`).",
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

func kafkaTopicCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	httpEndpoint := d.Get(paramHttpEndpoint).(string)
	clusterId := extractStringValueFromBlock(d, paramKafkaCluster, paramId)
	clusterApiKey, clusterApiSecret, _ := extractClusterApiKeyAndApiSecret(d)
	kafkaRestClient := meta.(*Client).kafkaRestClientFactory.CreateKafkaRestClient(httpEndpoint, clusterId, clusterApiKey, clusterApiSecret)
	topicName := d.Get(paramTopicName).(string)

	createTopicRequest := kafkarestv3.CreateTopicRequestData{
		TopicName:       topicName,
		PartitionsCount: int32(d.Get(paramPartitionsCount).(int)),
		Configs:         extractConfigs(d.Get(paramConfigs).(map[string]interface{})),
	}
	createTopicRequestJson, err := json.Marshal(createTopicRequest)
	if err != nil {
		return diag.Errorf("error creating Kafka Topic: error marshaling %#v to json: %s", createTopicRequest, createDescriptiveError(err))
	}
	tflog.Debug(ctx, fmt.Sprintf("Creating new Kafka Topic: %s", createTopicRequestJson))

	createdKafkaTopic, _, err := executeKafkaTopicCreate(ctx, kafkaRestClient, createTopicRequest)

	if err != nil {
		return diag.Errorf("error creating Kafka Topic: %s", createDescriptiveError(err))
	}

	kafkaTopicId := createKafkaTopicId(kafkaRestClient.clusterId, topicName)
	d.SetId(kafkaTopicId)

	// https://github.com/confluentinc/terraform-provider-confluent/issues/40#issuecomment-1048782379
	time.Sleep(kafkaRestAPIWaitAfterCreate)

	createdKafkaTopicJson, err := json.Marshal(createdKafkaTopic)
	if err != nil {
		return diag.Errorf("error creating Kafka Topic: error marshaling %#v to json: %s", createdKafkaTopic, createDescriptiveError(err))
	}
	tflog.Debug(ctx, fmt.Sprintf("Finished creating Kafka Topic %q: %s", d.Id(), createdKafkaTopicJson), map[string]interface{}{kafkaTopicLoggingKey: d.Id()})

	return kafkaTopicRead(ctx, d, meta)
}

func executeKafkaTopicCreate(ctx context.Context, c *KafkaRestClient, requestData kafkarestv3.CreateTopicRequestData) (kafkarestv3.TopicData, *http.Response, error) {
	opts := &kafkarestv3.CreateKafkaV3TopicOpts{
		CreateTopicRequestData: optional.NewInterface(requestData),
	}
	return c.apiClient.TopicV3Api.CreateKafkaV3Topic(c.apiContext(ctx), c.clusterId, opts)
}

func kafkaTopicDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	tflog.Debug(ctx, fmt.Sprintf("Deleting Kafka Topic %q", d.Id()), map[string]interface{}{kafkaTopicLoggingKey: d.Id()})

	httpEndpoint := d.Get(paramHttpEndpoint).(string)
	clusterId := extractStringValueFromBlock(d, paramKafkaCluster, paramId)
	clusterApiKey, clusterApiSecret, _ := extractClusterApiKeyAndApiSecret(d)
	kafkaRestClient := meta.(*Client).kafkaRestClientFactory.CreateKafkaRestClient(httpEndpoint, clusterId, clusterApiKey, clusterApiSecret)
	topicName := d.Get(paramTopicName).(string)

	_, err := kafkaRestClient.apiClient.TopicV3Api.DeleteKafkaV3Topic(kafkaRestClient.apiContext(ctx), kafkaRestClient.clusterId, topicName)

	if err != nil {
		return diag.Errorf("error deleting Kafka Topic %q: %s", d.Id(), createDescriptiveError(err))
	}

	if err := waitForKafkaTopicToBeDeleted(kafkaRestClient.apiContext(ctx), kafkaRestClient, topicName); err != nil {
		return diag.Errorf("error waiting for Kafka Topic %q to be deleted: %s", d.Id(), createDescriptiveError(err))
	}

	tflog.Debug(ctx, fmt.Sprintf("Finished deleting Kafka Topic %q", d.Id()), map[string]interface{}{kafkaTopicLoggingKey: d.Id()})

	return nil
}

func kafkaTopicRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	tflog.Debug(ctx, fmt.Sprintf("Reading Kafka Topic %q", d.Id()), map[string]interface{}{kafkaTopicLoggingKey: d.Id()})

	httpEndpoint := d.Get(paramHttpEndpoint).(string)
	clusterId := extractStringValueFromBlock(d, paramKafkaCluster, paramId)
	clusterApiKey, clusterApiSecret, _ := extractClusterApiKeyAndApiSecret(d)
	kafkaRestClient := meta.(*Client).kafkaRestClientFactory.CreateKafkaRestClient(httpEndpoint, clusterId, clusterApiKey, clusterApiSecret)
	topicName := d.Get(paramTopicName).(string)

	_, err := readTopicAndSetAttributes(ctx, d, kafkaRestClient, topicName)

	tflog.Debug(ctx, fmt.Sprintf("Finished reading Kafka Topic %q", d.Id()), map[string]interface{}{kafkaTopicLoggingKey: d.Id()})

	return diag.FromErr(createDescriptiveError(err))
}

func createKafkaTopicId(clusterId, topicName string) string {
	return fmt.Sprintf("%s/%s", clusterId, topicName)
}

func credentialsSchema() *schema.Schema {
	return &schema.Schema{
		Type:        schema.TypeList,
		Required:    true,
		Description: "The Cluster API Credentials.",
		MinItems:    1,
		MaxItems:    1,
		Sensitive:   true,
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				paramKey: {
					Type:         schema.TypeString,
					Required:     true,
					Description:  "The Cluster API Key for your Confluent Cloud cluster.",
					Sensitive:    true,
					ValidateFunc: validation.StringIsNotEmpty,
				},
				paramSecret: {
					Type:         schema.TypeString,
					Required:     true,
					Description:  "The Cluster API Secret for your Confluent Cloud cluster.",
					Sensitive:    true,
					ValidateFunc: validation.StringIsNotEmpty,
				},
			},
		},
	}
}

func kafkaClusterBlockSchema() *schema.Schema {
	return &schema.Schema{
		Type: schema.TypeList,
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				paramId: {
					Type:         schema.TypeString,
					Required:     true,
					ForceNew:     true,
					Description:  "The Kafka cluster ID (e.g., `lkc-12345`).",
					ValidateFunc: validation.StringMatch(regexp.MustCompile("^lkc-"), "the Kafka cluster ID must be of the form 'lkc-'"),
				},
			},
		},
		Required: true,
		MinItems: 1,
		MaxItems: 1,
		ForceNew: true,
	}
}

func kafkaClusterIdSchema() *schema.Schema {
	return &schema.Schema{
		Type:         schema.TypeString,
		Required:     true,
		ForceNew:     true,
		Description:  "The Kafka cluster ID (e.g., `lkc-12345`).",
		ValidateFunc: validation.StringMatch(regexp.MustCompile("^lkc-"), "the Kafka cluster ID must be of the form 'lkc-'"),
	}
}

func kafkaTopicImport(ctx context.Context, d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	tflog.Debug(ctx, fmt.Sprintf("Importing Kafka Topic %q", d.Id()), map[string]interface{}{kafkaTopicLoggingKey: d.Id()})

	kafkaImportEnvVars, err := checkEnvironmentVariablesForKafkaImportAreSet()
	if err != nil {
		return nil, err
	}

	clusterIDAndTopicName := d.Id()
	parts := strings.Split(clusterIDAndTopicName, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("error importing Kafka Topic: invalid format: expected '<Kafka cluster ID>/<topic name>'")
	}

	clusterId := parts[0]
	topicName := parts[1]

	kafkaRestClient := meta.(*Client).kafkaRestClientFactory.CreateKafkaRestClient(kafkaImportEnvVars.kafkaHttpEndpoint, clusterId, kafkaImportEnvVars.kafkaApiKey, kafkaImportEnvVars.kafkaApiSecret)

	// Mark resource as new to avoid d.Set("") when getting 404
	d.MarkNewResource()
	if _, err := readTopicAndSetAttributes(ctx, d, kafkaRestClient, topicName); err != nil {
		return nil, fmt.Errorf("error importing Kafka Topic %q: %s", d.Id(), createDescriptiveError(err))
	}
	tflog.Debug(ctx, fmt.Sprintf("Finished importing Kafka Topic %q", d.Id()), map[string]interface{}{kafkaTopicLoggingKey: d.Id()})
	return []*schema.ResourceData{d}, nil
}

func readTopicAndSetAttributes(ctx context.Context, d *schema.ResourceData, c *KafkaRestClient, topicName string) ([]*schema.ResourceData, error) {
	kafkaTopic, resp, err := c.apiClient.TopicV3Api.GetKafkaV3Topic(c.apiContext(ctx), c.clusterId, topicName)
	if err != nil {
		tflog.Warn(ctx, fmt.Sprintf("Error reading Kafka Topic %q: %s", d.Id(), createDescriptiveError(err)), map[string]interface{}{kafkaTopicLoggingKey: d.Id()})

		isResourceNotFound := ResponseHasExpectedStatusCode(resp, http.StatusNotFound)
		if isResourceNotFound && !d.IsNewResource() {
			tflog.Warn(ctx, fmt.Sprintf("Removing Kafka Topic %q in TF state because Kafka Topic could not be found on the server", d.Id()), map[string]interface{}{kafkaTopicLoggingKey: d.Id()})
			d.SetId("")
			return nil, nil
		}

		return nil, err
	}
	kafkaTopicJson, err := json.Marshal(kafkaTopic)
	if err != nil {
		return nil, fmt.Errorf("error reading Kafka Topic %q: error marshaling %#v to json: %s", d.Id(), kafkaTopic, createDescriptiveError(err))
	}
	tflog.Debug(ctx, fmt.Sprintf("Fetched Kafka Topic %q: %s", d.Id(), kafkaTopicJson), map[string]interface{}{kafkaTopicLoggingKey: d.Id()})

	if err := setStringAttributeInListBlockOfSizeOne(paramKafkaCluster, paramId, c.clusterId, d); err != nil {
		return nil, err
	}
	if err := d.Set(paramTopicName, kafkaTopic.TopicName); err != nil {
		return nil, err
	}
	if err := d.Set(paramPartitionsCount, kafkaTopic.PartitionsCount); err != nil {
		return nil, err
	}

	configs, err := loadTopicConfigs(ctx, d, c, topicName)
	if err != nil {
		return nil, err
	}
	if err := d.Set(paramConfigs, configs); err != nil {
		return nil, err
	}

	if err := setKafkaCredentials(c.clusterApiKey, c.clusterApiSecret, d); err != nil {
		return nil, err
	}
	if err := d.Set(paramHttpEndpoint, c.httpEndpoint); err != nil {
		return nil, err
	}
	d.SetId(createKafkaTopicId(c.clusterId, topicName))

	return []*schema.ResourceData{d}, nil
}

func kafkaTopicUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	if d.HasChangesExcept(paramCredentials, paramConfigs) {
		return diag.Errorf("error updating Kafka Topic %q: only %q and %q blocks can be updated for Kafka Topic", d.Id(), paramCredentials, paramConfigs)
	}
	if d.HasChange(paramConfigs) {
		// TF Provider allows the following operations for editable topic settings under 'config' block:
		// 1. Adding new key value pair, for example, "retention.ms" = "600000"
		// 2. Update a value for existing key value pair, for example, "retention.ms" = "600000" -> "retention.ms" = "600001"
		// You might find the list of editable topic settings and their limits at
		// https://docs.confluent.io/cloud/current/clusters/broker-config.html#custom-topic-settings-for-all-cluster-types

		// Extract 'old' and 'new' (include changes in TF configuration) topic settings
		// * 'old' topic settings -- all topic settings from TF configuration _before_ changes / updates (currently set on Confluent Cloud)
		// * 'new' topic settings -- all topic settings from TF configuration _after_ changes
		oldTopicSettingsMap, newTopicSettingsMap := extractOldAndNewTopicSettings(d)

		// Verify that no topic settings were removed (reset to its default value) in TF configuration which is an unsupported operation at the moment
		for oldTopicSettingName := range oldTopicSettingsMap {
			if _, ok := newTopicSettingsMap[oldTopicSettingName]; !ok {
				return diag.Errorf("error updating Kafka Topic %q: reset to topic setting's default value operation (in other words, removing topic settings from 'configs' block) "+
					"is not supported at the moment. "+
					"Instead, find its default value at %s and set its current value to the default value.", d.Id(), docsUrl)
			}
		}

		// Store only topic settings that were updated in TF configuration.
		// Will be used for creating a request to Kafka REST API.
		var topicSettingsUpdateBatch []kafkarestv3.AlterConfigBatchRequestDataData

		// Verify that topics that were changed in TF configuration settings are indeed editable
		for topicSettingName, newTopicSettingValue := range newTopicSettingsMap {
			oldTopicSettingValue, ok := oldTopicSettingsMap[topicSettingName]
			isTopicSettingValueUpdated := !(ok && oldTopicSettingValue == newTopicSettingValue)
			if isTopicSettingValueUpdated {
				// operation #1 (ok = False) or operation #2 (ok = True, oldTopicSettingValue != newTopicSettingValue)
				isTopicSettingEditable := stringInSlice(topicSettingName, editableTopicSettings, false)
				if isTopicSettingEditable {
					topicSettingsUpdateBatch = append(topicSettingsUpdateBatch, kafkarestv3.AlterConfigBatchRequestDataData{
						Name:  topicSettingName,
						Value: ptr(newTopicSettingValue),
					})
				} else {
					return diag.Errorf("error updating Kafka Topic %q: %q topic setting is read-only and cannot be updated. "+
						"Read %s for more details.", d.Id(), topicSettingName, docsUrl)
				}
			}
		}

		// Construct a request for Kafka REST API
		updateTopicRequest := kafkarestv3.AlterConfigBatchRequestData{
			Data: topicSettingsUpdateBatch,
		}
		httpEndpoint := d.Get(paramHttpEndpoint).(string)
		clusterId := extractStringValueFromBlock(d, paramKafkaCluster, paramId)
		clusterApiKey, clusterApiSecret, _ := extractClusterApiKeyAndApiSecret(d)
		kafkaRestClient := meta.(*Client).kafkaRestClientFactory.CreateKafkaRestClient(httpEndpoint, clusterId, clusterApiKey, clusterApiSecret)
		topicName := d.Get(paramTopicName).(string)
		updateTopicRequestJson, err := json.Marshal(updateTopicRequest)
		if err != nil {
			return diag.Errorf("error updating Kafka Topic: error marshaling %#v to json: %s", updateTopicRequest, createDescriptiveError(err))
		}
		tflog.Debug(ctx, fmt.Sprintf("Updating Kafka Topic %q: %s", d.Id(), updateTopicRequestJson), map[string]interface{}{kafkaTopicLoggingKey: d.Id()})

		// Send a request to Kafka REST API
		_, err = executeKafkaTopicUpdate(ctx, kafkaRestClient, topicName, updateTopicRequest)
		if err != nil {
			// For example, Kafka REST API will return Bad Request if new topic setting value exceeds the max limit:
			// 400 Bad Request: Config property 'delete.retention.ms' with value '63113904003' exceeded max limit of 60566400000.
			return diag.FromErr(createDescriptiveError(err))
		}
		// Give some time to Kafka REST API to apply an update of topic settings
		time.Sleep(kafkaRestAPIWaitAfterCreate)

		// Check that topic configs update was successfully executed
		// In other words, remote topic setting values returned by Kafka REST API match topic setting values from updated TF configuration
		actualTopicSettings, err := loadTopicConfigs(ctx, d, kafkaRestClient, topicName)
		if err != nil {
			return diag.FromErr(createDescriptiveError(err))
		}

		var updatedTopicSettings, outdatedTopicSettings []string
		for _, v := range topicSettingsUpdateBatch {
			if v.Value == nil {
				// It will never happen because of the way we construct topicSettingsUpdateBatch
				continue
			}
			topicSettingName := v.Name
			expectedValue := *v.Value
			actualValue, ok := actualTopicSettings[topicSettingName]
			if ok && actualValue != expectedValue {
				outdatedTopicSettings = append(outdatedTopicSettings, topicSettingName)
			} else {
				updatedTopicSettings = append(updatedTopicSettings, topicSettingName)
			}
		}
		if len(outdatedTopicSettings) > 0 {
			diag.Errorf("error updating Kafka Topic %q: topic settings update failed for %#v. "+
				"Double check that these topic settings are indeed editable and provided target values do not exceed min/max allowed values by reading %s", d.Id(), outdatedTopicSettings, docsUrl)
		}
		updatedTopicSettingsJson, err := json.Marshal(updatedTopicSettings)
		if err != nil {
			return diag.Errorf("error updating Kafka Topic: error marshaling %#v to json: %s", updatedTopicSettings, createDescriptiveError(err))
		}
		tflog.Debug(ctx, fmt.Sprintf("Finished updating Kafka Topic %q: topic settings update has been completed for %s", d.Id(), updatedTopicSettingsJson), map[string]interface{}{kafkaTopicLoggingKey: d.Id()})
	}
	return nil
}

func executeKafkaTopicUpdate(ctx context.Context, c *KafkaRestClient, topicName string, requestData kafkarestv3.AlterConfigBatchRequestData) (*http.Response, error) {
	opts := &kafkarestv3.UpdateKafkaV3TopicConfigBatchOpts{
		AlterConfigBatchRequestData: optional.NewInterface(requestData),
	}
	return c.apiClient.ConfigsV3Api.UpdateKafkaV3TopicConfigBatch(c.apiContext(ctx), c.clusterId, topicName, opts)
}

func setKafkaCredentials(kafkaApiKey, kafkaApiSecret string, d *schema.ResourceData) error {
	return d.Set(paramCredentials, []interface{}{map[string]interface{}{
		paramKey:    kafkaApiKey,
		paramSecret: kafkaApiSecret,
	}})
}

func loadTopicConfigs(ctx context.Context, d *schema.ResourceData, c *KafkaRestClient, topicName string) (map[string]string, error) {
	topicConfigList, _, err := c.apiClient.ConfigsV3Api.ListKafkaV3TopicConfigs(c.apiContext(ctx), c.clusterId, topicName)
	if err != nil {
		return nil, fmt.Errorf("error reading Kafka Topic %q: could not load configs %s", topicName, createDescriptiveError(err))
	}

	config := make(map[string]string)
	for _, remoteConfig := range topicConfigList.Data {
		// Extract configs that were set via terraform vs set by default
		if remoteConfig.Source == kafkarestv3.CONFIGSOURCE_DYNAMIC_TOPIC_CONFIG && remoteConfig.Value != nil {
			config[remoteConfig.Name] = *remoteConfig.Value
		}
	}
	configJson, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("error reading Kafka Topic: error marshaling %#v to json: %s", config, createDescriptiveError(err))
	}
	tflog.Debug(ctx, fmt.Sprintf("Fetched Kafka Topic %q Settings: %s", d.Id(), configJson), map[string]interface{}{"kafka_acl_id": d.Id()})

	return config, nil
}

func extractOldAndNewTopicSettings(d *schema.ResourceData) (map[string]string, map[string]string) {
	oldConfigs, newConfigs := d.GetChange(paramConfigs)
	return convertToStringStringMap(oldConfigs.(map[string]interface{})), convertToStringStringMap(newConfigs.(map[string]interface{}))
}
