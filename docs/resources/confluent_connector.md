---
# generated by https://github.com/hashicorp/terraform-plugin-docs
page_title: "confluent_connector Resource - terraform-provider-confluent"
subcategory: ""
description: |-
  
---

# confluent_connector Resource

<img src="https://img.shields.io/badge/Lifecycle%20Stage-General%20Availability-%2345c6e8" alt="">

-> **Note:** `confluent_connector` resource is available in an **Preview Program** for early adopters. Preview features are introduced to gather customer feedback. This feature should be used only for evaluation and non-production testing purposes or to provide feedback to Confluent, particularly as it becomes more widely available in follow-on editions.  
**Preview Program** features are intended for evaluation use in development and testing environments only, and not for production use. The warranty, SLA, and Support Services provisions of your agreement with Confluent do not apply to Preview Program features. Preview Program features are considered to be a Proof of Concept as defined in the Confluent Cloud Terms of Service. Confluent may discontinue providing preview releases of the Preview Program features at any time in Confluent’s sole discretion.

`confluent_connector` provides a connector resource that enables creating, editing, and deleting connectors on Confluent Cloud.

-> **Note:** Use [Confluent docs](https://docs.confluent.io/cloud/current/connectors/index.html) or the [Confluent Cloud Console](https://docs.confluent.io/cloud/current/connectors/cc-s3-sink.html#using-the-ccloud-console) to pregenerate the configuration for your desired connector and to see what ACLs are required to be created.

-> **Note:** Provisioning a connector takes 15 minutes on average. Work is ongoing to decrease connector provisioning time in future releases.

## Example Usage

### Example [Datagen Source Connector](https://docs.confluent.io/cloud/current/connectors/cc-datagen-source.html) that uses a service account to communicate with your Kafka cluster
```terraform
resource "confluent_connector" "source" {
  environment {
    id = confluent_environment.staging.id
  }
  kafka_cluster {
    id = confluent_kafka_cluster.basic.id
  }

  config_sensitive = {}

  config_nonsensitive = {
    "connector.class"          = "DatagenSource"
    "name"                     = "DatagenSourceConnector_0"
    "kafka.auth.mode"          = "SERVICE_ACCOUNT"
    "kafka.service.account.id" = confluent_service_account.app-connector.id
    "kafka.topic"              = confluent_kafka_topic.orders.topic_name
    "output.data.format"       = "JSON"
    "quickstart"               = "ORDERS"
    "tasks.max"                = "1"
  }

  depends_on = [
    confluent_kafka_acl.app-connector-describe-on-cluster,
    confluent_kafka_acl.app-connector-write-on-target-topic,
    confluent_kafka_acl.app-connector-create-on-data-preview-topics,
    confluent_kafka_acl.app-connector-write-on-data-preview-topics,
  ]
}
```

### Example [Amazon S3 Sink Connector](https://docs.confluent.io/cloud/current/connectors/cc-s3-sink.html) that uses a service account to communicate with your Kafka cluster
```terraform
resource "confluent_connector" "sink" {
  environment {
    id = confluent_environment.staging.id
  }
  kafka_cluster {
    id = confluent_kafka_cluster.basic.id
  }

  config_sensitive = {
    "aws.access.key.id"     = "***REDACTED***"
    "aws.secret.access.key" = "***REDACTED***"
  }

  config_nonsensitive = {
    "topics"                   = confluent_kafka_topic.orders.topic_name
    "input.data.format"        = "JSON"
    "connector.class"          = "S3_SINK"
    "name"                     = "S3_SINKConnector_0"
    "kafka.auth.mode"          = "SERVICE_ACCOUNT"
    "kafka.service.account.id" = confluent_service_account.app-connector.id
    "s3.bucket.name"           = "<s3-bucket-name>"
    "output.data.format"       = "JSON"
    "time.interval"            = "DAILY"
    "flush.size"               = "1000"
    "tasks.max"                = "1"
  }

  depends_on = [
    confluent_kafka_acl.app-connector-describe-on-cluster,
    confluent_kafka_acl.app-connector-read-on-target-topic,
    confluent_kafka_acl.app-connector-create-on-dlq-lcc-topics,
    confluent_kafka_acl.app-connector-write-on-dlq-lcc-topics,
    confluent_kafka_acl.app-connector-create-on-success-lcc-topics,
    confluent_kafka_acl.app-connector-write-on-success-lcc-topics,
    confluent_kafka_acl.app-connector-create-on-error-lcc-topics,
    confluent_kafka_acl.app-connector-write-on-error-lcc-topics,
    confluent_kafka_acl.app-connector-read-on-connect-lcc-group,
  ]
}
```

<!-- schema generated by tfplugindocs -->
## Argument Reference

The following arguments are supported:

- `environment` (Required Configuration Block) supports the following:
  - `id` - (Required String) The ID of the Environment that the connector belongs to, for example, `env-abc123`.
- `kafka_cluster` (Optional Configuration Block) supports the following:
  - `id` - (Required String) The ID of the Kafka cluster that the connector belongs to, for example, `lkc-abc123`.
- `config_nonsensitive` - (Required Map) The custom connector _nonsensitive_ configuration settings to set:
  - `name` - (Required String) The configuration setting name, for example, `connector.class`.
  - `value` - (Required String) The configuration setting value, for example, `S3_SINK`.
- `config_sensitive` - (Required Map) The custom connector _sensitive_ configuration settings to set:
  - `name` - (Required String) The configuration setting name, for example, `aws.secret.access.key`.
  - `value` - (Required String, Sensitive) The configuration setting value, for example, `***REDACTED***`.
- `status` (Optional String) The status of the connector (one of `"NONE"`, `"PROVISIONING"`, `"RUNNING"`, `"DEGRADED"`, `"FAILED"`, `"PAUSED"`, `"DELETED"`). Pausing (`"RUNNING" -> "PAUSED"`) and resuming (`"PAUSED" -> "RUNNING"`) a connector is supported via an update operation.

-> **Note:** If there are no _sensitive_ configuration settings for your connector, set `config_sensitive = {}` explicitly.

-> **Note:** You may declare [sensitive variables](https://learn.hashicorp.com/tutorials/terraform/sensitive-variables) for secrets `config_sensitive` block and set them using environment variables (for example, `export TF_VAR_aws_access_key_id="foo"`).

## Attributes Reference

In addition to the preceding arguments, the following attributes are exported:

- `id` - (Required String) The ID of the connector, for example, `lcc-abc123`.

## Import

-> **Note:** Set `config_sensitive = {}` before importing a connector.

You can import a connector by using Environment ID, Kafka cluster ID, and connector's name, in the format `<Environment ID>/<Kafka cluster ID>/<Connector name>`, for example:

```shell
$ export CONFLUENT_CLOUD_API_KEY="<cloud_api_key>"
$ export CONFLUENT_CLOUD_API_SECRET="<cloud_api_secret>"
$ terraform import confluent_connector.my_connector "env-abc123/lkc-abc123/S3_SINKConnector_0"
```

## Getting Started

The following end-to-end examples might help to get started with `confluent_connector` resource:
* [`source-connector`](https://github.com/confluentinc/terraform-provider-confluent/tree/master/examples/configurations/source-connector)
* [`sink-connector`](https://github.com/confluentinc/terraform-provider-confluent/tree/master/examples/configurations/sink-connector)

-> **Note:** Certain connectors require additional ACL entries. See [Additional ACL entries](https://docs.confluent.io/cloud/current/connectors/service-account.html#additional-acl-entries) for more details.
