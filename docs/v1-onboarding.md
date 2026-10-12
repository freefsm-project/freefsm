# Invitation and manual onboarding (#21)

Production routes construct the invitation service with the PostgreSQL pool.
Initial account creation, the first invitation and sealed audit are one
transaction (`CreateInvitedAccount`), returning the bound recipient snapshot.
There is no late initial token command that can supersede an intervening disable,
identity edit or explicit resend.
Acceptance takes the shared account/Role/security lock (190019), rechecks the
invitation and pending account, locks and validates the current password policy,
then commits consumption, the chosen password, activation and immutable own-action
audit together. Validation or audit failure leaves the link usable. Successful
redemption does not require another password change. Links are invitation-table
credentials, never password-recovery credentials; each newly issued link has
exactly 72 hours from its recorded creation time.

An explicit identity, Role, enable/disable, password or forced-change decision on
a pending account invalidates outstanding invitation links in the same database
transaction. The pending account's button is **Invalidate pending invitation**;
it does not enable an account without a chosen password. An Administrator's
explicit **Resend Welcome** makes a new decision and replaces old links.

Invitation creation checks the global email gate and saved transport/sender/key
configuration before creating an account. Known unavailable mail offers manual
onboarding. Unexpected transport failure retains one inactive pending account
and directs the Administrator to resend from that account. A successful handoff
is described as transport acceptance, not inbox delivery. `welcome_email_sent_at`
is now recorded only after that handoff, together with protected audit. Existing
timestamps are preserved. A handoff whose confirmation cannot be recorded is
reported separately; Administrators are told to check before resending.
Loss of the final SMTP DATA acknowledgment is a typed uncertain-acceptance
outcome: the invitation may have arrived, so the Administrator must verify before
an explicit resend. It is never described as definite nonacceptance.

Invitation pages send `Referrer-Policy: no-referrer` and `Cache-Control: no-store`.
Production request logs use a query-redacted snapshot while handlers receive the
original URL. Outcome pages, audit payloads and transport error logs omit raw
invitation secrets and provider diagnostics.

Manual creation retains the existing sealed command's mandatory temporary-
password restriction. Browser and field access remain blocked across Role changes
until the user replaces the temporary credential.

## Upgrade evidence

Migration `070_invitation_decisions.up.sql` preserves uncertain legacy completion
timestamps in `onboarding_migration_review`. Migration 048 sometimes synthesized
completion from account creation or token invalidation, neither of which proves
activation. Inactive welcomes with those timestamps and no recorded login/session/
acceptance evidence become reviewable pending accounts; their old links are
invalidated. The account page explains the review and explicit resend path.
Active accounts, identifiers, passwords and historical welcome timestamps are
preserved. No password-recovery token is converted into an invitation.

## Verification and shared-policy integration

`internal/handlers/invitation_v1_integration_test.go` exercises the production
router against migrated PostgreSQL: lifecycle/expiry/purpose/single-use,
concurrency, account-decision and policy races, atomic audit failure, mail
availability/failure, sealed denial, restricted manual credentials, and upgrade.
`TestOnboardingPersistedBrowser` uses CSRF, real Chromium, real SMTP acceptance,
real password login/replacement, and fresh-router persisted-state verification.
Review-follow-up tests additionally cover initial issuance interrupted by actual
sealed disable/email/resend commands, token/audit insertion rollback, Role/session
loss while create/resend waits, and concurrent resend/redemption. An actual
pre-040 installation is advanced through pre-048 and current migrations with
explicit workflow and authorization review, preserving unknown reset-table
records, consumed/expired invitation facts and completed-disabled evidence.
Production API login rejects temporary credentials without issuing a mobile
session, then accepts the chosen credential after browser replacement. Chromium
checks that resource requests do not carry token-bearing referrers.

The composed #21/#22 implementation uses `passwordpolicy.Load` and `Validate`
inside the invitation and manual-account transactions. All credential forms use
the configured minimum, including validation-error re-renders. The default is
eight characters with uppercase, lowercase, digit and special-character checks
enabled; the shared validator also enforces bcrypt's 72-byte limit without
truncation. Policy changes are prospective for existing credentials.

Composition tests cover configured minima across all six password-setting forms,
exact 72-byte acceptance and 73-byte rejection, a policy change while redemption
waits (without consuming the link), and manual creation's commit-time policy
check. Both SMTP-backed onboarding and recovery browser journeys use production
CSRF and a configured minimum of 16. Legacy recovery tokens are quarantined for
review by the separate recovery migration; they are never promoted to invitation
credentials. See `password-recovery.md` for that review procedure.
