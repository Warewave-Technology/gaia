| Screen | Endpoint | anonymous | auditor | operator | admin |
|---|---|---|---|---|---|
| Overview `/` | `GET /api/v1/me` | 401 | 200 | 200 | 200 |
| Overview `/` | `GET /api/v1/system` | 401 | 200 | 200 | 200 |
| Overview `/` | `GET /api/v1/search` | 401 | 200 | 200 | 200 |
| Overview `/` | `GET /api/v1/views/fleet` | 401 | 200 | 200 | 200 |
| Overview `/` | `GET /api/v1/views/capacity` | 401 | 200 | 200 | 200 |
| Overview `/` | `GET /api/v1/views/attention` | 401 | 200 | 200 | 200 |
| Overview `/` | `GET /api/v1/views/coverage` | 401 | 200 | 200 | 200 |
| Overview `/` | `GET /api/v1/views/vm-list` | 401 | 200 | 200 | 200 |
| Overview `/` | `GET /api/v1/hosts` | 401 | 200 | 200 | 200 |
| Overview `/` | `GET /api/v1/policies` | 401 | 200 | 200 | 200 |
| VM `/vms/{id}` | `GET /api/v1/me` | 401 | 200 | 200 | 200 |
| VM `/vms/{id}` | `GET /api/v1/system` | 401 | 200 | 200 | 200 |
| VM `/vms/{id}` | `GET /api/v1/search` | 401 | 200 | 200 | 200 |
| VM `/vms/{id}` | `GET /api/v1/views/vms/{id}` | 401 | 200 | 200 | 200 |
| VM `/vms/{id}` | `GET /api/v1/views/vms/{id}/points` | 401 | 200 | 200 | 200 |
| VM `/vms/{id}` | `GET /api/v1/views/vms/{id}/status-history` | 401 | 200 | 200 | 200 |
| VM `/vms/{id}` | `GET /api/v1/views/vm-list` | 401 | 200 | 200 | 200 |
| VM `/vms/{id}` | `GET /api/v1/policies` | 401 | 200 | 200 | 200 |
| VM `/vms/{id}` | `GET /api/v1/reason-codes` | 401 | 200 | 200 | 200 |
| VM `/vms/{id}` | `GET /api/v1/evidence/{id}/files/{name}` | 401 | 200 | 200 | 200 |
| VM `/vms/{id}` (act) | `POST /api/v1/vms/{id}/backups` | 401 | 403 | 409 | 409 |
| VM `/vms/{id}` (act) | `POST /api/v1/vms/{id}/tests` | 401 | 403 | 409 | 409 |
| VM `/vms/{id}` (act) | `PUT /api/v1/vms/{id}/tags` | 401 | 403 | 400 | 400 |
| VM `/vms/{id}` (act) | `PATCH /api/v1/vms/{id}` | 401 | 403 | 400 | 400 |
| VM `/vms/{id}` (admin) | `POST /api/v1/points/{id}/forget` | 401 | 403 | 403 | 400 |
| Proof capacity `/capacity` | `GET /api/v1/me` | 401 | 200 | 200 | 200 |
| Proof capacity `/capacity` | `GET /api/v1/system` | 401 | 200 | 200 | 200 |
| Proof capacity `/capacity` | `GET /api/v1/search` | 401 | 200 | 200 | 200 |
| Proof capacity `/capacity` | `GET /api/v1/views/capacity` | 401 | 200 | 200 | 200 |
| Proof capacity `/capacity` | `GET /api/v1/views/test-queue` | 401 | 200 | 200 | 200 |
| Proof capacity `/capacity` | `GET /api/v1/capacity/inputs` | 401 | 200 | 200 | 200 |
| Proof capacity `/capacity` | `GET /api/v1/policies` | 401 | 200 | 200 | 200 |
| Proof capacity `/capacity` (any) | `POST /api/v1/capacity/what-if` | 401 | 400 | 400 | 400 |
| Activity `/activity` | `GET /api/v1/me` | 401 | 200 | 200 | 200 |
| Activity `/activity` | `GET /api/v1/system` | 401 | 200 | 200 | 200 |
| Activity `/activity` | `GET /api/v1/search` | 401 | 200 | 200 | 200 |
| Activity `/activity` | `GET /api/v1/views/activity` | 401 | 200 | 200 | 200 |
| Activity `/activity` | `GET /api/v1/hosts` | 401 | 200 | 200 | 200 |
| Inventory `/inventory` | `GET /api/v1/me` | 401 | 200 | 200 | 200 |
| Inventory `/inventory` | `GET /api/v1/system` | 401 | 200 | 200 | 200 |
| Inventory `/inventory` | `GET /api/v1/search` | 401 | 200 | 200 | 200 |
| Inventory `/inventory` | `GET /api/v1/views/hosts` | 401 | 200 | 200 | 200 |
| Inventory `/inventory` | `GET /api/v1/policies` | 401 | 200 | 200 | 200 |
| Inventory `/inventory` | `GET /api/v1/views/coverage` | 401 | 200 | 200 | 200 |
| Inventory `/inventory` | `GET /api/v1/views/repositories` | 401 | 200 | 200 | 200 |
| Inventory `/inventory` (act) | `POST /api/v1/hosts` | 401 | 403 | 400 | 400 |
| Inventory `/inventory` (act) | `POST /api/v1/hosts/{id}/scan` | 401 | 403 | 502 | 502 |
| Inventory `/inventory` (act) | `POST /api/v1/hosts/{id}/trust` | 401 | 403 | 400 | 400 |
| Inventory `/inventory` (act) | `POST /api/v1/hosts/{id}/check` | 401 | 403 | 502 | 502 |
| Inventory `/inventory` (act) | `DELETE /api/v1/hosts/{id}` | 401 | 403 | 409 | 409 |
| Inventory `/inventory` (act) | `POST /api/v1/policies` | 401 | 403 | 400 | 400 |
| Inventory `/inventory` (act) | `PATCH /api/v1/policies/{id}` | 401 | 403 | 400 | 400 |
| Inventory `/inventory` (act) | `DELETE /api/v1/policies/{id}` | 401 | 403 | 409 | 409 |
| Inventory `/inventory` (act) | `POST /api/v1/repositories/{id}/verify` | 401 | 403 | 400 | 400 |
| Inventory `/inventory` (any) | `POST /api/v1/capacity/what-if` | 401 | 400 | 400 | 400 |
| Inventory: users `/inventory/users` | `GET /api/v1/me` | 401 | 200 | 200 | 200 |
| Inventory: users `/inventory/users` | `GET /api/v1/system` | 401 | 200 | 200 | 200 |
| Inventory: users `/inventory/users` | `GET /api/v1/search` | 401 | 200 | 200 | 200 |
| Inventory: users `/inventory/users` | `GET /api/v1/users` | 401 | 200 | 403 | 200 |
| Inventory: users `/inventory/users` (admin) | `POST /api/v1/users` | 401 | 403 | 403 | 400 |
| Inventory: users `/inventory/users` (admin) | `PATCH /api/v1/users/{id}` | 401 | 403 | 403 | 400 |
| Inventory: users `/inventory/users` (admin) | `POST /api/v1/users/{id}/factors/reset` | 401 | 403 | 403 | 204 |
| Inventory: users `/inventory/users` (admin) | `POST /api/v1/users/{id}/unlock` | 401 | 403 | 403 | 200 |
| Inventory: audit log `/inventory/audit` | `GET /api/v1/me` | 401 | 200 | 200 | 200 |
| Inventory: audit log `/inventory/audit` | `GET /api/v1/system` | 401 | 200 | 200 | 200 |
| Inventory: audit log `/inventory/audit` | `GET /api/v1/search` | 401 | 200 | 200 | 200 |
| Inventory: audit log `/inventory/audit` | `GET /api/v1/views/audit` | 401 | 200 | 403 | 200 |
| Migration `/migration` | `GET /api/v1/me` | 401 | 200 | 200 | 200 |
| Migration `/migration` | `GET /api/v1/system` | 401 | 200 | 200 | 200 |
| Migration `/migration` | `GET /api/v1/search` | 401 | 200 | 200 | 200 |
| Migration `/migration` | `GET /api/v1/views/migration` | 401 | 200 | 200 | 200 |
| Migration `/migration` | `GET /api/v1/evidence/{id}/files/{name}` | 401 | 200 | 200 | 200 |
| Migration `/migration` | `GET /api/v1/migration-checks/{id}/report` | 401 | 200 | 200 | 200 |
| Migration `/migration` (act) | `GET /api/v1/migration-sources/{id}/inventory` | 401 | 403 | 502 | 502 |
| Migration `/migration` (act) | `POST /api/v1/migration-sources/{id}/checks` | 401 | 403 | 400 | 400 |
| Evidence `/evidence` | `GET /api/v1/me` | 401 | 200 | 200 | 200 |
| Evidence `/evidence` | `GET /api/v1/system` | 401 | 200 | 200 | 200 |
| Evidence `/evidence` | `GET /api/v1/search` | 401 | 200 | 200 | 200 |
| Evidence `/evidence` | `GET /api/v1/ledger` | 401 | 200 | 200 | 200 |
| Evidence `/evidence` | `GET /api/v1/daily-reports` | 401 | 200 | 200 | 200 |
| Evidence `/evidence` | `GET /api/v1/evidence` | 401 | 200 | 200 | 200 |
| Evidence `/evidence` | `GET /api/v1/views/evidence` | 401 | 200 | 200 | 200 |
| Evidence `/evidence` | `GET /api/v1/views/fleet` | 401 | 200 | 200 | 200 |
| Evidence `/evidence` | `GET /api/v1/vms/{id}` | 401 | 200 | 200 | 200 |
| Evidence `/evidence` | `GET /api/v1/evidence-export` | 401 | 200 | 403 | 200 |
| Evidence `/evidence` (admin) | `POST /api/v1/ledger/verify` | 401 | 403 | 403 | 200 |
| Approve a CLI sign-in `/approve-cli` | `GET /api/v1/me` | 401 | 200 | 200 | 200 |
| Approve a CLI sign-in `/approve-cli` (any) | `POST /api/v1/login/cli/approve` | 401 | 400 | 400 | 400 |
| Approve a CLI step-up `/approve-step-up` | `GET /api/v1/me` | 401 | 200 | 200 | 200 |
| Approve a CLI step-up `/approve-step-up` (any) | `POST /api/v1/step-up/approve` | 401 | 400 | 400 | 400 |
| Inventory: sign-in and security `/inventory/security` | `GET /api/v1/me` | 401 | 200 | 200 | 200 |
| Inventory: sign-in and security `/inventory/security` | `GET /api/v1/system` | 401 | 200 | 200 | 200 |
| Inventory: sign-in and security `/inventory/security` | `GET /api/v1/search` | 401 | 200 | 200 | 200 |
| Inventory: sign-in and security `/inventory/security` | `GET /api/v1/settings/signin` | 401 | 200 | 403 | 200 |
| Inventory: sign-in and security `/inventory/security` | `GET /api/v1/sessions` | 401 | 403 | 403 | 200 |
| Inventory: sign-in and security `/inventory/security` (admin) | `PUT /api/v1/settings/signin` | 401 | 403 | 403 | 400 |
| Inventory: sign-in and security `/inventory/security` (admin) | `DELETE /api/v1/sessions/{id}` | 401 | 403 | 403 | 400 |
| Inventory: sign-in and security `/inventory/security` (admin) | `POST /api/v1/sessions/end-others` | 401 | 403 | 403 | 200 |
