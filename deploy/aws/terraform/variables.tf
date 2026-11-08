variable "region" {
  type    = string
  default = "us-west-2"
}

variable "cluster_name" {
  type    = string
  default = "atlas"
}

variable "kubernetes_version" {
  type    = string
  default = "1.31"
}

variable "vpc_cidr" {
  type    = string
  default = "10.40.0.0/16"
}

variable "node_instance_types" {
  type    = list(string)
  default = ["t3.medium"]
}

variable "db_instance_class" {
  type    = string
  default = "db.t3.micro"
}

variable "db_password" {
  type      = string
  sensitive = true
}
