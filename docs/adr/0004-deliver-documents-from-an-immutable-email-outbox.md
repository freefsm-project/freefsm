---
status: accepted
---

# Deliver documents from an immutable email outbox snapshot

Each requested document email will create an immutable database outbox snapshot containing the complete message inputs and the rendered PDF. Later edits to the source document, customer, recipient, or template will not alter what a retry sends. Each delivery has a stable message identifier across attempts and progresses through Queued, Sending, Accepted, Delivered, Bounced, or Failed.

The transport may establish Accepted from a successful handoff, including generic SMTP acceptance, but acceptance is not evidence of delivery. Delivered and Bounced require provider evidence. Where a transport cannot provide later evidence, Accepted is the furthest state it can establish. Provider evidence is retained so the asserted delivery state remains explainable.

Automatic retries are bounded and apply only to retryable failures. An authorized user may manually retry a Failed or Bounced delivery; a retry remains part of the same logical delivery and uses its original snapshot and stable message identifier. Email delivery is necessarily at least once at the external boundary: ambiguous transport failures and retries can result in a recipient receiving duplicates even though internal requests and attempts are controlled.

Open tracking is optional and disabled by default. When enabled, only the first observed open time, most recent observed open time, and observation count are retained. Open signals are not proof that a person read the message and do not determine delivery or document status.

For a document whose expected underlying status is eligible to become Sent, Accepted changes that status to Sent only when it is still the status observed when delivery was requested. This comparison prevents a delayed acceptance from overwriting an intervening user or system transition. The acceptance state and any resulting Sent transition and activity are recorded atomically.

Technicians may request or retry delivery only for documents belonging to Jobs currently assigned to them. Dispatchers and administrators are unrestricted. Customer-only documents remain controlled by office roles.

## Considered Options

- Render the current document and message again for every attempt. Rejected because retries could send content different from the originally requested communication and would weaken auditability.
- Store the message snapshot but regenerate the PDF. Rejected because the attachment is part of the communication and must remain identical across attempts.
- Send inline with the initiating request and omit a durable outbox. Rejected because process or transport failures could lose requests, leave status ambiguous, or couple user response time to an external service.
- Mark a message Delivered when an SMTP server accepts it. Rejected because SMTP acceptance proves handoff only; it does not prove delivery to the recipient's mail system.
- Require a provider with delivery webhooks. Rejected because generic SMTP remains useful, provided its evidence is represented honestly and stops at Accepted.
- Retry indefinitely or retry all failures. Rejected because permanent failures would create unbounded traffic and repeated unwanted messages.
- Create a new logical delivery and message identifier for every retry. Rejected because retries would lose continuity and make provider evidence and duplicate analysis harder to correlate.
- Enable open tracking by default or retain detailed open events. Rejected because tracking is privacy-sensitive, unreliable as proof of reading, and unnecessary for the delivery lifecycle.
- Set Sent whenever acceptance arrives. Rejected because asynchronous evidence must not overwrite a status changed after the delivery request.
- Promise exactly-once recipient delivery. Rejected because no internal transaction can atomically include an external mail system, and retrying an ambiguous handoff can duplicate delivery.

## Consequences

- The outbox consumes additional durable storage, particularly for PDF snapshots, but preserves exactly what was requested and sent.
- Delivery workers may retry safely from the same immutable inputs, while recipients can still receive duplicates after ambiguous external outcomes.
- Provider capabilities determine whether a delivery can progress beyond Accepted.
- Manual retry is an explicit operational action and remains subject to authorization and audit.
- Stable message identifiers improve correlation and recipient-side deduplication but cannot guarantee it.
- Sent reflects transport acceptance, not delivery or reading, and only advances from the unchanged expected underlying status.
- Delivery state, the conditional Sent transition, and activity cannot partially succeed internally.
- Assigned-Job authorization is evaluated when a technician requests or manually retries delivery.
