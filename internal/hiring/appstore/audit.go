package appstore

// The applications service's own audit and compliance trails (migration 000021): hiring.app_audit_events holds
// "application.received", ".viewed", ".file_downloaded", ".status_changed"; hiring.app_compliance_log holds the
// retention deletion record. Both are written in the same transaction as the change they record. Hiring keeps its
// own job audit in hiring.audit_events; the combined view of a job is the UNION documented in
// docs/microservices/applications.md.
//
// Neither table has a foreign key, so entries outlive the application, the job and the user. The compliance log is
// append-only and holds the application id and the reason, nothing about the candidate.
const insertAuditSQL = `
INSERT INTO hiring.app_audit_events (job_id, tenant_id, actor_id, action, diff)
VALUES ($1::uuid, $2::uuid, NULLIF($3, '')::uuid, $4, $5::jsonb)`

const complianceDeletionSQL = `
INSERT INTO hiring.app_compliance_log (job_id, tenant_id, gate_id, severity, event)
VALUES ($1::uuid, $2::uuid, 'retention', 'informational', $3)`
