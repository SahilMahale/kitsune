terraform {
  required_providers {
    helm = {
      source = "hashicorp/helm"
      version = "~> 2.13"
    }
    kubernetes = {
      source = "hashicorp/kubernetes"
      version = "~> 2.30"
    }
  }
}

provider "kubernetes" {
  config_path = "~/.kube/config"
  config_context = "minikube"
}

provider "helm" {
  kubernetes = {
  config_path = "~/.kube/config"
  config_context = "minikube"
  }
}
