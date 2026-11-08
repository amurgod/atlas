output "cluster_name" {
  value = module.eks.cluster_name
}

output "cluster_endpoint" {
  value = module.eks.cluster_endpoint
}

output "ecr_urls" {
  value = { for k, r in aws_ecr_repository.svc : k => r.repository_url }
}

output "rds_endpoint" {
  value = aws_db_instance.atlas.address
}

output "database_url" {
  sensitive = true
  value     = "postgres://atlas:${var.db_password}@${aws_db_instance.atlas.address}:5432/atlas?sslmode=require"
}

output "notification_role_arn" {
  value = aws_iam_role.notification.arn
}

output "configure_kubectl" {
  value = "aws eks update-kubeconfig --region ${var.region} --name ${module.eks.cluster_name}"
}
