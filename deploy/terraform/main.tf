data "aws_caller_identity" "current" {}
data "aws_region" "current" {}

data "external" "git" {
  program     = ["bash", "${abspath(path.module)}/scripts/git-version.sh"]
  working_dir = local.repo_root
}

locals {
  repo_root            = abspath("${path.module}/../..")
  dist_dir             = abspath("${path.module}/dist")
  stack_name           = "${var.project_name}-${random_string.stack_suffix.result}"
  worker_function_name = "${local.stack_name}-worker"
  api_function_name    = "${local.stack_name}-api"
  lambda_architectures = var.lambda_architecture == "amd64" ? ["x86_64"] : ["arm64"]
  temporal_principal_arns = [
    "arn:aws:iam::902542641901:role/wci-lambda-invoke",
    "arn:aws:iam::160190466495:role/wci-lambda-invoke",
    "arn:aws:iam::819232936619:role/wci-lambda-invoke",
    "arn:aws:iam::829909441867:role/wci-lambda-invoke",
    "arn:aws:iam::354116250941:role/wci-lambda-invoke",
  ]
  source_files = sort(concat(
    tolist(fileset(local.repo_root, "cmd/**")),
    tolist(fileset(local.repo_root, "internal/**")),
    ["go.mod", "go.sum", "deploy/terraform/scripts/build.sh"],
  ))
  source_manifest = join("", [
    for file in local.source_files : "${file}:${filesha256("${local.repo_root}/${file}")}"
  ])
  source_fingerprint = sha256(local.source_manifest)
  source_hash        = base64sha256(local.source_manifest)
  worker_build_id    = "git-${substr(data.external.git.result.sha, 0, 12)}-${substr(local.source_fingerprint, 0, 12)}"
}

resource "random_string" "stack_suffix" {
  length  = 8
  lower   = true
  numeric = true
  special = false
  upper   = false
}

resource "time_static" "created" {}

resource "temporalcloud_namespace" "wordflow" {
  name           = local.stack_name
  regions        = [var.temporal_region]
  api_key_auth   = true
  retention_days = 1
  description    = "Ephemeral Wordflow end-to-end test stack"

  namespace_lifecycle = {
    enable_delete_protection = false
  }
}

resource "temporalcloud_service_account" "wordflow" {
  name        = local.stack_name
  description = "Runtime identity for the ephemeral Wordflow stack"

  namespace_scoped_access = {
    namespace_id = temporalcloud_namespace.wordflow.id
    permission   = "admin"
  }
}

resource "temporalcloud_apikey" "wordflow" {
  display_name = local.stack_name
  description  = "Runtime key for the ephemeral Wordflow stack"
  owner_type   = "service-account"
  owner_id     = temporalcloud_service_account.wordflow.id
  expiry_time  = timeadd(time_static.created.rfc3339, var.runtime_api_key_ttl)
  disabled     = false
}

resource "terraform_data" "build" {
  triggers_replace = [local.source_hash, var.lambda_architecture]

  provisioner "local-exec" {
    command = "${path.module}/scripts/build.sh"
    environment = {
      DIST_DIR  = local.dist_dir
      GOARCH    = var.lambda_architecture
      REPO_ROOT = local.repo_root
    }
  }
}

resource "random_uuid" "temporal_external_id" {}

resource "random_password" "session_jwt_secret" {
  length  = 64
  special = false
}

resource "aws_secretsmanager_secret" "temporal_api_key" {
  name                    = "${local.stack_name}/temporal-api-key"
  description             = "Temporal Cloud API key for ${temporalcloud_namespace.wordflow.id}"
  recovery_window_in_days = 0
}

resource "terraform_data" "temporal_api_key" {
  triggers_replace = [
    aws_secretsmanager_secret.temporal_api_key.id,
    temporalcloud_apikey.wordflow.id,
  ]

  provisioner "local-exec" {
    command = "${path.module}/scripts/put-secret.sh"
    environment = {
      AWS_REGION       = data.aws_region.current.region
      SECRET_ID        = aws_secretsmanager_secret.temporal_api_key.id
      TEMPORAL_API_KEY = temporalcloud_apikey.wordflow.token
    }
  }
}

data "aws_iam_policy_document" "lambda_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "lambda_execution" {
  name               = "${local.stack_name}-lambda-execution"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume_role.json
}

