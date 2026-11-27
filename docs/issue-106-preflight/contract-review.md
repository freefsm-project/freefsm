# #106 early contract review

Reviewed `implementation-contract.md` against GitHub #106/#18 and existing domain boundaries. This is interface/spec review only; unfinished implementation is not judged as completed. No runtime tests or Git/GitHub mutations.

## 1. Scalar allowlist narrows the stated search requirement

**Location:** contract lines 69–70.

The contract explicitly supports custom scalar fields, then limits built-ins to identifiers/names/descriptions and named relationships, with “No arbitrary financial amount indexing.” As written this excludes authorized built-in number/date/option fields rather than just forbidding unrestricted financial JSON/index traversal.

**Normative evidence:** #106 AC2 requires authorized “identifiers, names, descriptions and active scalar text, number, date and option fields”; DISC-03 likewise specifies authorized scalar text-number-date-options. Neither states that scalar fields are custom-only or excludes authorized numeric financial fields. MAS-07/PER-05 independently protect Pricebook and Cost; they do not make authorized scalar price/cost values categorically unsearchable. Existing `authorization/item.go:17–21,37–42` and `item_boundary.go:90–100` identify UnitPrice/TaxRate as Pricebook and UnitCost as Cost.

**Smallest correction:** make the field manifest explicitly include applicable built-in scalar fields as well as custom fields. Include stored Item price/tax/cost numeric terms only with their respective independent View grants; likewise enumerate other applicable stored numbers/dates/options by owning facet. Exclude derived financial aggregates/history/child ledgers unless independently required, rather than excluding financial scalar fields wholesale. Specify canonical numeric/date/option representation so coverage is executable. If “no arbitrary” was intended only to forbid unbounded indexing, clarify that wording now; the current affirmative built-in allowlist is narrower than AC2.

## 2. Successful-query persistence lacks a CSRF-safe transport contract

**Location:** contract orchestration lines 113–117.

Reset is explicitly CSRF-protected POST, but the preceding instructions persist successful state during ordinary URL/read orchestration without specifying an equivalent protected write. Implementing those steps directly in list GET handlers would allow a cross-site top-level navigation carrying attacker-selected filters to write a durable per-user preference. CAS is concurrency control, not proof of user-origin intent.

**Normative evidence:** ACC-03 retains CSRF protection; AUTH-03 treats presentation as an intrinsic **own-user** action, not unauthenticated write authority. DISC-03 requires shareable URL reads and last-used user state, but grants no exception to request protection. Existing `middleware/auth.go:129–135` supplies the browser CSRF boundary. Moving from the older Lease cookie to durable server state makes this a shared write-interface decision.

**Smallest correction:** separate authorized GET rendering from preference persistence. Use a CSRF-protected same-origin POST carrying canonical successful-query state and the originally loaded revision, revalidate actor/state/authority server-side, and keep Reset on the same protected CAS boundary. Do not accept a client assertion of authorization or blindly echo unvalidated state. GET shared URLs remain usable immediately; a failed preference write is reported distinctly without claiming persistence. An equivalent explicitly documented CSRF-safe write mechanism is acceptable.

No additional blocking conflict found in the contract's Lease base-or-Billing entitlement, Item independent-facet intent, authorization-before-SQL matching, applicable active field-definition rule, explicit archived-mode grants, ten-main-collection scope, Reset tombstones, or single-owner refreshed history design. That conclusion concerns the written design, not implementation correctness or acceptance.
