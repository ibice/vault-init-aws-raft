provider "aws" {
  region = "us-east-1"
}

provider "helm" {
  kubernetes {
    host                   = data.aws_eks_cluster.example.endpoint
    token                  = data.aws_eks_cluster_auth.example.token
    cluster_ca_certificate = base64decode(data.aws_eks_cluster.example.certificate_authority[0].data)
  }
}

data "aws_eks_cluster" "example" {
  name = var.cluster
}

data "aws_eks_cluster_auth" "example" {
  name = var.cluster
}

resource "helm_release" "vault" {
  name = "vault"

  repository = "https://helm.releases.hashicorp.com"
  chart      = "vault"
  version    = "0.27.0"

  namespace        = "vault"
  create_namespace = true

  values = [templatefile("${path.module}/values.yaml", {
    image                    = var.image
    secretsmanager_secret_id = aws_secretsmanager_secret.example.id
    role_arn                 = module.irsa.iam_role_arn
  })]
}

##############################################################################
# Backing AWS resources                                                      #
##############################################################################

module "irsa" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-role-for-service-accounts-eks"
  version = "~> 5.34"

  role_name = "vault-init-example"

  role_policy_arns = {
    policy = aws_iam_policy.example.arn
  }

  oidc_providers = {
    main = {
      provider_arn               = data.aws_iam_openid_connect_provider.example.arn
      namespace_service_accounts = ["vault:vault"]
    }
  }
}

data "aws_iam_openid_connect_provider" "example" {
  url = data.aws_eks_cluster.example.identity[0].oidc[0].issuer
}

resource "aws_iam_policy" "example" {
  policy = jsonencode(
    {
      Version = "2012-10-17"
      Statement = [
        {
          Action = [
            "secretsmanager:DescribeSecret",
            "secretsmanager:GetSecretValue",
            "secretsmanager:UpdateSecret",
          ],
          Effect   = "Allow"
          Resource = aws_secretsmanager_secret.example.arn
        }
      ]
    }
  )
}

resource "aws_secretsmanager_secret" "example" {
  name_prefix = "vault-init-example"
}
