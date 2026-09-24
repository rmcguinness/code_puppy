# Specification 013: Infrastructure as Code (Terraform) & Declarative Cloud Provisioning

## 1. Specification Metadata
- **Specification ID:** `SPEC-013`
- **Component:** Infrastructure as Code (IaC), Terraform Modules, Secret Provisioning & IAM
- **Target Platform:** Google Cloud Platform (GCS, Cloud SQL/AlloyDB, BigQuery, Vertex AI, Secret Manager, Cloud Run)
- **Status:** Approved for Implementation

---

## 2. Technology Architecture & Directory Layout

### 2.1 Tooling & Standards
- **IaC Engine:** Terraform >= 1.9.0
- **State Management:** Remote backend in Google Cloud Storage (`backend "gcs"`) with bucket versioning and uniform access.
- **Static Analysis & Linting:** `tflint` and `terraform fmt` integrated into pre-commit and CI/CD pipelines.
- **Environment Isolation:** Segmented environments (`dev`, `staging`, `prod`) using isolated state files and parameter sets.

### 2.2 Directory Structure (`deployments/terraform/`)
```
deployments/terraform/
├── environments/
│   ├── dev/
│   │   ├── main.tf             # Dev root module invoking shared modules
│   │   ├── variables.tf        # Dev-specific variable definitions
│   │   ├── terraform.tfvars    # Dev variable values (project ID, region)
│   │   └── backend.tf          # GCS backend configuration for dev state
│   └── prod/
│       ├── main.tf             # Prod root module
│       ├── variables.tf
│       ├── terraform.tfvars
│       └── backend.tf          # GCS backend configuration for prod state
└── modules/
    ├── storage/                # GCS buckets for shelf images and crops
    │   ├── main.tf
    │   ├── variables.tf
    │   └── outputs.tf
    ├── database/               # Cloud SQL / AlloyDB PostgreSQL instance
    │   ├── main.tf
    │   ├── variables.tf
    │   └── outputs.tf
    ├── bigquery/               # Dataset retail_cortex and partitioned audits table
    │   ├── main.tf
    │   ├── variables.tf
    │   └── outputs.tf
    ├── vertex_ai/              # Vertex AI Vector Search index & endpoint
    │   ├── main.tf
    │   ├── variables.tf
    │   └── outputs.tf
    ├── secret_manager/         # Secrets for modenv URI schemes (cloud://)
    │   ├── main.tf
    │   ├── variables.tf
    │   └── outputs.tf
    ├── cloud_run/              # Cloud Run microservice configuration
    │   ├── main.tf
    │   ├── variables.tf
    │   └── outputs.tf
    └── iam/                    # Service accounts, Workload Identity & roles
        ├── main.tf
        ├── variables.tf
        └── outputs.tf
```

---

## 3. Terraform Module Specifications

### 3.1 Remote State Configuration (`deployments/terraform/environments/dev/backend.tf`)
```hcl
terraform {
  required_version = ">= 1.9.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
  }

  backend "gcs" {
    bucket = "walmart-price-comp-tfstate-dev"
    prefix = "price-comp/dev"
  }
}
```

### 3.2 Google Cloud Storage Module (`modules/storage/main.tf`)
Provisions GCS buckets for raw camera uploads and isolated packaging crops with lifecycle rules:
```hcl
resource "google_storage_bucket" "shelf_images" {
  name                        = "${var.project_id}-price-comp-shelf-images-${var.environment}"
  location                    = var.region
  uniform_bucket_level_access = true
  versioning {
    enabled = true
  }

  cors {
    origin          = var.cors_allowed_origins
    method          = ["GET", "PUT", "POST", "HEAD", "OPTIONS"]
    response_header = ["*"]
    max_age_seconds = 3600
  }

  lifecycle_rule {
    condition {
      age = 90
    }
    action {
      type = "SetStorageClass"
      storage_class = "NEARLINE"
    }
  }
}
```

### 3.3 BigQuery Enterprise Data Lake Module (`modules/bigquery/main.tf`)
Provisions the `retail_cortex` dataset and audit table with strict fully-qualified naming compatibility:
```hcl
resource "google_bigquery_dataset" "retail_cortex" {
  dataset_id                  = "retail_cortex"
  friendly_name               = "Retail Cortex Data Lake"
  description                 = "Enterprise price comparison audits and multimodal embedding data lake"
  location                    = var.region
  default_table_expiration_ms = null

  labels = {
    env = var.environment
    app = "price-comp"
  }
}

resource "google_bigquery_table" "price_comparison_audits" {
  dataset_id = google_bigquery_dataset.retail_cortex.dataset_id
  table_id   = "price_comparison_audits"

  time_partitioning {
    type  = "DAY"
    field = "created_at"
  }

  clustering = ["store_id", "selected_walmart_item_id", "verification_action"]

  schema = file("${path.module}/schemas/price_comparison_audits.json")
}
```

### 3.4 Secret Manager & `modenv` Integration Module (`modules/secret_manager/main.tf`)
Pre-provisions Secret Manager secret containers to satisfy `modenv`'s `cloud://` scheme:
```hcl
resource "google_secret_manager_secret" "database_credentials" {
  secret_id = "price-comp-db-creds-${var.environment}"

  replication {
    auto {}
  }
}

resource "google_secret_manager_secret" "gemini_api_key" {
  secret_id = "price-comp-gemini-key-${var.environment}"

  replication {
    auto {}
  }
}

# Grant backend service account read access to secrets
resource "google_secret_manager_secret_iam_member" "backend_db_secret_access" {
  secret_id = google_secret_manager_secret.database_credentials.id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${var.backend_service_account_email}"
}
```

### 3.5 Cloud Run Service Module (`modules/cloud_run/main.tf`)
Configures serverless compute with scaling controls, health probes, and environment variables:
```hcl
resource "google_cloud_run_v2_service" "backend" {
  name     = "price-comp-backend-${var.environment}"
  location = var.region

  template {
    service_account = var.backend_service_account_email

    scaling {
      min_instance_count = var.environment == "prod" ? 2 : 0
      max_instance_count = 50
    }

    containers {
      image = var.container_image

      resources {
        limits = {
          cpu    = "2000m"
          memory = "2Gi"
        }
      }

      env {
        name  = "MODENV_RUNTIME"
        value = var.environment
      }

      startup_probe {
        http_get {
          path = "/healthz"
          port = 8000
        }
        initial_delay_seconds = 5
        period_seconds        = 10
        failure_threshold     = 3
      }

      liveness_probe {
        http_get {
          path = "/healthz"
          port = 8000
        }
        period_seconds    = 15
        failure_threshold = 3
      }
    }
  }

  traffic {
    type    = "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST"
    percent = 100
  }
}
```

---

## 4. Spec-Driven Implementation Tasks (Gemini 3.8 Flash Directives)

### Task 13.1: Author Terraform Modules
1. Scaffold `deployments/terraform/modules/` directory structure.
2. Author storage, bigquery, database, secret_manager, and cloud_run modules with parameterized inputs and validated outputs.

### Task 13.2: Configure Static Analysis & CI Pipeline
1. Create `.tflint.hcl` in `deployments/terraform/`.
2. Add Terraform check job to GitHub Actions workflow executing `terraform fmt -check` and `tflint --recursive`.

---

## 5. Verification & Acceptance Criteria

### Automated Verification
```bash
cd deployments/terraform/environments/dev
terraform init -backend=false
terraform validate
tflint --init && tflint
```
- Module validation succeeds with zero errors.
- All resource references adhere to least-privilege IAM and fully qualified BigQuery naming.
