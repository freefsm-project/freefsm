# Invitation and manual onboarding (#21)

Production routes construct the invitation service with the PostgreSQL pool.
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

The shared policy/UI/manual commit-validation and unknown legacy recovery-token
classification are coordinated with #22. Until that work is merged, configured
onboarding form limits and manual creation's transaction-time policy validation
remain integration acceptance items; this document does not claim those passed.
