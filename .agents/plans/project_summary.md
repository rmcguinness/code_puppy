# Project Summary: Walmart Price Comparison System

The Walmart Price Comparison application is a multi-tier retail intelligence tool designed to capture competitor shelf pricing and match observed products against Walmart's internal catalog. The architecture leverages multimodal generative AI ([Gemini 2.5 Flash Lite](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/backend/src/main/resources/application.properties#L6)) for shelf display parsing and dense vector embeddings ([Gemini Embedding 001](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/backend/src/main/resources/application.properties#L17)) combined with vector similarity search for product resolution.

---

## 1. Client Application (`client/`)

### Summary
The client is a single-page web application built with [React 19](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/package.json#L17) and [TypeScript 5.9](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/package.json#L31), bundled via [Vite 7](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/package.json#L33). It provides a mobile-first interface targeted at retail associates conducting in-store competitive intelligence. The client orchestrates a multi-step workflow: competitor store check-in via geolocation, shelf image capture, asynchronous extraction loading, candidate product selection/disambiguation, and neighbor matching review.

- **Source Root:** [client/src/](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/src/)
- **Core Dependencies:** `react`, `react-dom`, `react-router-dom` (v7), `@vis.gl/react-google-maps`, `@fortawesome/react-fontawesome`.
- **Styling:** Modular vanilla CSS accompanying individual component files.

### Build
- **Package Manager:** `npm`
- **Configuration Files:**
  - Build & Dev Server: [client/vite.config.ts](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/vite.config.ts)
  - TypeScript Configs: [client/tsconfig.json](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/tsconfig.json), [client/tsconfig.app.json](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/tsconfig.app.json), [client/tsconfig.node.json](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/tsconfig.node.json)
  - Linter: [client/eslint.config.js](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/eslint.config.js)
- **Environment Requirements:**
  - `VITE_GOOGLE_MAPS_API_KEY`: Required for Google Maps JavaScript API and Places API integration in `client/.env`.
- **Scripts:**
  ```bash
  cd client
  npm install        # Install project dependencies
  npm run dev        # Launch Vite development server with network host exposure
  npm run build      # Typecheck with tsc -b and create production bundle via vite build
  npm run lint       # Run ESLint across workspace
  npm run preview    # Locally serve production bundle
  ```
- **Reverse Proxy Routing:**
  [client/vite.config.ts](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/vite.config.ts#L8-L19) defines proxy rules forwarding:
  - `/extract-product-info` -> `http://localhost:8080` (Backend API)
  - `/api/gcs` -> `https://storage.googleapis.com` (Google Cloud Storage asset proxying)

### Features
- **Store Location & Onboarding:** ([AddStore.tsx](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/src/AddStore.tsx))
  - Uses `@vis.gl/react-google-maps` to render Google Maps centered on store coordinates.
  - Queries Google Places API (`/places:searchNearby`) using `fetch` with a 5000m radial filter to surface competitor locations with custom map markers and bottom sheet selection.
- **Image Submission & Progress Handling:** ([ScanCompetitorItem.tsx](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/src/ScanCompetitorItem.tsx))
  - Provides a viewfinder overlay and file input picker.
  - Shows an analysis modal during backend inference and routes dynamically based on extraction results (`/select-product` for multi-item detections, `/item-not-found` for single-item reviews).
- **Multi-Product Disambiguation:** ([ProductSelection.tsx](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/src/ProductSelection.tsx), [ProductSelectionCard.tsx](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/src/ProductSelectionCard.tsx))
  - Displays detected shelf items in a card list showing Brand, Description, Size, UOM, and Price when an image yields more than one item.
- **Match Review & Similarity Inspection:** ([ItemNotFound.tsx](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/src/ItemNotFound.tsx), [ItemFound.tsx](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/src/ItemFound.tsx), [NeighborItemCard.tsx](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/src/NeighborItemCard.tsx))
  - Displays uploaded shelf images alongside catalog nearest neighbors retrieved by vector search.
  - Calculates and displays match confidence percentages (`(distance * 100)% Match`).
  - Supports image popup zoom for visual inspection.
- **Item Attribute Correction Form:** ([Item.tsx](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/src/Item.tsx))
  - Pre-fills detected fields (Brand, Product Name, UPC, Unit, Weight, Competitor Price, Processing Status/Notes) for associate review and adjustment.
- **Authentication State Management:** ([AuthContext.tsx](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/src/contexts/AuthContext.tsx))
  - Inspects `?auth=` URL query parameters, persists tokens to `sessionStorage`, and supplies Bearer tokens to API calls via [productService.ts](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/client/src/services/productService.ts#L8-L10).

### Architectural Remediations & Target Design (SPEC-006, SPEC-008, SPEC-009)
The new project space systematically eliminates the prototype's client blindspots:
- **Comprehensive Automated Testing Harness ([SPEC-009](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_009.md)):** Configures **Vitest**, **React Testing Library**, and **Playwright MCP** browser automation tests in `client/package.json`, enforcing component coverage and CI quality gates.
- **Hardware-Integrated Camera Viewfinder ([SPEC-006](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_006.md)):** Replaces decorative markup with a production-grade WebRTC hook (`useCamera`) utilizing `navigator.mediaDevices.getUserMedia`, active torch/flashlight controls, HTML5 canvas frame capture, and graceful fallback to file upload.
- **Dynamic Schema Binding ([SPEC-008](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_008.md)):** Completely eliminates hardcoded fallback strings (`"1234567890"`, `"$2.00"`, `mms.com`). All match screens dynamically bind to strongly-typed TypeScript interfaces generated from backend Pydantic schemas.
- **Reactive Audit Persistence ([SPEC-007](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_007.md)):** Wires form edits and verification actions directly to backend `POST /api/v1/audits` with confirmation modals, associate action logging (`EXACT_MATCH`, `OVERRIDDEN_SKU`, `PRICE_CORRECTED`, `FLAGGED_NO_MATCH`), and optimistic cache updates.
- **Verified Static Asset Pipeline ([SPEC-008](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_008.md)):** Replaces broken `./broken.png` references with an in-tree SVG icon library (`lucide-react`) and packaged product image placeholders in `client/public/assets/`.
- **Resilient Global State & Deep-Link Hydration ([SPEC-008](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_008.md)):** Replaces fragile `location.state` with a centralized **Zustand 5** store with `persist` middleware, automatically hydrating state from `sessionStorage` and URL search parameters upon refresh or direct navigation.
- **Interactive Geolocation & Store Search ([SPEC-008](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_008.md)):** Connects the store search input to a debounced Google Places API query (`useDebounce`) with live radius filtering.

---

## 2. Backend Application (`backend/`)

### Summary
The backend is a [Spring Boot 3.5.7](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/backend/pom.xml#L8) application running on [Java 21](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/backend/pom.xml#L17). It provides REST endpoints for image analysis, leveraging [Spring AI 1.1.0-M3](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/backend/pom.xml#L18) with Google GenAI (`gemini-2.5-flash-lite`) and Vertex AI embedding models (`gemini-embedding-001`). The backend extracts structured product entities from shelf photography and executes catalog matching against vector search indexes or external UPC databases through a pluggable enrichment architecture.

- **Source Root:** [backend/src/main/java/com/example/geminimultimodal/](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/backend/src/main/java/com/example/geminimultimodal/)
- **Core Dependencies:** `spring-boot-starter-web`, `spring-boot-starter-webflux`, `spring-ai-starter-model-google-genai`, `spring-ai-starter-model-vertex-ai-embedding`, `spring-cloud-gcp-starter-storage`.

### Build
- **Build Tool:** Apache Maven (`pom.xml`)
- **Compilation Requirements:** Java 21 JDK
- **JVM Arguments:** [backend/pom.xml](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/backend/pom.xml#L126) configures `maven-surefire-plugin` with module reflection flags required by ByteBuddy and Spring test runners:
  `--add-opens java.base/java.lang=ALL-UNNAMED --add-opens java.base/java.util=ALL-UNNAMED -Dnet.bytebuddy.experimental=true`
- **Environment Variables Required:**
  ```bash
  export GOOGLE_CLOUD_PROJECT="<gcp-project-id>"
  export GOOGLE_CLOUD_LOCATION="us-central1"
  export GOOGLE_APPLICATION_CREDENTIALS="/path/to/service-account.json"
  ```
- **Commands:**
  ```bash
  cd backend
  mvn clean install       # Compile, run static tests, and package JAR
  mvn test                # Execute JUnit Jupiter test suites
  mvn spring-boot:run     # Launch Spring Boot embedded Tomcat on port 8080
  ```

### Features
- **Multimodal Extraction Pipeline:** ([ProductAnalysisController.java](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/backend/src/main/java/com/example/geminimultimodal/controller/ProductAnalysisController.java#L47-L72))
  - Exposes `POST /extract-product-info` accepting multipart image payload and optional Bearer auth header.
  - Injects image bytes into [ProductExtractionService.java](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/backend/src/main/java/com/example/geminimultimodal/service/ProductExtractionService.java#L44-L54) using Spring AI `UserMessage` and `Media` objects.
- **Strict Schema Enforcement:** ([PROMPT.md](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/backend/PROMPT.md))
  - Defines rigid JSON schema rules for UPC-A/UPC-E, Brand, Description, Float Price, Size, UnitOfMeasure, and mandatory `ProcessingStatus`.
- **Extraction Cleaning & Deduplication:** ([ProductExtractionService.java](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/backend/src/main/java/com/example/geminimultimodal/service/ProductExtractionService.java#L59-L88))
  - Cleans responses by isolating JSON array boundaries (`[` and `]`) and removing markdown code fences (` ```json `).
  - Deduplicates products using composite key: `[Brand, Description, Price, Size, UnitOfMeasure]`.
- **Vector Embedding Generation:** ([VectorEmbeddingClient.java](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/backend/src/main/java/com/example/geminimultimodal/client/VectorEmbeddingClient.java#L21-L36))
  - Generates 3072-dimensional text embeddings using Vertex AI `gemini-embedding-001`.
- **Pluggable Product Enrichment:** ([ProductEnricher.java](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/backend/src/main/java/com/example/geminimultimodal/service/enrichment/ProductEnricher.java), [AppConfiguration.java](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/backend/src/main/java/com/example/geminimultimodal/config/AppConfiguration.java#L31-L50))
  Configurable via `app.product-lookup.mode`:
  1. `wmt-search` ([SemanticSearchEnricher.java](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/backend/src/main/java/com/example/geminimultimodal/service/enrichment/SemanticSearchEnricher.java)): Generates vector embeddings for composite product descriptions and queries a local FAISS vector search microservice ([product-info/product_info_service.py](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/product-info/product_info_service.py)) on port 8000.
  2. `similar` ([VisualSimilarityEnricher.java](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/backend/src/main/java/com/example/geminimultimodal/service/enrichment/VisualSimilarityEnricher.java)): Queries Google Vertex AI Vector Search public domain index endpoints via REST `findNeighbors`.
  3. `item` ([UpcDatabaseEnricher.java](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/backend/src/main/java/com/example/geminimultimodal/service/enrichment/UpcDatabaseEnricher.java)): Queries the `api.upcitemdb.com` trial API using Spring WebFlux `WebClient`.
- **Request Context Propagation:** ([RequestContext.java](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/backend/src/main/java/com/example/geminimultimodal/util/RequestContext.java))
  - Maintains `ThreadLocal` storage of authorization tokens passed via HTTP headers for downstream enrichment calls.

### Architectural Remediations & Target Design (SPEC-001 through SPEC-005, SPEC-007, SPEC-010, SPEC-012, SPEC-014)
The new project space systematically eliminates the prototype's backend blindspots:
- **Asynchronous Cloud Storage & Signed URLs ([SPEC-003](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_003.md)):** Implements non-blocking `StorageService` using `google-cloud-storage` with Google Cloud V4 signed URLs (15-minute expiration) and a local filesystem emulator (`tests/fixtures/gcs_emulator/`) for zero-cost offline development.
- **Hermetic Configuration & Decoupled Prompt Templates ([SPEC-002](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_002.md), [SPEC-004](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_004.md)):** Replaces working-directory relative file reads with package-embedded system prompts (`importlib.resources`) and cascading TOML configuration managed by `modenv` (`.env.toml`, `.env.dev.toml`, `.env.local.toml`) with automated secret resolution (`cloud://`, `pks://`, `simple://`).
- **Declarative Environment Parameterization ([SPEC-002](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_002.md), [SPEC-013](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_013.md)):** Replaces hardcoded GCP project numbers, index IDs, and vertexai endpoints with strongly-typed `AppSettings` validated at startup. Environment values are provisioned cleanly by Terraform modules into GCP Secret Manager.
- **Structured Error Envelopes & Diagnostics ([SPEC-001](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_001.md), [SPEC-014](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_014.md)):** Replaces silent exception swallows with structured `ProcessingStatus` diagnostic reporting (`SUCCESS`, `PARTIAL_EXTRACTION`, `SERVICE_DEGRADED`, `EXTRACTION_FAILED`). Downstream timeouts trigger circuit breakers and provide actionable degraded responses rather than empty or unhandled 500 errors.
- **Enterprise Multimodal Vector Resolution ([SPEC-005](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_005.md), [SPEC-014](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_014.md)):** Eliminates rate-limited third-party trial APIs (`api.upcitemdb.com`). Catalog matching standardizes on Gemini Multimodal Embeddings 2 (`multimodalembedding@001`) with Vertex AI Vector Search and an in-memory/disk FAISS index fallback for high-availability local resilience.
- **Comprehensive Live Integration & ADK Golden Flywheel ([SPEC-010](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_010.md), [SPEC-014](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_014.md)):** Establishes a 100-fixture shelf photo golden dataset in `backend/tests/evals/test_adk_flywheel.py`, validating tokenization, IoU bounding boxes ($\ge 0.75$), and top-1 recall ($\ge 90\%$) through automated LLM-as-a-judge CI evaluation before deployment.

---

## 3. Infrastructure as Code & Declarative Provisioning (`deployments/terraform/`)

### Summary
The enterprise target replaces manual cloud configuration with modular Terraform 1.9+ scripts utilizing remote state storage in GCS (`walmart-price-comp-tfstate-*`) and enforced static analysis via `tflint` and `terraform fmt`.
- **Modules:**
  - `modules/storage`: Provisions GCS buckets for shelf uploads and crops with uniform bucket access and 90-day Nearline lifecycle policies.
  - `modules/bigquery`: Provisions dataset `retail_cortex` and partitioned/clustered `price_comparison_audits` table.
  - `modules/database`: Manages Cloud SQL / AlloyDB for PostgreSQL instances and connection pools.
  - `modules/secret_manager`: Provisions secret containers integrated with `modenv` (`cloud://` URIs).
  - `modules/cloud_run`: Configures serverless container runtime with autoscaling (0-50 instances), VPC connectors, and health probes.
  - `modules/iam`: Manages least-privilege service accounts and Workload Identity federation.

---

## 4. Resilience Engineering & Failure Protection (`core/resilience.py`)

### Summary
To prevent cascading server starvation during bursty store audits and intermittent cellular connectivity:
- **Bulkhead Isolation:** Bounded `asyncio.Semaphore` pools segregate execution resources (Vision: 15, Vector Search: 30, Storage: 25, DB Pool: 20). Excess requests fail fast with `HTTP 503` rather than queuing indefinitely.
- **Circuit Breakers:** Monitors consecutive errors against Vertex AI and Gemini APIs. Trips to `OPEN` after 5 failures in 30 seconds, routing traffic to in-memory/disk FAISS index or prompting associate barcode scan mode.
- **Distributed Deadlines & Load Shedding:** Enforces a strict 5.0s end-to-end timeout budget across distributed spans and limits HTTP request payloads to $\le 15\text{ MB}$ (`HTTP 413`).

---

## 5. Enterprise Data Lake & Antigravity Agentic Maintenance

### Summary
- **BigQuery Data Lake:** Streams verified price audits, associate actions (`EXACT_MATCH`, `OVERRIDDEN_SKU`, `PRICE_CORRECTED`, `FLAGGED_NO_MATCH`), and 1408-dimensional dense embeddings to `<gcp-project>.retail_cortex.price_comparison_audits`.
- **Human-in-the-Loop Continuous Retraining:** Uses associate overrides to upsert multi-vector exemplars into Vertex AI Vector Search and generate $(a, p, n)$ contrastive triplets for metric learning fine-tuning.
- **Antigravity Tooling & ADK Quality Flywheel:**
  - Standardizes autonomous agent workflows via `.gemini/GEMINI.md` and custom domain skills.
  - Enforces automated regression testing against a 100-fixture Golden Dataset in `backend/tests/evals/test_adk_flywheel.py` with LLM-as-a-judge scoring before merging code changes.
  - Provides offline local sidecars (mock GCS, in-memory FAISS, mock `modenv` secrets) enabling zero-cost local agent development.

---

## 6. Target Greenfield Scaffolding Blueprint

The specifications (`SPEC-001` through `SPEC-014`) serve as the direct build contract for creating the clean, enterprise-grade codebase without carrying over any legacy prototype debt.

### Greenfield Architecture Layout
```
├── .gemini/                       # Antigravity agentic workspace rules & custom skills (SPEC-014)
├── .env.toml                      # modenv base config with cloud:// secret resolution (SPEC-002)
├── .env.dev.toml                  # Development environment configuration
├── .env.local.toml                # Local developer overrides and sidecar switches
├── backend/                       # Python 3.13 / FastAPI service (SPEC-001)
│   ├── pyproject.toml             # uv package manager definition
│   ├── src/price_comp_backend/
│   │   ├── config.py              # Strongly-typed modenv AppSettings (SPEC-002)
│   │   ├── main.py                # FastAPI lifecycle, OTEL tracing, /healthz, /readyz (SPEC-001, 014)
│   │   ├── core/
│   │   │   ├── agent.py           # Google ADK agent and reasoning orchestration (SPEC-011)
│   │   │   ├── resilience.py      # Bulkhead semaphores, circuit breakers, deadline propagation (SPEC-014)
│   │   │   └── telemetry.py       # Cloud Trace exporter & JSON structured logger (SPEC-012)
│   │   ├── models/schemas.py      # Unified Pydantic models (SPEC-001)
│   │   └── services/
│   │       ├── storage.py         # Async GCS with V4 signed URLs & local mock sidecar (SPEC-003)
│   │       ├── vision.py          # Gemini 2.5 Flash Lite shelf parsing (SPEC-004)
│   │       ├── embedding.py       # Gemini Multimodal Embeddings 2 (1408-dim) (SPEC-005)
│   │       ├── matcher.py         # Vector similarity search with local FAISS fallback (SPEC-005, 014)
│   │       └── lakehouse.py       # BigQuery CDC streaming & continuous learning (SPEC-007)
│   └── tests/
│       ├── test_*.py              # Comprehensive unit & resilience test suite (SPEC-010, 014)
│       └── evals/                 # ADK Quality Flywheel golden dataset evaluations (SPEC-014)
├── client/                        # Modern React 19 + TypeScript 5.9 + Vite 7 SPA (SPEC-006, 008)
│   ├── src/
│   │   ├── components/camera/     # Real WebRTC camera viewfinder with torch controls (SPEC-006)
│   │   ├── stores/                # Zustand persistent store with deep-link hydration (SPEC-008)
│   │   ├── telemetry.ts           # OpenTelemetry web tracer & W3C traceparent injector (SPEC-012)
│   │   └── views/                 # Disambiguation, review, store locator, dynamic forms (SPEC-008)
│   └── tests/                     # Vitest component tests & Playwright browser verification (SPEC-009)
└── deployments/terraform/         # Modular IaC with remote GCS state (SPEC-013)
    ├── modules/{storage,bigquery,database,secret_manager,cloud_run,iam}
    └── environments/{dev,prod}
```

### Remediation Verification Checklist
| Former Blindspot | Remediating Specification | Target Greenfield Solution | Verification Method |
| :--- | :--- | :--- | :--- |
| **No automated client tests** | [SPEC-009](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_009.md) | Vitest, React Testing Library, Playwright MCP | `npm run test` & Playwright CI suites |
| **Mock camera viewfinder** | [SPEC-006](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_006.md) | WebRTC `useCamera` hook with HTML5 canvas snapshot | Real stream capture & canvas unit tests |
| **Hardcoded mock data** | [SPEC-008](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_008.md) | TypeScript interfaces strictly generated from Pydantic | Compiler type-check `tsc -b` |
| **Dead form & no persistence** | [SPEC-007](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_007.md) | REST `POST /api/v1/audits` with store & associate actions | Integration test validating DB/BQ commit |
| **Fragile router state** | [SPEC-008](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_008.md) | Zustand 5 with `persist` middleware & query param sync | Page refresh & direct URL navigation tests |
| **Stubbed Cloud Storage** | [SPEC-003](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_003.md) | Async `google-cloud-storage` with V4 signed URLs | GCS upload & signed URL verification test |
| **Relative prompt path** | [SPEC-002](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_002.md), [SPEC-004](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_004.md) | Embedded prompt templates in Python package | Standalone execution from arbitrary CWD |
| **Hardcoded cloud endpoints** | [SPEC-002](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_002.md), [SPEC-013](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_013.md) | Cascading `modenv` configuration & Terraform modules | Multi-environment CI validation (`dev`/`prod`) |
| **Silent enricher failures** | [SPEC-001](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_001.md), [SPEC-014](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_014.md) | Structured `ProcessingStatus` & Circuit Breaker | Breaker trip unit test verifying fallback |
| **Fragile external UPC API** | [SPEC-005](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_005.md), [SPEC-014](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_014.md) | Vertex AI Multimodal Embeddings + FAISS fallback | Offline similarity matching test |
| **No live model eval** | [SPEC-010](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_010.md), [SPEC-014](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_014.md) | 100-fixture Golden Dataset ADK Quality Flywheel | Automated CI gate (IoU $\ge 0.75$, Recall $\ge 90\%$) |
| **No cloud provisioning** | [SPEC-013](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_013.md) | Declarative Terraform modules with GCS remote state | `terraform fmt -check` & `tflint` in CI |
| **Cascading overload** | [SPEC-014](file:///Users/rmcguinness/Projects/customers/walmart/price-comp/project_spec_014.md) | Bulkhead semaphores (Vision: 15, Vector: 30) & 5.0s budget | Concurrency saturation test with HTTP 503 fast-fail |
