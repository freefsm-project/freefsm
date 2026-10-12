-- Forward correction of 055. Preserve recorded grants; never infer or add
-- nonprotected grants during upgrade. New scopes reach Administrator only.
SELECT pg_advisory_xact_lock(190019);
INSERT INTO authorization_permissions(permission,scope,prerequisite) VALUES
 ('estimate.export','J','estimate.view'),('invoice.export','J','invoice.view');
UPDATE authorization_permissions SET prerequisite='job.view' WHERE permission='job.export';
UPDATE authorization_permissions SET prerequisite='time.gps.view' WHERE permission='time.gps.export';
UPDATE authorization_permissions SET prerequisite=replace(permission,'.restore','.archived.view')
 WHERE permission LIKE '%.restore' AND scope='A';
UPDATE authorization_permissions SET prerequisite='credit.view' WHERE permission IN ('credit.apply','credit.refund');
UPDATE authorization_permissions SET prerequisite='settlement.view'
 WHERE permission IN ('payment.reverse','credit.application.reverse','credit.refund.reverse');
UPDATE authorization_permissions SET prerequisite='audit.view' WHERE permission='audit.export';
UPDATE authorization_permissions SET prerequisite=replace(permission,'.manage','.view')
 WHERE permission LIKE 'security.%.manage';

-- A requirement is an AND clause; its listed alternatives are OR choices.
-- These are fixed product catalog rules, not user-defined policies. A matching
-- alternative never authorizes a different parent or facet at runtime.
CREATE TABLE authorization_view_requirements (
 permission text NOT NULL,
 scope text NOT NULL,
 requirement text NOT NULL,
 view_permission text NOT NULL,
 view_scope text NOT NULL,
 PRIMARY KEY(permission,scope,requirement,view_permission,view_scope),
 FOREIGN KEY(permission,scope) REFERENCES authorization_permissions(permission,scope),
 FOREIGN KEY(view_permission,view_scope) REFERENCES authorization_permissions(permission,scope)
);
UPDATE authorization_permissions SET prerequisite=NULL WHERE permission='pricing.view';
INSERT INTO authorization_view_requirements
 SELECT 'pricing.view',s,'parent',p||'.view',s
 FROM unnest(ARRAY['A','J']) s CROSS JOIN unnest(ARRAY['job','estimate','invoice']) p;

INSERT INTO authorization_view_requirements
 SELECT 'activity.export','A','parent',p||'.view','A'
 FROM unnest(ARRAY['customer','contact','location','asset','project','job','estimate','invoice','item','time','service_entry','lease']) p;
INSERT INTO authorization_view_requirements
 SELECT 'activity.export','J','parent',p||'.view','J'
 FROM unnest(ARRAY['job','estimate','invoice','time','service_entry']) p;

INSERT INTO authorization_view_requirements
 SELECT a,'A','parent',p||'.view','A'
 FROM unnest(ARRAY['comment.add','comment.remove','file.download','file.upload','file.rename','file.remove','tag.attach','tag.detach']) a
 CROSS JOIN unnest(ARRAY['customer','project','job','asset','estimate','invoice','lease']) p;
INSERT INTO authorization_view_requirements
 SELECT a,'J','parent',p||'.view','J'
 FROM unnest(ARRAY['comment.add','comment.remove','file.download','file.upload','file.rename','file.remove','tag.attach','tag.detach']) a
 CROSS JOIN unnest(ARRAY['job','estimate','invoice']) p;
-- Own is an author/uploader anchor intersected with parent visibility, not
-- an imaginary Own-scoped Job/Customer View and never Context-wide collaboration.
INSERT INTO authorization_view_requirements
 SELECT a,'O','parent',p||'.view',CASE WHEN p IN ('job','estimate','invoice') THEN 'J' ELSE 'A' END
 FROM unnest(ARRAY['comment.remove','file.rename','file.remove']) a
 CROSS JOIN unnest(ARRAY['customer','project','job','asset','estimate','invoice','lease']) p;
INSERT INTO authorization_view_requirements VALUES ('credit.apply','A','selected-invoice','invoice.view','A');

-- Old approvals must not silently approve a changed catalog contract. Raw
-- grants remain available for explicit repair; effective authority filters
-- unmet prerequisites rather than granting new views or discarding history.
UPDATE authorization_roles r SET revision=revision+1 WHERE NOT protected AND EXISTS (
 SELECT 1 FROM authorization_grants g WHERE g.role_id=r.id AND (
 g.permission IN ('job.export','time.gps.export','pricing.view','activity.export',
 'comment.add','comment.remove','file.download','file.upload','file.rename','file.remove','tag.attach','tag.detach',
 'credit.apply','credit.refund','payment.reverse','credit.application.reverse','credit.refund.reverse')
 OR g.permission LIKE '%.restore'));
