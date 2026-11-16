| Endpoint | Allowed | anonymous | auditor | operator | admin |
|---|---|---|---|---|---|
| `POST /api/v1/login` | (public) | 401 | 401 | 401 | 401 |
| `POST /api/v1/logout` | admin, operator, auditor | 401 | 204 | 204 | 204 |
| `GET /api/v1/me` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `PUT /api/v1/me/password` | admin, operator, auditor | 401 | 400 | 400 | 400 |
| `GET /api/v1/health` | (public) | 200 | 200 | 200 | 200 |
| `GET /api/v1/openapi.json` | (public) | 200 | 200 | 200 | 200 |
| `GET /api/v1/system` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/users` | admin, auditor | 401 | 200 | 403 | 200 |
| `POST /api/v1/users` | admin | 401 | 403 | 403 | 201 |
| `GET /api/v1/users/{id}` | admin, auditor | 401 | 200 | 403 | 200 |
| `PATCH /api/v1/users/{id}` | admin | 401 | 403 | 403 | 200 |
| `GET /api/v1/hosts` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/hosts` | admin, operator | 401 | 403 | 201 | 409 |
| `GET /api/v1/hosts/{id}` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `PATCH /api/v1/hosts/{id}` | admin, operator | 401 | 403 | 200 | 200 |
| `DELETE /api/v1/hosts/{id}` | admin, operator | 401 | 403 | 409 | 409 |
| `GET /api/v1/vms` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/vms` | admin, operator | 401 | 403 | 201 | 409 |
| `GET /api/v1/vms/{id}` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `PATCH /api/v1/vms/{id}` | admin, operator | 401 | 403 | 200 | 200 |
| `DELETE /api/v1/vms/{id}` | admin, operator | 401 | 403 | 409 | 409 |
| `GET /api/v1/vms/{id}/disks` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/vms/{id}/disks` | admin, operator | 401 | 403 | 201 | 409 |
| `GET /api/v1/repositories` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/repositories` | admin | 401 | 403 | 403 | 501 |
| `GET /api/v1/repositories/{id}` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/policies` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/policies` | admin, operator | 401 | 403 | 201 | 409 |
| `GET /api/v1/policies/{id}` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `PATCH /api/v1/policies/{id}` | admin, operator | 401 | 403 | 200 | 200 |
| `DELETE /api/v1/policies/{id}` | admin, operator | 401 | 403 | 409 | 409 |
| `GET /api/v1/jobs` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/jobs/{id}` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/points` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/points/{id}` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/restore-tests` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/restore-tests/{id}` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/evidence` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/evidence/{id}` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/evidence/{id}/export` | admin, auditor | 401 | 200 | 403 | 200 |
| `GET /api/v1/state-changes` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/audit` | admin, auditor | 401 | 200 | 403 | 200 |
| `GET /api/v1/audit/verify` | admin, auditor | 401 | 200 | 403 | 200 |
| `GET /api/v1/state-backups` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/state-backups` | admin | 401 | 403 | 403 | 201 |
