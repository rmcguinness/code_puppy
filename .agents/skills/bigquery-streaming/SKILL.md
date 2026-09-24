---
name: bigquery-streaming
description: Validate fully qualified BigQuery table schemas, execute streaming CDC insertion tests, and extract contrastive triplets for metric learning models.
---

# `bigquery-streaming` Skill

## Overview
This skill guides autonomous agents and data engineers in interacting with Google BigQuery as the enterprise analytical lakehouse for the Retail Cortex Price Comparison platform. It enforces:
1. Strict adherence to fully qualified table naming: `<gcp-project>.<dataset>.<table-name>`.
2. Valid schema insertion of 1408-dimensional dense vector embeddings generated from shelf crops.
3. Asynchronous CDC streaming ingestion via the BigQuery client.
4. Contrastive triplet SQL mining for continuous human-in-the-loop (HITL) metric learning.

---

## BigQuery SQL Quality Gates

### Mandatory Table Naming Rule
Every SQL query targeting BigQuery MUST format table names using full project and dataset qualification. Relative or unqualified table references (e.g., `FROM price_comparison_audits`) fail static analysis:

```sql
-- VALID
SELECT audit_id, associate_action, vector_embedding 
FROM `<gcp-project>.retail_cortex.price_comparison_audits`
WHERE created_at >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 7 DAY);

-- FORBIDDEN (FAILS CI STATIC ANALYSIS)
SELECT audit_id FROM price_comparison_audits;
```

---

## Schema & Vector Payload Verification

The target table schema is defined in `SPEC-012`:
- **Table:** `<gcp-project>.retail_cortex.price_comparison_audits`
- **Partitioning:** `TIMESTAMP_TRUNC(created_at, DAY)`
- **Clustering:** `store_id, competitor_name, associate_action`

### Schema Validation Helper
```python
def validate_audit_row_schema(row: dict) -> None:
    required_fields = [
        "audit_id", "trace_id", "created_at", "store_id", "associate_id",
        "competitor_name", "detected_upc", "detected_price",
        "associate_action", "vector_embedding", "confidence_score"
    ]
    for field in required_fields:
        if field not in row or row[field] is None:
            raise ValueError(f"Missing mandatory BigQuery field: {field}")
            
    # Vector validation: Exactly 1408 float32 dimensions
    vec = row["vector_embedding"]
    if not isinstance(vec, list) or len(vec) != 1408:
        raise ValueError(f"vector_embedding must be a 1408-element list of floats, got length {len(vec) if isinstance(vec, list) else type(vec)}")
```

---

## Streaming CDC Insertion Workflow

To stream audit records asynchronously without blocking FastAPI worker threads:
```python
import asyncio
from google.cloud import bigquery
from price_comp_backend.config import settings

async def stream_audit_to_lakehouse(client: bigquery.Client, record: dict) -> list[dict]:
    """Streams a validated audit record to BigQuery with retry logic."""
    table_id = f"{settings.gcp.project_id}.{settings.bigquery.dataset}.{settings.bigquery.table_name}"
    
    # Run blocking BigQuery client SDK call in asyncio threadpool
    errors = await asyncio.to_thread(
        client.insert_rows_json,
        table=table_id,
        json_rows=[record],
        retry=bigquery.DEFAULT_RETRY
    )
    if errors:
        raise RuntimeError(f"BigQuery streaming insert failed: {errors}")
    return errors
```

---

## Contrastive Triplet Mining (Continuous HITL Learning)

The continuous learning flywheel extracts training triplets `(Anchor, Positive, Negative)` from verified associate actions to fine-tune vector search embeddings:

```sql
WITH verified_audits AS (
  SELECT
    audit_id,
    catalog_item_id,
    vector_embedding,
    associate_action,
    created_at
  FROM `<gcp-project>.retail_cortex.price_comparison_audits`
  WHERE associate_action IN ('EXACT_MATCH', 'OVERRIDDEN_SKU')
    AND created_at >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 30 DAY)
)
SELECT
  a.audit_id AS anchor_id,
  a.vector_embedding AS anchor_embedding,
  p.audit_id AS positive_id,
  p.vector_embedding AS positive_embedding,
  n.audit_id AS negative_id,
  n.vector_embedding AS negative_embedding
FROM verified_audits a
-- Positive match: Same catalog SKU confirmed by associate
JOIN verified_audits p 
  ON a.catalog_item_id = p.catalog_item_id 
  AND a.audit_id != p.audit_id
-- Negative match: Different SKU from competitor catalog
JOIN verified_audits n 
  ON a.catalog_item_id != n.catalog_item_id
LIMIT 5000;
```

---

## Testing & Mocking Strategy

For offline unit tests, mock `google.cloud.bigquery.Client` to avoid cloud charges:
```python
from unittest.mock import MagicMock
import pytest

@pytest.fixture
def mock_bq_client():
    client = MagicMock()
    client.insert_rows_json.return_value = []
    return client

def test_lakehouse_stream_success(mock_bq_client):
    from price_comp_backend.services.lakehouse import LakehouseService
    service = LakehouseService(client=mock_bq_client)
    sample_row = {
        "audit_id": "aud_123", "trace_id": "tr_456", "created_at": "2026-09-08T10:00:00Z",
        "store_id": "100", "associate_id": "assoc_1", "competitor_name": "Target",
        "detected_upc": "012345678905", "detected_price": 3.99, "associate_action": "EXACT_MATCH",
        "vector_embedding": [0.0] * 1408, "confidence_score": 0.95
    }
    result = service.stream_audit(sample_row)
    assert result is True
    mock_bq_client.insert_rows_json.assert_called_once()
```

