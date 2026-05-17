# Terraform 系（backend のブートストラップ / init / 削除）

.PHONY: tf/bootstrap tf/init tf/bootstrap/destroy

## Terraform backend 用の S3 バケットを作成（初回のみ / 冪等）。state ロックは S3 use_lockfile で行う
tf/bootstrap:
	@set -eu; \
	ACCOUNT_ID=$$(aws sts get-caller-identity --query Account --output text); \
	BUCKET=$(TFSTATE_PREFIX)-$$ACCOUNT_ID; \
	echo "Region : $(AWS_REGION)"; \
	echo "Bucket : $$BUCKET"; \
	if aws s3api head-bucket --bucket $$BUCKET >/dev/null 2>&1; then \
		echo "[skip] S3 バケットは既に存在します"; \
	else \
		aws s3api create-bucket --bucket $$BUCKET --region $(AWS_REGION) \
			--create-bucket-configuration LocationConstraint=$(AWS_REGION) >/dev/null; \
		aws s3api put-bucket-versioning --bucket $$BUCKET \
			--versioning-configuration Status=Enabled; \
		aws s3api put-bucket-encryption --bucket $$BUCKET \
			--server-side-encryption-configuration \
			'{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}'; \
		aws s3api put-public-access-block --bucket $$BUCKET \
			--public-access-block-configuration \
			BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true; \
		echo "[ok] S3 バケットを作成しました"; \
	fi; \
	echo "完了。続けて make tf/init を実行してください"

## terraform init（backend を S3 に接続。tf/bootstrap 実行後に1回。state ロックは S3 use_lockfile）
tf/init:
	@set -eu; \
	ACCOUNT_ID=$$(aws sts get-caller-identity --query Account --output text); \
	cd terraform/environments/dev && terraform init -reconfigure \
		-backend-config="bucket=$(TFSTATE_PREFIX)-$$ACCOUNT_ID" \
		-backend-config="key=dev/terraform.tfstate" \
		-backend-config="region=$(AWS_REGION)" \
		-backend-config="use_lockfile=true" \
		-backend-config="encrypt=true"

## Terraform backend の S3 バケットを削除（tfstate が失われる。全インフラ destroy 後のみ実行）
tf/bootstrap/destroy:
	@set -eu; \
	ACCOUNT_ID=$$(aws sts get-caller-identity --query Account --output text); \
	BUCKET=$(TFSTATE_PREFIX)-$$ACCOUNT_ID; \
	echo "削除対象: S3=$$BUCKET"; \
	echo "警告: tfstate が完全に失われます。terraform 管理リソースを全て destroy 済みであることを確認してください"; \
	printf "削除する場合は 'yes' を入力: "; read ans; \
	[ "$$ans" = "yes" ] || { echo "中止しました"; exit 1; }; \
	if aws s3api head-bucket --bucket $$BUCKET >/dev/null 2>&1; then \
		while :; do \
			VERSIONS=$$(aws s3api list-object-versions --bucket $$BUCKET --max-keys 500 \
				--query '[Versions[].{Key:Key,VersionId:VersionId}, DeleteMarkers[].{Key:Key,VersionId:VersionId}][]' \
				--output json); \
			case "$$VERSIONS" in ""|"[]"|"null") break;; esac; \
			aws s3api delete-objects --bucket $$BUCKET \
				--delete "{\"Objects\": $$VERSIONS}" >/dev/null; \
		done; \
		aws s3api delete-bucket --bucket $$BUCKET --region $(AWS_REGION); \
		echo "[ok] S3 バケットを削除しました"; \
	else \
		echo "[skip] S3 バケットは存在しません"; \
	fi
