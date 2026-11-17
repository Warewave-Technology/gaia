| Screen | Endpoint | anonymous | auditor | operator | admin |
|---|---|---|---|---|---|
| Overview `/` | `GET /api/v1/me` | 401 | 200 | 200 | 200 |
| Overview `/` | `GET /api/v1/system` | 401 | 200 | 200 | 200 |
| Overview `/` | `GET /api/v1/views/overview` | 401 | 200 | 200 | 200 |
| VM `/vms/{id}` | `GET /api/v1/me` | 401 | 200 | 200 | 200 |
| VM `/vms/{id}` | `GET /api/v1/system` | 401 | 200 | 200 | 200 |
| VM `/vms/{id}` | `GET /api/v1/views/vms/{id}` | 401 | 200 | 200 | 200 |
| VM `/vms/{id}` | `GET /api/v1/evidence/{id}/files/{name}` | 401 | 200 | 200 | 200 |
| VM `/vms/{id}` (act) | `POST /api/v1/vms/{id}/backups` | 401 | 403 | 409 | 409 |
| VM `/vms/{id}` (act) | `POST /api/v1/vms/{id}/tests` | 401 | 403 | 409 | 409 |
| Evidence `/evidence` | `GET /api/v1/me` | 401 | 200 | 200 | 200 |
| Evidence `/evidence` | `GET /api/v1/system` | 401 | 200 | 200 | 200 |
| Evidence `/evidence` | `GET /api/v1/evidence` | 401 | 200 | 200 | 200 |
| Evidence `/evidence` | `GET /api/v1/vms` | 401 | 200 | 200 | 200 |
| Evidence `/evidence` | `GET /api/v1/daily-reports` | 401 | 200 | 200 | 200 |
| Evidence `/evidence` | `GET /api/v1/evidence-export` | 401 | 200 | 403 | 200 |
