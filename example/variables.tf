variable "cluster" {
  description = "Name of the EKS cluster"
}

variable "image" {
  description = "Token to access the Kubernetes cluster"
  default     = "jorgecarpio/vault-init:1.0.0"
}
