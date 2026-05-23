data "aws_caller_identity" "current" {}

locals {
  github_sub_main    = "repo:${var.github_owner}/${var.github_repo}:ref:refs/heads/main"
  github_sub_pr      = "repo:${var.github_owner}/${var.github_repo}:pull_request"
  github_sub_env_apl = "repo:${var.github_owner}/${var.github_repo}:environment:production-apply"

  # boundary ポリシーは自身の document 内で ARN を参照する（循環参照になる）ため、
  # aws_iam_policy.tf_apply_boundary.arn ではなく account_id から手動で組み立てる。
  tf_apply_boundary_name = "${var.name_prefix}-tf-apply-boundary"
  tf_apply_boundary_arn  = "arn:aws:iam::${data.aws_caller_identity.current.account_id}:policy/${local.tf_apply_boundary_name}"
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
  # 権限昇格を防ぐ上限（permissions boundary）。PowerUser+IAMFull のままでも
  # boundary の Deny を超える実効権限は付与されない。
  # boundary ポリシー作成 → role 付与の順序を保証するため、手組み ARN(local)
  # ではなくリソースの arn を参照する（local はポリシー document 内の自己参照専用）。
  permissions_boundary = aws_iam_policy.tf_apply_boundary.arn
}

# tf-apply の permissions boundary（実効権限の上限）。
# PowerUserAccess + IAMFullAccess は実質 admin 相当で、IAMFull により
# 「admin 権限を持つ別 role を作って assume する」等の昇格が可能になる。
# この boundary を role に付けることで、Terraform 運用に必要な広い権限は
# 残しつつ、昇格経路（boundary なしの IAM エンティティ作成・boundary の
# 付替/剥奪・boundary ポリシー自身の改変）のみを GitHub 側設定に依存せず封じる。
data "aws_iam_policy_document" "tf_apply_boundary" {
  # ベースライン: Terraform が多様なリソースを扱うため広範に許可する。
  # 実効権限は「role のポリシー ∩ この boundary」なので、ここで許可しても
  # 下の Deny に該当する操作は実行できない。
  statement {
    sid       = "AllowAll"
    effect    = "Allow"
    actions   = ["*"]
    resources = ["*"]
  }

  # 昇格防止1: この boundary を付けない（または別 boundary の）role/user の作成を禁止。
  # tf-apply が作る IAM エンティティは必ず同じ上限を継承する。
  statement {
    sid    = "DenyCreateWithoutBoundary"
    effect = "Deny"
    actions = [
      "iam:CreateRole",
      "iam:CreateUser",
    ]
    resources = ["*"]
    condition {
      test     = "StringNotEquals"
      variable = "iam:PermissionsBoundary"
      values   = [local.tf_apply_boundary_arn]
    }
  }

  # 昇格防止2: boundary の剥奪、および別 boundary への付替を禁止。
  statement {
    sid    = "DenyBoundaryDeletion"
    effect = "Deny"
    actions = [
      "iam:DeleteRolePermissionsBoundary",
      "iam:DeleteUserPermissionsBoundary",
    ]
    resources = ["*"]
  }
  statement {
    sid    = "DenyBoundaryAlteration"
    effect = "Deny"
    actions = [
      "iam:PutRolePermissionsBoundary",
      "iam:PutUserPermissionsBoundary",
    ]
    resources = ["*"]
    condition {
      test     = "StringNotEquals"
      variable = "iam:PermissionsBoundary"
      values   = [local.tf_apply_boundary_arn]
    }
  }

  # 昇格防止3: boundary ポリシー自身の改変・削除を禁止（上限の骨抜き防止）。
  statement {
    sid    = "ProtectBoundaryPolicy"
    effect = "Deny"
    actions = [
      "iam:CreatePolicyVersion",
      "iam:DeletePolicyVersion",
      "iam:SetDefaultPolicyVersion",
      "iam:DeletePolicy",
    ]
    resources = [local.tf_apply_boundary_arn]
  }
}

resource "aws_iam_policy" "tf_apply_boundary" {
  name   = local.tf_apply_boundary_name
  policy = data.aws_iam_policy_document.tf_apply_boundary.json
}

# apply は強力。PowerUserAccess を付与し、上限は permissions boundary で絞る
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