resource "aws_iam_role_policy_attachment" "lambda_logs" {
  role       = aws_iam_role.lambda_execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

data "aws_iam_policy_document" "lambda_secrets" {
  statement {
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [aws_secretsmanager_secret.temporal_api_key.arn]
  }
}

resource "aws_iam_role_policy" "lambda_secrets" {
  name   = "temporal-api-key"
  role   = aws_iam_role.lambda_execution.id
  policy = data.aws_iam_policy_document.lambda_secrets.json
}

resource "aws_cloudwatch_log_group" "worker" {
  name              = "/aws/lambda/${local.worker_function_name}"
  retention_in_days = var.log_retention_days
}

resource "aws_cloudwatch_log_group" "api" {
  name              = "/aws/lambda/${local.api_function_name}"
  retention_in_days = var.log_retention_days
}

resource "aws_lambda_function" "worker" {
  function_name = local.worker_function_name
  description   = "Temporal Wordflow Worker ${local.worker_build_id}"
  role          = aws_iam_role.lambda_execution.arn
  filename      = "${local.dist_dir}/worker.zip"
  handler       = "bootstrap"
  runtime       = "provided.al2023"
  architectures = local.lambda_architectures
  memory_size   = 512
  timeout       = 120
  publish       = true

  source_code_hash = local.source_hash

  environment {
    variables = {
      TEMPORAL_ADDRESS           = temporalcloud_namespace.wordflow.endpoints.grpc_address
      TEMPORAL_API_KEY_SECRET_ID = aws_secretsmanager_secret.temporal_api_key.id
      TEMPORAL_NAMESPACE         = temporalcloud_namespace.wordflow.id
      TEMPORAL_TASK_QUEUE        = var.task_queue
      TEMPORAL_WORKER_BUILD_ID   = local.worker_build_id
    }
  }

  depends_on = [
    aws_cloudwatch_log_group.worker,
    aws_iam_role_policy_attachment.lambda_logs,
    aws_iam_role_policy.lambda_secrets,
    terraform_data.build,
    terraform_data.temporal_api_key,
  ]
}

resource "aws_lambda_provisioned_concurrency_config" "worker" {
  count = var.worker_provisioned_concurrency > 0 ? 1 : 0

  function_name                     = aws_lambda_function.worker.function_name
  qualifier                         = aws_lambda_function.worker.version
  provisioned_concurrent_executions = var.worker_provisioned_concurrency
}

resource "aws_lambda_function" "api" {
  function_name = local.api_function_name
  description   = "Temporal Wordflow web app and API ${local.worker_build_id}"
  role          = aws_iam_role.lambda_execution.arn
  filename      = "${local.dist_dir}/api.zip"
  handler       = "bootstrap"
  runtime       = "provided.al2023"
  architectures = local.lambda_architectures
  memory_size   = var.api_memory_size
  timeout       = 30
  publish       = true

  source_code_hash = local.source_hash

  environment {
    variables = {
      SESSION_JWT_SECRET         = random_password.session_jwt_secret.result
      TEMPORAL_ADDRESS           = temporalcloud_namespace.wordflow.endpoints.grpc_address
      TEMPORAL_API_KEY_SECRET_ID = aws_secretsmanager_secret.temporal_api_key.id
      TEMPORAL_NAMESPACE         = temporalcloud_namespace.wordflow.id
      TEMPORAL_TASK_QUEUE        = var.task_queue
    }
  }

  depends_on = [
    aws_cloudwatch_log_group.api,
    aws_iam_role_policy_attachment.lambda_logs,
    aws_iam_role_policy.lambda_secrets,
    terraform_data.build,
    terraform_data.temporal_api_key,
  ]
}

resource "aws_lambda_alias" "api" {
  name             = "live"
  description      = "Current web and API release"
  function_name    = aws_lambda_function.api.function_name
  function_version = aws_lambda_function.api.version
}

resource "aws_lambda_provisioned_concurrency_config" "api" {
  count = var.api_provisioned_concurrency > 0 ? 1 : 0

  function_name                     = aws_lambda_function.api.function_name
  qualifier                         = aws_lambda_alias.api.name
  provisioned_concurrent_executions = var.api_provisioned_concurrency
}

data "aws_iam_policy_document" "temporal_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "AWS"
      identifiers = local.temporal_principal_arns
    }
    condition {
      test     = "StringEquals"
      variable = "sts:ExternalId"
      values   = [random_uuid.temporal_external_id.result]
    }
  }
}

resource "aws_iam_role" "temporal_invocation" {
  name               = "${local.stack_name}-temporal-invocation"
  description        = "Allows Temporal Cloud to invoke the Wordflow Worker Lambda"
  assume_role_policy = data.aws_iam_policy_document.temporal_assume_role.json
}

data "aws_iam_policy_document" "temporal_invocation" {
  statement {
    actions = [
      "lambda:GetFunction",
      "lambda:InvokeFunction",
    ]
    resources = [
      aws_lambda_function.worker.arn,
      "${aws_lambda_function.worker.arn}:*",
    ]
  }
}

