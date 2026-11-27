package authorization

// discoveryLeaseBillingTerms projects current agreement allocations rather than
// original Draft storage or generated Invoice history. It is the SQL counterpart
// of replayLeaseBillingAllocations: revisions precede same-day coverage events;
// Add starts fresh terms, Replace carries the outgoing allocation's current
// terms, and Remove/withdraw discard membership. Like the owning replay's
// "coverage" mode, same-day effective coverage participates. Discovery takes its
// active allocations (not that mode's separate one-time charges): these are
// current agreement fields, not a calculation of a month's Invoice entitlement.
//
// Finalized agreement fields are as of the company date, not charge
// entitlement: termination does not erase the retained agreement's scalar
// fields. Future revisions/events never match. Drafts use prospective draft
// fields. All reads, replay, rank and count remain inside the
// single discovery statement snapshot and this expression is included only
// after independent lease.billing.view authorization.
const discoveryLeaseBillingTerms = `SELECT v.value,false,'Lease Billing terms match'
 FROM (
  SELECT x.initial_cents,x.rent_cents,x.tax_rate::text tax_rate
  FROM lease_draft_asset_terms x
  WHERE x.lease_id=l.id AND l.lifecycle='draft'
  UNION ALL
  SELECT (term->>'InitialCents')::bigint,(term->>'RentCents')::bigint,term->>'TaxRate'
  FROM LATERAL (
   WITH RECURSIVE
   events AS (
    SELECT row_number() OVER(ORDER BY effective_day,priority,id,asset) seq,kind,asset,outgoing,term
    FROM (
     SELECT r.effective_on effective_day,0 priority,r.id,c->>'asset_id' asset,
      'revise'::text kind,''::text outgoing,
      jsonb_build_object('RentCents',c->'rent_cents','TaxRate',c->'tax_rate') term
     FROM lease_billing_revisions r CROSS JOIN LATERAL jsonb_array_elements(r.after_value) c
     WHERE r.lease_id=l.id AND r.effective_on<=lease_dependency_today(l.company_id)
     UNION ALL
     SELECT (c.payload->>'effective_on')::date,1,c.id,c.payload->>'incoming_id',
      c.payload->>'kind',c.payload->>'outgoing_id',
      jsonb_build_object('AssetID',c.payload->'incoming_id','InitialCents',coalesce(nullif(c.payload->'charge_cents','null'::jsonb),'0'::jsonb),
       'RentCents',c.payload->'rent_cents','TaxRate',c.payload->'tax_rate','Selected',true)
     FROM lease_coverage_current c
     WHERE c.lease_id=l.id AND c.action<>'withdraw'
      AND (c.payload->>'effective_on')::date<=lease_dependency_today(l.company_id)
    ) ordered
   ),
   replay(seq,allocations) AS (
    SELECT 0::bigint,coalesce(jsonb_object_agg(t->>'AssetID',t),'{}'::jsonb)
    FROM lease_finalizations f CROSS JOIN LATERAL jsonb_array_elements(f.terms) t
    WHERE f.lease_id=l.id AND (t->>'Selected')::boolean
    UNION ALL
    SELECT e.seq,CASE e.kind
     WHEN 'revise' THEN CASE WHEN r.allocations ? e.asset
      THEN jsonb_set(r.allocations,ARRAY[e.asset],(r.allocations->e.asset)||e.term) ELSE r.allocations END
     WHEN 'add' THEN r.allocations||jsonb_build_object(e.asset,e.term)
     WHEN 'remove' THEN r.allocations-e.outgoing
     WHEN 'replace' THEN (r.allocations-e.outgoing)||jsonb_build_object(e.asset,
      (r.allocations->e.outgoing)||jsonb_build_object('AssetID',e.asset::bigint,'InitialCents',0))
     END
    FROM replay r JOIN events e ON e.seq=r.seq+1
   )
   SELECT allocations FROM replay ORDER BY seq DESC LIMIT 1
  ) current_terms CROSS JOIN LATERAL jsonb_each(current_terms.allocations) allocation(asset,term)
  WHERE l.lifecycle<>'draft'
 ) terms CROSS JOIN LATERAL unnest(ARRAY[
 (terms.initial_cents::numeric/100)::numeric(30,2)::text,
 (terms.rent_cents::numeric/100)::numeric(30,2)::text,terms.tax_rate]) v(value)`
