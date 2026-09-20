output "website_url" {
  description = "Public Wordflow website and API URL."
  value       = "https://${var.domain_name}"
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
  value = var.project_name
}

output "temporal_namespace" {
  value = var.temporal_namespace
}

output "temporal_api_key_secret_arn" {
  value = aws_secretsmanager_secret.temporal_api_key.arn
}
