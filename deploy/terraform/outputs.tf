output "website_url" {
  description = "Public Wordflow website and API URL."
  value       = aws_apigatewayv2_api.web.api_endpoint
}

output "api_gateway_url" {
  description = "Direct API Gateway URL, retained for diagnostics."
  value       = aws_apigatewayv2_api.web.api_endpoint
}

output "aws_region" {
  value = data.aws_region.current.region
}

output "worker_build_id" {
  value = local.worker_build_id
}

output "worker_lambda_version_arn" {
  value = aws_lambda_function.worker.qualified_arn
}

output "worker_deployment" {
  value = var.worker_deployment_name
}

output "temporal_namespace" {
  value = temporalcloud_namespace.wordflow.id
}

output "temporal_address" {
  value = temporalcloud_namespace.wordflow.endpoints.grpc_address
}

output "temporal_api_key_secret_arn" {
  value = aws_secretsmanager_secret.temporal_api_key.arn
}
