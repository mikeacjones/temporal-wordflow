variable "project_name" {
  description = "Prefix for the ephemeral AWS and Temporal Cloud resources."
  type        = string
  default     = "temporal-word-game"
}

variable "temporal_account_id" {
  description = "Temporal Cloud Account ID. The provider refuses to mutate a different account."
  type        = string
  default     = "a2dd6"
}

variable "aws_region" {
  description = "AWS region for the ephemeral stack. Keep this colocated with the Temporal Cloud Namespace."
  type        = string
  default     = "ca-central-1"
}

variable "temporal_region" {
  description = "Temporal Cloud region for the ephemeral Namespace. Keep this colocated with the AWS provider region."
  type        = string
  default     = "aws-ca-central-1"
}

variable "worker_deployment_name" {
  description = "Temporal Worker Deployment name inside the ephemeral Namespace."
  type        = string
  default     = "temporal-word-game"
}

variable "runtime_api_key_ttl" {
  description = "Lifetime of the ephemeral Namespace runtime API key."
  type        = string
  default     = "168h"

  validation {
    condition     = can(timeadd("2026-01-01T00:00:00Z", var.runtime_api_key_ttl))
    error_message = "runtime_api_key_ttl must be a Terraform duration such as 24h or 168h."
  }
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

variable "api_memory_size" {
  description = "Memory allocated to the API Lambda. Extra CPU reduces authentication and cold-start latency."
  type        = number
  default     = 1024
}

variable "api_provisioned_concurrency" {
  description = "Warm API Lambda environments retained while the ephemeral stack exists."
  type        = number
  default     = 1
}

variable "worker_provisioned_concurrency" {
  description = "Warm Serverless Worker Lambda environments retained while the ephemeral stack exists."
  type        = number
  default     = 1
}
