variable "project_name" {
  description = "Prefix for the AWS resources and Temporal Worker Deployment."
  type        = string
  default     = "temporal-word-game"
}

variable "domain_name" {
  description = "Public domain name for the web app."
  type        = string
  default     = "games.tmprl-demo.cloud"
}

variable "hosted_zone_name" {
  description = "Existing public Route 53 hosted zone containing domain_name."
  type        = string
  default     = "tmprl-demo.cloud"
}

variable "temporal_namespace" {
  description = "Fully qualified Temporal Cloud Namespace."
  type        = string
  default     = "michaelj-durable-games.a2dd6"
}

variable "temporal_address" {
  description = "Temporal Cloud gRPC endpoint."
  type        = string
  default     = "michaelj-durable-games.a2dd6.tmprl.cloud:7233"
}

variable "temporal_api_key" {
  description = "Temporal Cloud API key. Supply it with TF_VAR_temporal_api_key; Terraform never stores it."
  type        = string
  sensitive   = true
  ephemeral   = true
}

variable "temporal_api_key_revision" {
  description = "Increment this value when rotating the Temporal API key."
  type        = number
  default     = 1
}

variable "task_queue" {
  description = "Task Queue shared by the API and Worker."
  type        = string
  default     = "temporal-word-game"
}

variable "lambda_architecture" {
  description = "Go and Lambda architecture."
  type        = string
  default     = "arm64"

  validation {
    condition     = contains(["arm64", "amd64"], var.lambda_architecture)
    error_message = "lambda_architecture must be arm64 or amd64."
  }
}

variable "log_retention_days" {
  description = "CloudWatch log retention for both Lambda functions."
  type        = number
  default     = 14
}
