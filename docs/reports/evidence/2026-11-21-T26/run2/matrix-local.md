| Endpoint | Allowed | anonymous | auditor | operator | admin |
|---|---|---|---|---|---|
| `DELETE /api/v1/api-tokens/{id}` | admin | 401 | 403 | 403 | ↑ 404 |
| `GET /api/v1/api-tokens` | admin | 401 | 403 | 403 | 200 |
| `POST /api/v1/api-tokens` | admin | 401 | 403 | 403 | ↑ 201 |
| `GET /api/v1/audit/verify` | admin, auditor | 401 | 200 | 403 | 200 |
| `GET /api/v1/audit` | admin, auditor | 401 | 200 | 403 | 200 |
| `GET /api/v1/capacity/inputs` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/capacity/what-if` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/converter` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/daily-reports/{id}` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `GET /api/v1/daily-reports` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/daily-reports` | admin | 401 | 403 | 403 | 201 |
| `GET /api/v1/evidence-export` | admin, auditor | 401 | ↑ 500 | 403 | ↑ 500 |
| `GET /api/v1/evidence/{id}/export` | admin, auditor | 401 | ↑ 404 | 403 | ↑ 404 |
| `GET /api/v1/evidence/{id}/files/{name}` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `GET /api/v1/evidence/{id}/verify` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `GET /api/v1/evidence/{id}` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `GET /api/v1/evidence` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/health` | (public) | 200 | 200 | 200 | 200 |
| `POST /api/v1/hosts/{id}/check` | admin, operator | 401 | 403 | 404 | 404 |
| `POST /api/v1/hosts/{id}/scan` | admin, operator | 401 | 403 | 404 | 404 |
| `POST /api/v1/hosts/{id}/trust` | admin, operator | 401 | 403 | 404 | 404 |
| `DELETE /api/v1/hosts/{id}` | admin, operator | 401 | 403 | ↑ 404 | ↑ 404 |
| `GET /api/v1/hosts/{id}` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `PATCH /api/v1/hosts/{id}` | admin, operator | 401 | 403 | 404 | 404 |
| `GET /api/v1/hosts` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/hosts` | admin, operator | 401 | 403 | ↑ 201 | ↑ 409 |
| `GET /api/v1/jobs/{id}` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `GET /api/v1/jobs` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/ledger/verify` | admin | 401 | 403 | 403 | 200 |
| `GET /api/v1/ledger` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/login/cli/approve` | admin, operator, auditor | 401 | ↑ 404 | ↑ 404 | ↑ 404 |
| `POST /api/v1/login/cli/token` | (public) | 401 | 401 | 401 | 401 |
| `POST /api/v1/login/cli` | (public) | 200 | 200 | 200 | 200 |
| `POST /api/v1/login/enrol/finish` | (public) | 401 | 401 | 401 | 401 |
| `POST /api/v1/login/enrol` | (public) | 401 | 401 | 401 | 401 |
| `POST /api/v1/login/factor` | (public) | 401 | 401 | 401 | 401 |
| `POST /api/v1/login/password` | (public) | 401 | 401 | 401 | 401 |
| `POST /api/v1/login` | (public) | 401 | · | · | · |
| `POST /api/v1/logout` | admin, operator, auditor | 401 | 204 | 204 | 204 |
| `POST /api/v1/me/factors/finish` | admin, operator, auditor | 401 | 400 | 400 | 400 |
| `DELETE /api/v1/me/factors/{id}` | admin, operator, auditor | 401 | ↑ 404 | ↑ 404 | ↑ 404 |
| `GET /api/v1/me/factors` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/me/factors` | admin, operator, auditor | 401 | ↑ 200 | ↑ 200 | ↑ 200 |
| `PUT /api/v1/me/password` | admin, operator, auditor | 401 | ↑ 400 | ↑ 400 | ↑ 400 |
| `POST /api/v1/me/recovery-codes` | admin, operator, auditor | 401 | ↑ 200 | ↑ 200 | ↑ 200 |
| `DELETE /api/v1/me/sessions/{id}` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `GET /api/v1/me/sessions` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/me` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/migration-checks/{id}/report` | admin, operator, auditor | 401 | ↑ 404 | ↑ 404 | ↑ 404 |
| `GET /api/v1/migration-checks/{id}` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `GET /api/v1/migration-checks` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/migration-sources/{id}/checks` | admin, operator | 401 | 403 | 404 | 404 |
| `GET /api/v1/migration-sources/{id}/inventory` | admin, operator | 401 | 403 | 404 | 404 |
| `DELETE /api/v1/migration-sources/{id}` | admin, operator | 401 | 403 | 404 | 404 |
| `GET /api/v1/migration-sources/{id}` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `GET /api/v1/migration-sources` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/migration-sources` | admin, operator | 401 | 403 | 201 | 409 |
| `POST /api/v1/notification-channels/{id}/test` | admin | 401 | 403 | 403 | 404 |
| `GET /api/v1/notification-channels/{id}` | admin | 401 | 403 | 403 | 404 |
| `PATCH /api/v1/notification-channels/{id}` | admin | 401 | 403 | 403 | 404 |
| `GET /api/v1/notification-channels` | admin | 401 | 403 | 403 | 200 |
| `POST /api/v1/notification-channels` | admin | 401 | 403 | 403 | 201 |
| `GET /api/v1/notifications` | admin, operator | 401 | 403 | 200 | 200 |
| `GET /api/v1/oidc/callback` | (public) | 302 | 302 | 302 | 302 |
| `POST /api/v1/oidc/device/token` | (public) | 400 | 400 | 400 | 400 |
| `POST /api/v1/oidc/device` | (public) | 400 | 400 | 400 | 400 |
| `GET /api/v1/oidc/start` | (public) | 400 | 400 | 400 | 400 |
| `GET /api/v1/openapi.json` | (public) | 200 | 200 | 200 | 200 |
| `GET /api/v1/points/{id}/disks` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `POST /api/v1/points/{id}/forget` | admin | 401 | 403 | 403 | ↑ 400 |
| `POST /api/v1/points/{id}/restore-check` | admin, operator | 401 | 403 | 404 | 404 |
| `GET /api/v1/points/{id}` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `GET /api/v1/points` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `DELETE /api/v1/policies/{id}` | admin, operator | 401 | 403 | 404 | 404 |
| `GET /api/v1/policies/{id}` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `PATCH /api/v1/policies/{id}` | admin, operator | 401 | 403 | 404 | 404 |
| `GET /api/v1/policies` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/policies` | admin, operator | 401 | 403 | 409 | 409 |
| `GET /api/v1/quarantined-contents` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/reason-codes` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/recovery-kit/confirm` | admin | 401 | 403 | 403 | 409 |
| `GET /api/v1/recovery-kit/file` | admin | 401 | 403 | 403 | 200 |
| `GET /api/v1/recovery-kit` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/recovery-kit` | admin | 401 | 403 | 403 | 400 |
| `POST /api/v1/repositories/{id}/maintenance` | admin | 401 | 403 | 403 | 404 |
| `POST /api/v1/repositories/{id}/verify` | admin, operator | 401 | 403 | 404 | 404 |
| `GET /api/v1/repositories/{id}` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `GET /api/v1/repositories` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/repositories` | admin | 401 | 403 | 403 | ↑ 400 |
| `GET /api/v1/restore-tests/{id}` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `GET /api/v1/restore-tests` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/search` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/sessions/end-others` | admin | 401 | 403 | 403 | 200 |
| `DELETE /api/v1/sessions/{id}` | admin | 401 | 403 | 403 | 404 |
| `GET /api/v1/sessions` | admin | 401 | 403 | 403 | 200 |
| `GET /api/v1/settings/signin` | admin, auditor | 401 | 200 | 403 | 200 |
| `PUT /api/v1/settings/signin` | admin | 401 | 403 | 403 | ↑ 200 |
| `GET /api/v1/signin-options` | (public) | 200 | 200 | 200 | 200 |
| `GET /api/v1/state-backups` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/state-backups` | admin | 401 | 403 | 403 | 409 |
| `GET /api/v1/state-changes` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/status/check` | admin, auditor | 401 | 200 | 403 | 200 |
| `POST /api/v1/status/recompute` | admin | 401 | 403 | 403 | 200 |
| `GET /api/v1/status` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/step-up/approve` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `GET /api/v1/step-up/{sid}` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `POST /api/v1/step-up` | admin, operator, auditor | 401 | 400 | 400 | 400 |
| `GET /api/v1/system` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/users/{id}/factors/reset` | admin | 401 | 403 | 403 | ↑ 204 |
| `POST /api/v1/users/{id}/unlock` | admin | 401 | 403 | 403 | ↑ 200 |
| `GET /api/v1/users/{id}` | admin, auditor | 401 | 200 | 403 | 200 |
| `PATCH /api/v1/users/{id}` | admin | 401 | 403 | 403 | ↑ 200 |
| `GET /api/v1/users` | admin, auditor | 401 | 200 | 403 | 200 |
| `POST /api/v1/users` | admin | 401 | 403 | 403 | ↑ 201 |
| `GET /api/v1/verifications/{id}` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `GET /api/v1/verifications` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/views/attention` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/views/capacity` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/views/coverage` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/views/fleet` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/views/migration` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/views/overview` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/views/test-queue` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/views/vm-list` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `GET /api/v1/views/vms/{id}` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `POST /api/v1/vms/{id}/backups` | admin, operator | 401 | 403 | 404 | 404 |
| `GET /api/v1/vms/{id}/disks` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `POST /api/v1/vms/{id}/disks` | admin, operator | 401 | 403 | 404 | 404 |
| `GET /api/v1/vms/{id}/status` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `PUT /api/v1/vms/{id}/tags` | admin, operator | 401 | 403 | 404 | 404 |
| `POST /api/v1/vms/{id}/tests` | admin, operator | 401 | 403 | 404 | 404 |
| `DELETE /api/v1/vms/{id}` | admin, operator | 401 | 403 | 404 | 404 |
| `GET /api/v1/vms/{id}` | admin, operator, auditor | 401 | 404 | 404 | 404 |
| `PATCH /api/v1/vms/{id}` | admin, operator | 401 | 403 | 404 | 404 |
| `GET /api/v1/vms` | admin, operator, auditor | 401 | 200 | 200 | 200 |
| `POST /api/v1/vms` | admin, operator | 401 | 403 | 201 | 409 |
