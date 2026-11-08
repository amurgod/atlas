terraform {
  required_version = ">= 1.6.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.60"
    }
  }
}

provider "aws" {
  region = var.region
}

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  azs      = slice(data.aws_availability_zones.available.names, 0, 2)
  services = ["user", "knowledge", "planner", "notification", "llm-gateway", "ui"]
}

module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "~> 5.8"

  name = "${var.cluster_name}-vpc"
  cidr = var.vpc_cidr

  azs             = local.azs
  private_subnets = [for i, az in local.azs : cidrsubnet(var.vpc_cidr, 4, i)]
  public_subnets  = [for i, az in local.azs : cidrsubnet(var.vpc_cidr, 4, i + 8)]

  enable_nat_gateway   = true
  single_nat_gateway   = true
  enable_dns_hostnames = true
  enable_dns_support   = true

  public_subnet_tags = {
    "kubernetes.io/role/elb" = "1"
  }
  private_subnet_tags = {
    "kubernetes.io/role/internal-elb" = "1"
  }
}

module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 20.24"

  cluster_name    = var.cluster_name
  cluster_version = var.kubernetes_version

  vpc_id     = module.vpc.vpc_id
  subnet_ids = module.vpc.private_subnets

  enable_cluster_creator_admin_permissions = true
  cluster_endpoint_public_access           = true

  eks_managed_node_groups = {
    default = {
      instance_types = var.node_instance_types
      min_size       = 2
      max_size       = 4
      desired_size   = 2
    }
  }
}

resource "aws_security_group" "rds" {
  name   = "${var.cluster_name}-rds"
  vpc_id = module.vpc.vpc_id

  ingress {
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = [module.eks.node_security_group_id]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_db_subnet_group" "atlas" {
  name       = "${var.cluster_name}-db"
  subnet_ids = module.vpc.private_subnets
}

resource "aws_db_instance" "atlas" {
  identifier                   = var.cluster_name
  engine                       = "postgres"
  engine_version               = "16"
  instance_class               = var.db_instance_class
  allocated_storage            = 20
  db_name                      = "atlas"
  username                     = "atlas"
  password                     = var.db_password
  db_subnet_group_name         = aws_db_subnet_group.atlas.name
  vpc_security_group_ids       = [aws_security_group.rds.id]
  skip_final_snapshot          = true
  deletion_protection          = false
  publicly_accessible          = false
  backup_retention_period      = 7
  performance_insights_enabled = false
}

resource "aws_ecr_repository" "svc" {
  for_each             = toset(local.services)
  name                 = "${var.cluster_name}-${each.key}"
  image_tag_mutability = "MUTABLE"
  force_delete         = true

  image_scanning_configuration {
    scan_on_push = true
  }
}

data "aws_iam_policy_document" "ses" {
  statement {
    actions   = ["ses:SendEmail", "ses:SendRawEmail"]
    resources = ["*"]
  }
}

resource "aws_iam_policy" "ses" {
  name   = "${var.cluster_name}-notification-ses"
  policy = data.aws_iam_policy_document.ses.json
}

data "aws_iam_policy_document" "notify_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [module.eks.oidc_provider_arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${replace(module.eks.cluster_oidc_issuer_url, "https://", "")}:sub"
      values   = ["system:serviceaccount:atlas:notification"]
    }
    condition {
      test     = "StringEquals"
      variable = "${replace(module.eks.cluster_oidc_issuer_url, "https://", "")}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "notification" {
  name               = "${var.cluster_name}-notification"
  assume_role_policy = data.aws_iam_policy_document.notify_assume.json
}

resource "aws_iam_role_policy_attachment" "notification_ses" {
  role       = aws_iam_role.notification.name
  policy_arn = aws_iam_policy.ses.arn
}
