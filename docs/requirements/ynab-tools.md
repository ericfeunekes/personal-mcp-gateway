---
title: "YNAB Tool Requirements"
status: accepted
purpose: "Specify broad YNAB API coverage through typed, batch-oriented MCP tools."
covers:
  - docs/ynab.md
---

# YNAB Tool Requirements

## Outcome and Scope

Allow local agents and ChatGPT to read and manage everything exposed by the
documented YNAB API through the same small tool vocabulary. YNAB remains the
financial-record authority. Modeling, rules, triggers, scheduled checks, local
merges, and workflow decisions belong to the external finance domain.

The coverage baseline is the official [OpenAPI specification v1.86.0](https://raw.githubusercontent.com/ynab/ynab-sdk-python/main/open_api_spec.yaml),
inspected on 2026-09-06: 44 method/path operations. This URL follows upstream;
compare the version and operation inventory before implementation. An upstream
addition does not silently change the accepted tool surface.

## Tool Contract

Expose exactly `list`, `get`, `create`, `update`, `delete`, and
`import_transactions` on the `ynab` server. Every tool accepts an object with a
nonempty `items` array. One operation uses one item; there are no separate
single/batch variants. Each item has a literal `type` discriminator and the
fields appropriate to that resource and operation. Mixed resource types are
supported within one call where the verb permits them.

File export is `create` with `type: export`, preserving the six-tool vocabulary.
It creates an artifact on the gateway host; it does not change YNAB records.

Each plan-scoped item identifies its `plan_id`; no mutable current-plan state.
Concrete provider IDs are canonical. YNAB's `last-used` and `default` aliases
may be accepted with their documented semantics, without treating them as stable
storage identities. User retrieval requires no plan. Each import item identifies
a plan and the linked-transaction import operation; it contains no transaction
upload data.

Publish complete input and structured output schemas, including alternatives,
required fields, enums, nullability, units, and field descriptions. Unsupported
verb/resource combinations must not appear as valid alternatives. Generic
untyped payload dictionaries do not satisfy the contract. Apply the gateway's
[client rendering constraints](../gateway.md#model-visible-tool-schemas).

Return one result per input item, correlated by input position and retaining
resource/provider identity where known. Distinguish successful results, provider
errors, operations not attempted, and uncertain write outcomes. Do not label an
entire partially completed batch successful or retry successful items implicitly.
These are call results, not a persisted receipt/proposal system.

Validate the whole call's schema and locally checkable constraints before any
provider mutation. Preserve item order semantically. Use native transaction
batches when compatible; other batches may require several provider calls.
Do not reorder dependent operations or imply all-or-nothing execution across
requests. A dependent create-then-update needing the newly returned ID uses
separate calls. Do not invent temporary IDs or intra-batch references.

Mutation batches stop dispatching later provider requests after a provider error,
rate-limit response, cancellation, or uncertain write outcome. Retain results
already received and mark undispatched items not attempted; do not roll back
successful writes automatically. Execute mutation requests sequentially, allowing
compatible consecutive transaction items to use one native provider batch.
If a native batch response cannot establish individual outcomes, report that
uncertainty for the affected items rather than inferring success or failure.
Read batches may continue after an item-specific error, but stop new requests
on cancellation or rate limiting and preserve any already returned results.
Credential failures and shared outage suppression also stop the read batch.
The accepted recovery extension adds optional typed error codes, HTTP status,
caller recovery action, and retry time to per-item results, with MCP `isError`
for any non-successful batch. Safe GET requests may retry once inside the call
budget; writes are never replayed automatically. Shared per-token request
accounting/cooldowns are adapter reliability state, not a financial workflow or
sync database. Defaults and transitions are owned by
[rate limits and recovery](../ynab.md#rate-limits-and-recovery).

## Complete API Coverage

Paths below are relative to `/plans/{plan_id}` unless stated otherwise.
Alternative paths in a row remain accessible through typed scope arguments.

| Resource | Read mapping | Write mapping | Operations |
| --- | --- | --- | ---: |
| User | `get`: `GET /user` (API root) | None | 1 |
| Plans/settings | `list`/plan metadata: `GET /plans` (API root), including optional accounts; settings: `GET /settings`; full-plan export source: `GET /plans/{plan_id}` | `create(type: export)` writes the fetched full plan to a local artifact, not back to YNAB | 3 |
| Accounts | `list`: `GET /accounts`; `get`: `GET /accounts/{account_id}` | `create`: `POST /accounts` | 3 |
| Categories/groups/month categories | `list`: `GET /categories` with groups; `get`: `GET /categories/{category_id}`, `GET /months/{month}/categories/{category_id}` | `create`: `POST /categories`, `POST /category_groups`; `update`: `PATCH /categories/{category_id}`, `PATCH /category_groups/{category_group_id}`, `PATCH /months/{month}/categories/{category_id}` | 8 |
| Payees | `list`: `GET /payees`; `get`: `GET /payees/{payee_id}` | `create`: `POST /payees`; `update`: `PATCH /payees/{payee_id}` | 4 |
| Payee locations | `list`: `GET /payee_locations`, `GET /payees/{payee_id}/payee_locations`; `get`: `GET /payee_locations/{payee_location_id}` | None | 3 |
| Months | `list`: `GET /months`; `get`: `GET /months/{month}` | None | 2 |
| Money movements/groups | `list`: `GET /money_movements`, `GET /months/{month}/money_movements`, `GET /money_movement_groups`, `GET /months/{month}/money_movement_groups` | None | 4 |
| Transactions | `list`: `GET /transactions` and `GET /accounts/{account_id}/transactions`, `/categories/{category_id}/transactions`, `/payees/{payee_id}/transactions`, `/months/{month}/transactions`; `get`: `GET /transactions/{transaction_id}` | `create`: `POST /transactions` (single/native batch); `update`: `PUT /transactions/{transaction_id}`, `PATCH /transactions` (native batch); `delete`: `DELETE /transactions/{transaction_id}`; `import_transactions`: `POST /transactions/import` | 11 |
| Scheduled transactions | `list`: `GET /scheduled_transactions`; `get`: `GET /scheduled_transactions/{scheduled_transaction_id}` | `create`: `POST /scheduled_transactions`; `update`: `PUT /scheduled_transactions/{scheduled_transaction_id}`; `delete`: `DELETE /scheduled_transactions/{scheduled_transaction_id}` | 5 |

Coverage concerns observable provider capabilities, not forcing an upstream
single-write request when its native batch form preserves the same semantics.
Category groups are retrieved through category listings; there is no standalone
group GET endpoint. Targets are category fields, not a separate subsystem.

## Fields and Provider Semantics

- Account creation accepts name, starting balance, and the provider-supported
  types: checking, savings, cash, credit card, other asset, other liability.
  Preserve the broader account types returned on reads, including loan accounts.
- Category creation includes name and group. Updates support name, note, group,
  target amount/date, set-aside versus refill, and weekly/monthly/yearly target
  frequency. Preserve provider restrictions on frequency/date combinations and
  credit-card payment categories. Explicit null target amount removes a target.
- Category-group and payee management includes creation and renaming.
- Monthly category updates set the absolute assigned (`budgeted`) amount.
  Calculating an increment or moving allocations between categories is external
  composition; multiple assignment writes are not an atomic money movement.
- Transaction writes expose provider-supported account, date, amount, payee,
  category, memo, cleared state, approval, flag color, and split fields.
  Preserve `uncleared`, `cleared`, and `reconciled` as distinct states.
- Native batch transaction updates accept ID or import ID, never both for one
  item. Import ID is lookup-only on update. Creation preserves optional import
  IDs, provider duplicate detection, matching behavior, and duplicate results.
  Do not fabricate a provider idempotency guarantee for other writes.
- Transfers use the destination account's transfer payee. They record ledger
  transfers; they do not move funds at a bank.
- Preserve split parent/child identities and provider restrictions. Existing
  split dates, amounts, category changes, and subtransaction editing are not
  unrestricted. Reject unsupported changes rather than presenting ignored fields
  as applied. Creating scheduled splits is unsupported.
- Scheduled writes expose account, future date, amount, payee, category, memo,
  flag, and the complete provider recurrence enum. Preserve required fields and
  the five-year future-date bound. Ordinary transaction creation rejects future
  dates; do not silently convert it into a scheduled operation.
- Preserve integer milliunits, sign, plan currency/date settings, null versus
  omission, transfer links, and deleted markers. Describe integer constraints
  explicitly because Codex renders integers as TypeScript `number`.
- Include returned targets, monthly activity/available amounts, account balances,
  import health, reconciliation timestamp, and money-movement relationships.
  Read-only provider fields must not become writable merely because they exist
  in result objects.

## Retrieval and Local Merging

Require both `since_date` and `until_date` on every transaction-list item,
including account/category/payee/month-scoped items and incremental reads.
Use inclusive ISO dates and reject missing bounds or a start after the end.
Month-scoped requests must keep both bounds within that month. These fields
are required in the model-visible schema, not merely recommended in prose.
This requirement applies to transaction collections, not account/category
listings, single-record gets, or scheduled transactions.

Expose native date bounds, uncategorized/unapproved filters, and typed
plan/account/category/payee/month transaction scopes. Preserve the distinction
between those provider routes; do not imply arbitrary combinations are native.
General text search and financial aggregation are outside this baseline.

Transaction collection reads exclude pending bank transactions. The provider
defaults most transaction routes to one year of history when `since_date` is
omitted; the MCP's required bounds eliminate that implicit default. Return the
effective range. Month-relative `current` uses YNAB's UTC convention.

Preserve `last_knowledge_of_server`, returned `server_knowledge`, and deletions
where supported. Callers own storing change tokens and merging by provider IDs;
the server stores no synchronization checkpoints. Do not add delta arguments to
routes that lack them in the pinned coverage baseline.

Ordinary transaction reads return the requested interval in one bounded result.
If it does not fit, return an explicit size error asking the caller to narrow
the dates or scope; do not silently truncate, automatically fetch all history,
or repeatedly download the collection to manufacture pages. A multi-year
transaction request is still valid when it explicitly supplies both dates.

Full-plan export is an explicit artifact operation described below. `get` for a
plan supplies plan metadata; it must not inject the complete export into model
context. The `items` array continues to mean batching, not full history. Never
return a usable final change checkpoint for content that was not delivered or
written completely.

## File Exports

An export writes complete content to a configured private export directory on
the gateway host and returns small artifact metadata. It does not return raw
JSON, CSV rows, or base64 bytes in ordinary tool content or structuredContent.
Both local Codex and ChatGPT can request the same server-side export operation.

Each `create(type: export)` item specifies `plan_id`, a typed source, a format,
and a destination filename relative to the configured export directory:

- Source `plan` fetches the full available plan through `GET /plans/{plan_id}`
  once and writes JSON, preserving all returned records and relationships.
- Source `transactions` uses explicit required `since_date` and `until_date`,
  and the existing typed account/category/payee/month scope. JSON preserves the
  complete response; CSV presents a transaction table. A multi-year range is
  explicit, with no paging loop.

V1 formats are JSON and CSV. Full-plan CSV is not one flat table and is not
advertised as a lossless export. Spreadsheet workbooks, PDF reports, and richer
analysis are produced externally from the saved data. ZIP packaging was tested
as an optional future bundle, not added to the initial tool contract.

JSON preserves provider IDs, nullable values, milliunits, deleted markers,
relationships, and change knowledge. CSV preserves stable row IDs, raw integer
milliunits, dates, and quoted text; retain nested split details as JSON in a
dedicated cell so flattening does not duplicate the parent amount. Treat
spreadsheet formula-like text as literal text and document any CSV escaping;
the JSON export remains the exact representation when null/empty distinctions
or unescaped source text matter.

Use a server-configured root, relative filenames, confinement, and exclusive
creation. Reject traversal, symlinks, conflicting names, and mismatched format
extensions. Write to a private temporary file and publish only after completion;
remove incomplete task-owned files on failure/cancellation. No arbitrary host
path or caller-provided upload URL is accepted. Completed exports belong to the
external finance workflow; no background cleanup or synchronization service is
introduced. Runtime logs omit filenames, paths, and file contents.

Return filename, format/MIME type, byte size, source scope, and a host-local
artifact location; include record counts/change knowledge when applicable and
known. A host-local location is not a ChatGPT download URL. Saving on the Mac,
delivering a user download, and attaching a file for model analysis are separate
boundaries. Local agents can use the saved file directly. Phone transfer or
viewing uses Eric's existing presentation/transfer workflow when requested.

Native ChatGPT JSON/CSV attachment or download remains unverified and deferred;
the existing Obsidian proof covers PDF only. Do not expose a public file server,
assume a private Mac/Tailscale path is reachable by ChatGPT, or fall back to
dumping the export into model context. A future native delivery adapter must
prove that client boundary independently. Server-side export remains usable
without that adapter.

## Bank Import and Account Upkeep

`import_transactions` requests available imports for all linked accounts in each
specified plan, returning imported transaction IDs. It accepts neither a bank
account selector nor an arbitrary file upload. Creating imported records from
external evidence uses `create` with transaction items instead.

Account reads expose `direct_import_linked`, `direct_import_in_error`, and
`last_reconciled_at` as supplied, including unavailable/null values. Connection
health is not proof of fresh bank data; last reconciliation is not last import.
Import does not guarantee a new upstream bank fetch. Scheduling these calls,
detecting stale accounts, and notifying Eric belong to the local finance domain.

The documented API lacks account rename/close/reopen/delete, bank link/reconnect,
account-level reconciliation completion, category/group hide/reorder/delete,
payee merge/delete/rule management, and plan creation/settings mutation.
Changing transaction status to reconciled does not claim completion of YNAB's
account reconciliation flow. Report these application-only boundaries clearly.

## Access and Execution

Use private local credentials for Eric's account and explicit provider requests;
never expose tokens or general HTTP/shell execution through tools. Read and
write tools have truthful impact annotations; mutation tools must not be marked
read-only to ease client approvals. Normal client authorization applies, without
introducing a mandatory persisted proposal or plan-hash workflow.

Keep metadata-only telemetry: operation/resource kind, bounded counts, duration,
and outcome. Exclude financial record contents, account/payee names, amounts,
memos, credentials, and raw request/response bodies from routine logs.

Bound calls, batches, provider concurrency, and responses; honor cancellation
and provider rate limits. Preserve uncertain outcomes after interrupted writes.
Never blindly retry a potentially applied non-idempotent write. Numeric budgets,
credential/configuration wiring, and provider library reuse are implementation
decisions that must be recorded before code depends on them.

## Acceptance and Proof

1. Account for all 44 operations and every supported input field in the schemas
   and provider mapping, including meaningful read-only/application-only gaps.
2. Prove array-only calls with one item and multiple mixed types. Verify schema
   rejection, null/omission, enums, units, IDs, dates, and unsupported operations.
3. Use realistic HTTP boundary tests for provider methods, paths, query/body
   semantics, native batches, per-item results, duplicates, partial failures,
   timeouts, cancellation, rate limiting, and uncertain writes. Include split,
   transfer, target, scheduled, import, full-history, and deletion cases.
4. Prove complete bounded retrieval and correct change-token handling across
   interrupted or continued results, without creating a synchronization store.
5. Exercise the actual Go MCP server over stdio and the chosen gateway transport;
   verify schemas, annotations, safe logs, and advertised resource coverage.
   Run the repository's required implementation checks, including `make test`.
6. Inspect full input/output declarations in Codex for lost alternatives or
   `unknown`, and exercise representative calls against harmless fixtures. The
   existing small union probe supports the design but is not full-schema proof.
7. ChatGPT model testing is deferred by Eric's explicit 2026-09-06 decision and
   is not a current gate. Retain ChatGPT-compatible transport/schema intent and
   accurately report that live ChatGPT behavior is unverified. Live financial
   mutations require separately authorized test targets.

Requirements are complete at capability level. Exact public JSON schemas,
continuation/batch limits, launch configuration, and the reuse boundary with the
local CLI remain implementation-planning outputs, not invented defaults.
