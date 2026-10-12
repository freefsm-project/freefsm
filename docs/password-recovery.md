# Password policy and account recovery

## Policy and assistance

The protected Administrator manages password policy at **Settings → Advanced →
Security** (`/settings/advanced`). The default is 8, with uppercase, lowercase,
digit and special requirements enabled. The editable minimum is **6–72**.
Lengths use JavaScript/HTML UTF-16 units, not UTF-8 bytes or visual graphemes.
Classes are ASCII `A–Z`, `a–z`, `0–9` and any character outside those ranges.
For example `Aa1!éé` has length 6, while `Aa1!😀😀` has length 8. bcrypt also
limits every newly set password to **72 UTF-8 bytes**; no truncation is performed.

Policy changes are prospective: existing hashes, identities and sessions are
unchanged. A protected Administrator can separately select **Require password
change** on a user's page. A manually assigned/reset password is temporary;
normal browser and mobile access is unavailable until browser replacement.
Email recovery and Administrator reset revoke all browser/mobile sessions.
Self-change retains only the browser session making the change. All successful
credential changes invalidate outstanding v1 recovery links and queued requests
from the previous credential revision. Disabled/pending accounts cannot recover;
pending-account onboarding remains the separate invitation workflow.

## Public recovery and durable email requests

`/forgot-password` accepts requests without login, with CSRF and rate limiting.
Known global email disablement/missing configuration is reported before any
account lookup; contact an Administrator for a temporary password in that case.
With email available, all addresses enter the same durable request queue and get
the same acknowledgement. This means **requested**, not accepted by SMTP or
delivered to an inbox. Unknown, disabled and pending addresses send no mail.
Provider latency/failure never affects account-specific HTTP completion.

The application-managed recovery worker runs with the instance maintenance/email
gate. Claims expire after two minutes and resume after a crash. No plaintext
token is stored in this queue. An interrupted attempt may send another message;
only the newest link can work. Unexpected SMTP failure is logged without address,
message or token and completes that request; submit another request or ask an
Administrator. Completed queue rows clear the address and URL.

Links use their own purpose, expire exactly 3,600 seconds after issuance, and are
single-use. They are bound to the current email identity and credential revision.
Changing the destination email invalidates old-mailbox links, including messages
already in flight and queued intent. Invitation tokens are never recovery proof.
Recovery pages use `Cache-Control: no-store` and `Referrer-Policy: no-referrer`,
including validation/error responses, so subsequent browser requests do not
disclose the bearer URL through their Referer header. Request logs omit queries.
Validation, credential update, revocation and immutable audit commit together;
storage/audit failure rolls back the usable credential state.

## Upgrade review (host-local, not public directory data)

Migration `070_recovery_purpose` leaves all existing hashes, user identifiers and
recorded timestamps untouched. Previously recorded recovery links have unknown
purpose/validity and are quarantined as `legacy-unknown`, never grandfathered into
valid one-hour links. Unknown rows remain retained through password reset,
disablement and host recovery. They are excluded from every validation query.
No historical credential-change audit is fabricated.

After upgrading, a host database operator should run these **token-free** reports
and arrange review with an existing protected Administrator:

```sql
SELECT id, user_id, company_id, created_at, expires_at, review_reason
FROM password_recovery_review ORDER BY id;

SELECT company_id, password_min_length, review_reason
FROM password_policy_review ORDER BY company_id;
```

For an unknown link, verify the current account/email with the Administrator and
request a fresh link through `/forgot-password` or the sealed **Resend recovery**
action. Do not change the legacy purpose, expiry, recipient or revision to make
an old token valid. The report exposes no token/hash. Historical unknown rows are
kept for explicit operator retention review rather than silently deleted by
credential operations. The operational directory does not expose this report.

An older minimum above 72 is retained but makes new password setting impossible.
The security editor displays an explicit review notice; an Administrator must
choose 6–72 and save. Existing login remains valid. If locked out with an
impossible policy, the host operator must first coordinate an explicit database
policy repair with an Administrator; recovery does not silently relax policy.

## Host-local Administrator recovery

See [the deployment runbook](../deploy/README.md) for the installed executable's
`-recover-admin` command, configuration and restricted state-directory handling.
It requires host execution and recovers only an **existing, enabled, initialized
protected Administrator identity**, never a role label. It enforces the same
policy, sets a temporary password, revokes sessions and current recovery links,
and commits host-attributed immutable audit atomically. It does not enable,
create or promote a user, create an operator product Role, or reopen `/setup`.
The operator is not granted product access by running the command. A separately
tracked native-service/reboot acceptance matrix is not proved by unit/HTTP tests.