resource "aws_iam_role_policy" "temporal_invocation" {
  name   = "invoke-${local.worker_function_name}"
  role   = aws_iam_role.temporal_invocation.id
  policy = data.aws_iam_policy_document.temporal_invocation.json
}

resource "terraform_data" "temporal_worker_version" {
  triggers_replace = [
    local.worker_build_id,
    filesha256("${path.module}/scripts/register-worker.sh"),
    aws_lambda_function.worker.qualified_arn,
    aws_iam_role.temporal_invocation.arn,
    random_uuid.temporal_external_id.result,
  ]

  provisioner "local-exec" {
    command = "${path.module}/scripts/register-worker.sh"
    environment = {
      DEPLOYMENT_NAME     = var.worker_deployment_name
      EXTERNAL_ID         = random_uuid.temporal_external_id.result
      INVOCATION_ROLE_ARN = aws_iam_role.temporal_invocation.arn
      LAMBDA_FUNCTION_ARN = aws_lambda_function.worker.qualified_arn
      TASK_QUEUE          = var.task_queue
      TEMPORAL_ADDRESS    = temporalcloud_namespace.wordflow.endpoints.grpc_address
      TEMPORAL_API_KEY    = temporalcloud_apikey.wordflow.token
      TEMPORAL_NAMESPACE  = temporalcloud_namespace.wordflow.id
      WORKER_BUILD_ID     = local.worker_build_id
    }
  }

  depends_on = [aws_iam_role_policy.temporal_invocation]
}

resource "terraform_data" "palm_springs_campaign" {
  triggers_replace = [
    filesha256("${path.module}/campaigns/palm-springs-offsite-2026.json"),
    filesha256("${path.module}/scripts/start-campaign.sh"),
  ]

  provisioner "local-exec" {
    command = "${path.module}/scripts/start-campaign.sh"
    environment = {
      CAMPAIGN_FILE      = "${path.module}/campaigns/palm-springs-offsite-2026.json"
      CAMPAIGN_ID        = "palm-springs-offsite-2026"
      TASK_QUEUE         = var.task_queue
      TEMPORAL_ADDRESS   = temporalcloud_namespace.wordflow.endpoints.grpc_address
      TEMPORAL_API_KEY   = temporalcloud_apikey.wordflow.token
      TEMPORAL_NAMESPACE = temporalcloud_namespace.wordflow.id
    }
  }

  depends_on = [terraform_data.temporal_worker_version]
}

resource "terraform_data" "temporal_foundations_campaign" {
  triggers_replace = [
    local.source_fingerprint,
    filesha256("${local.repo_root}/internal/bootstrap/temporal-foundations.json"),
    filesha256("${path.module}/scripts/start-default-campaign.sh"),
  ]

  provisioner "local-exec" {
    command = "${path.module}/scripts/start-default-campaign.sh"
    environment = {
      CAMPAIGN_FILE      = "${local.repo_root}/internal/bootstrap/temporal-foundations.json"
      CAMPAIGN_ID        = "temporal-foundations"
      REPO_ROOT          = local.repo_root
      TASK_QUEUE         = var.task_queue
      TEMPORAL_ADDRESS   = temporalcloud_namespace.wordflow.endpoints.grpc_address
      TEMPORAL_API_KEY   = temporalcloud_apikey.wordflow.token
      TEMPORAL_NAMESPACE = temporalcloud_namespace.wordflow.id
    }
  }

  depends_on = [terraform_data.temporal_worker_version]
}

resource "aws_apigatewayv2_api" "web" {
  name          = local.stack_name
  protocol_type = "HTTP"
}

resource "aws_apigatewayv2_integration" "web" {
  api_id                 = aws_apigatewayv2_api.web.id
  integration_type       = "AWS_PROXY"
  integration_method     = "POST"
  integration_uri        = aws_lambda_alias.api.invoke_arn
  payload_format_version = "2.0"
  timeout_milliseconds   = 30000
}

resource "aws_apigatewayv2_route" "web" {
  api_id    = aws_apigatewayv2_api.web.id
  route_key = "$default"
  target    = "integrations/${aws_apigatewayv2_integration.web.id}"
}

resource "aws_apigatewayv2_stage" "web" {
  api_id      = aws_apigatewayv2_api.web.id
  name        = "$default"
  auto_deploy = true
}

resource "aws_lambda_permission" "api_gateway" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.api.function_name
  qualifier     = aws_lambda_alias.api.name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.web.execution_arn}/*/*"
}
