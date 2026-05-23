data "aws_caller_identity" "current" {}

locals {
  github_sub_main    = "repo:${var.github_owner}/${var.github_repo}:ref:refs/heads/main"
  github_sub_pr      = "repo:${var.github_owner}/${var.github_repo}:pull_request"
  github_sub_env_apl = "repo:${var.github_owner}/${var.github_repo}:environment:production-apply"
}

resource "aws_iam_openid_connect_provider" "github" {
  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
  # GitHub の thumbprint。AWS 側で検証されるため固定値で OK（公式ドキュメント記載）
  thumbprint_list = ["6938fd4d98bab03faadb97b34396831e3780aea1"]
}

# ---- Role: deploy (main push 時のみ、ECR push + ECS update) ----

data "aws_iam_policy_document" "deploy_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = [local.github_sub_main]
    }
  }
}

resource "aws_iam_role" "deploy" {
  name               = "${var.name_prefix}-role-deploy"
  assume_role_policy = data.aws_iam_policy_document.deploy_assume.json
}

data "aws_iam_policy_document" "deploy" {
  statement {
    sid    = "ECRAuth"
    effect = "Allow"
    actions = [
      "ecr:GetAuthorizationToken",
    ]
    resources = ["*"]
  }

  statement {
    sid    = "ECRPush"
    effect = "Allow"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:CompleteLayerUpload",
      "ecr:InitiateLayerUpload",
      "ecr:PutImage",
      "ecr:UploadLayerPart",
      "ecr:BatchGetImage",
      "ecr:DescribeImages",
      "ecr:DescribeRepositories",
    ]
    resources = var.ecr_repository_arns
  }

  statement {
    sid    = "ECSUpdate"
    effect = "Allow"
    actions = [
      "ecs:DescribeServices",
      "ecs:UpdateService",
      "ecs:DescribeTasks",
      "ecs:RunTask",
      "ecs:DescribeTaskDefinition",
      "ecs:RegisterTaskDefinition",
      "ecs:ListTasks",
    ]
    resources = ["*"]
    condition {
      test     = "ArnEquals"
      variable = "ecs:cluster"
      values   = [var.ecs_cluster_arn]
    }
  }

  statement {
    sid       = "PassRoleForTaskDef"
    effect    = "Allow"
    actions   = ["iam:PassRole"]
    resources = ["arn:aws:iam::${data.aws_caller_identity.current.account_id}:role/${var.name_prefix}-*"]
  }
}

resource "aws_iam_role_policy" "deploy" {
  name   = "deploy"
  role   = aws_iam_role.deploy.id
  policy = data.aws_iam_policy_document.deploy.json
}

# ---- Role: terraform-plan (PR 時、read-only に近い) ----

data "aws_iam_policy_document" "tf_plan_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values = [
        local.github_sub_pr,   # repo:owner/repo:pull_request  (terraform.yml plan)
        local.github_sub_main, # repo:owner/repo:ref:refs/heads/main (deploy.yml precheck / main dispatch)
      ]
    }
  }
}

resource "aws_iam_role" "tf_plan" {
  name               = "${var.name_prefix}-role-tf-plan"
  assume_role_policy = data.aws_iam_policy_document.tf_plan_assume.json
}

# plan には ReadOnlyAccess + state バケットの read/write を付与
resource "aws_iam_role_policy_attachment" "tf_plan_readonly" {
  role       = aws_iam_role.tf_plan.name
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}

data "aws_iam_policy_document" "tf_state_access" {
  statement {
    effect    = "Allow"
    actions   = ["s3:ListBucket", "s3:GetBucketVersioning"]
    resources = [var.tfstate_bucket_arn]
  }
  # tfstate 本体と state ロックファイル（use_lockfile が同バケットに *.tflock を置く）の双方をカバーする。
  statement {
    effect    = "Allow"
    actions   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"]
    resources = ["${var.tfstate_bucket_arn}/*"]
  }
}

resource "aws_iam_role_policy" "tf_plan_state" {
  name   = "tfstate-access"
  role   = aws_iam_role.tf_plan.id
  policy = data.aws_iam_policy_document.tf_state_access.json
}

# ReadOnlyAccess は機密保護のため GetSecretValue / kms:Decrypt を含まない。
# plan の refresh で aws_secretsmanager_secret_version を読むため、対象の Aurora
# Secret と暗号化 CMK に限定して両アクションを補う。
data "aws_iam_policy_document" "tf_plan_secret_read" {
  statement {
    effect    = "Allow"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [var.aurora_master_secret_arn]
  }
  statement {
    effect    = "Allow"
    actions   = ["kms:Decrypt"]
    resources = [var.db_kms_key_arn]
  }
}

resource "aws_iam_role_policy" "tf_plan_secret_read" {
  name   = "aurora-secret-read"
  role   = aws_iam_role.tf_plan.id
  policy = data.aws_iam_policy_document.tf_plan_secret_read.json
}

# ---- Role: terraform-apply (main + production-apply environment 限定) ----

data "aws_iam_policy_document" "tf_apply_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = [local.github_sub_env_apl]
    }
  }
}

resource "aws_iam_role" "tf_apply" {
  name               = "${var.name_prefix}-role-tf-apply"
  assume_role_policy = data.aws_iam_policy_document.tf_apply_assume.json
}

# apply は強力。PowerUserAccess を付与し、必要に応じて条件で絞る
resource "aws_iam_role_policy_attachment" "tf_apply_power" {
  role       = aws_iam_role.tf_apply.name
  policy_arn = "arn:aws:iam::aws:policy/PowerUserAccess"
}

resource "aws_iam_role_policy_attachment" "tf_apply_iam" {
  role       = aws_iam_role.tf_apply.name
  policy_arn = "arn:aws:iam::aws:policy/IAMFullAccess"
}

resource "aws_iam_role_policy" "tf_apply_state" {
  name   = "tfstate-access"
  role   = aws_iam_role.tf_apply.id
  policy = data.aws_iam_policy_document.tf_state_access.json
}
