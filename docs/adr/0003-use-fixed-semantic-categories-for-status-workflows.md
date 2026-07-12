---
status: accepted
---

# Use fixed semantic categories for status workflows

Status workflows will be organized around fixed semantic categories rather than deriving meaning from configurable labels. Each category may have multiple custom statuses, each with a unique label and color within its object type, and one status is the default for that category. Labels and colors provide local vocabulary and presentation while categories remain stable for behavior and reporting.

Jobs use New, Travel Time, In Progress, Pending, Completed, and Canceled. Projects use New, In Progress, Pending, Completed, and Canceled. Estimates use Draft, Estimate, Sent, Accepted, Rejected, and Completed. Any active Estimate may be converted; conversion is not a capability granted by a particular status or category.

This decision supersedes the status-granted Estimate conversion capability in ADR 0002. It refines ADR 0001's separation of workflow and settlement: Partially Paid and Paid are fixed effective-status slots derived from Settlement State, not manual workflow choices.

Moving a status within its category changes its local order. Moving it to another category changes its semantic category and places it in that category's order. A category cannot be moved or deleted. An in-use status may be deleted only when a same-category replacement is supplied; all active, archived, and conversion-hidden records are reassigned atomically. Deleting a default requires a same-category replacement, which becomes the default. An unused non-default may be deleted without replacement unless it is the category's last status. Changing a default affects future category entries only; it does not rewrite existing objects. Every category must retain a default status.

Invoices use exactly six semantic slots: Draft, Invoiced, Sent, Partially Paid, Paid, and Void. Labels and colors may be customized, but slots cannot be added, removed, or reordered. Draft, Invoiced, Sent, and Void are manual workflow choices. Partially Paid and Paid are derived from Settlement State and do not replace the preserved manual choice. Effective Invoice Status uses this precedence: manual Void always displays Void; manual Draft with no active settlement displays Draft; active partial settlement displays Partially Paid; a Paid settlement with an active effect, or a manually non-Draft zero-total Invoice whose projection is Paid, displays Paid; otherwise the preserved Manual Invoice Status is displayed.

Status reporting groups by category or Invoice slot, not by mutable labels. Every status transition atomically changes the object and records its activity so history cannot disagree with current state.

## Considered Options

- Make all statuses semantically free-form. Rejected because behavior, cross-company reporting, and lifecycle rules would depend on mutable labels or administrator convention.
- Provide one configurable linear workflow shared by all object types. Rejected because Jobs, Projects, Estimates, and Invoices have different lifecycle meanings and constraints.
- Allow administrators to add, remove, or reorder semantic categories and Invoice slots. Rejected because stable behavior and reporting require a fixed semantic vocabulary.
- Permit duplicate labels within an object type and distinguish them by category or color. Rejected because users could not identify a status unambiguously in controls, activity, or reports.
- Infer Estimate convertibility from a category or configurable status capability. Rejected because every active Estimate is a proposal that may be converted, and category-dependent conversion would add an unnecessary workflow gate.
- Replace an Invoice's manual status when settlement changes. Rejected because undoing or reversing settlement must reveal the workflow state that applied before the monetary override.
- Treat SMTP acceptance as successful delivery. Rejected for status workflow purposes because acceptance concerns transmission, not receipt; the separate delivery decision defines its evidence and lifecycle.

## Consequences

- Administrators retain control over workflow vocabulary, colors, status ordering, and category defaults without changing domain semantics.
- Category changes can alter the meaning of a custom status and therefore require the same audited transition discipline as object status changes.
- Fixed categories make reports comparable even when organizations use different labels.
- Invoice settlement temporarily takes precedence over manual workflow without destroying the manual choice.
- Invoice reporting uses Effective Invoice Status when reporting current state and may separately expose Settlement State and Manual Invoice Status when that distinction matters.
- Status mutation and its activity entry share one atomic boundary; a failed activity write must fail the transition.
