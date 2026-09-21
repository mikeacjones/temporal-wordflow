# Temporal Wordflow

Temporal Wordflow is an unofficial Temporal-themed word-crossword game built to
make entity Workflows easy to study. Temporal owns all durable catalog,
campaign, player, level, and leaderboard state. There is no application
database, cache, or object store.

Players sign up with a username, display name, and password. The normalized
username becomes the human-readable entity ID in `player/{username}` and
`wordflow-level/{username}/{campaign}/{level}`. Before calling Temporal, the API derives a stable
PBKDF2-SHA256 password hash using a username-derived salt. The browser keeps the
API-signed JWT in an HTTP-only cookie. The Player Workflow stores the password
hash, while the stateless API verifies the JWT before calling Temporal.

The public display name receives a stable eight-character discriminator derived
from that unique username, such as `Michael#8f3a1c2d`. The tagged value is stored
on first registration and used by the leaderboard.

## State ownership

| Component | Owns | Lifetime |
| --- | --- | --- |
| `CatalogWorkflow` | Registered games and references to their campaign Workflows | Singleton entity; Continue-As-New keeps history bounded |
| `WordflowCampaignWorkflow` | One campaign's levels, unlock schedule, prerequisites, availability dates, and expiration timer | One long-lived entity per campaign |
| `PlayerWorkflow` | Password hash, display name, campaign progress, points, rewards, streaks, and the active level reference | Long-lived entity; Continue-As-New keeps history bounded |
| `WordflowLevelWorkflow` | One player's level answers, guesses, revealed cells, shuffles, hints, timing, scoring, and completion | Runs until the level is solved; can Continue-As-New after heavy use |
| `LeaderboardWorkflow` | The top 100 absolute lifetime-point snapshots | Singleton entity; Continue-As-New keeps history bounded |
| HTTP API | Nothing durable; verifies signed session JWTs and translates HTTP requests into Updates and Queries | Stateless |
| Browser | Only the HTTP-only session JWT and letters currently selected on screen | Local session convenience |

All mutations use Workflow Updates because the UI needs an accepted result.
Queries rebuild screens without changing state. HTTP request IDs are passed as
Temporal Update IDs, so Temporal deduplicates retries without a request ledger
in Workflow state.

A campaign registers itself with `catalog/global` through a short Activity that
performs Update-with-Start. The Catalog stores only game metadata and campaign
Workflow references. The stateless API queries the Catalog, Player, and
Campaign Workflows to build the campaign browser.

To start a level, the Player runs a short Activity that queries the campaign
with its compact progress snapshot. The campaign owns eligibility and unlock
decisions and returns an immutable Wordflow level definition. The Player starts
`WordflowLevelWorkflow` as a child and keeps its Future in the main event loop.
The child returns only a small game-independent level result. The Player does
not Continue-As-New while that child is open.

Registration sends an authentication Update through Update-with-Start to
`player/{username}`. The first accepted Update claims that username; later calls
must supply the same password hash and return the current `PlayerView`. Login
uses the same Update against an existing Workflow without Update-with-Start, so
an unknown username returns `account does not exist` and cannot reserve a name.
After that credential check, the API issues a 30-day HMAC-signed JWT. Normal
requests validate it entirely in the API and query player state only when the
endpoint actually needs that state.

After a completed level updates Player state, a small Activity uses
Signal-With-Start to publish an absolute score snapshot to `leaderboard/global`.
The leaderboard ignores older player versions, so Activity retries cannot add
points twice. The public `/leaderboard` page queries that Workflow every three
seconds and can run independently on an event display.

Hints belong only to a Wordflow level. When its free inventory reaches zero, that level
sets its own price and runs a `SpendPoints` Activity. The Activity sends an
Update to the Player Workflow because Workflows cannot send Updates directly to
other Workflows. Its Update ID is derived from the level Workflow ID and the
originating level Update ID, making Activity retries safe without custom
deduplication state. The Player Workflow only validates ownership and the
current balance, then atomically deducts the requested amount.

The workflow code is intentionally direct:

- [`internal/workflows/catalog.go`](internal/workflows/catalog.go) contains the
  campaign registry.
- [`internal/workflows/wordflow_campaign.go`](internal/workflows/wordflow_campaign.go)
  contains Wordflow campaign lifecycle and eligibility rules.
- [`internal/workflows/player.go`](internal/workflows/player.go) contains the
  complete player state machine.
- [`internal/workflows/wordflow_level.go`](internal/workflows/wordflow_level.go)
  contains the complete Wordflow level state machine.
- [`internal/workflows/leaderboard.go`](internal/workflows/leaderboard.go)
  contains the bounded ranking projection.
- [`internal/activities/points.go`](internal/activities/points.go) contains the
  one cross-Workflow client call used to spend points.
- [`internal/bootstrap/temporal-foundations.json`](internal/bootstrap/temporal-foundations.json)
  seeds the default campaign. Once started, its Workflow state is authoritative.
- [`internal/game/catalog.go`](internal/game/catalog.go) builds and validates a
  configured crossword layout before it enters Workflow state.

To add another Wordflow campaign, build a `WordflowCampaignWorkflowInput` from
configuration and start it with `wordflow-campaign/{campaign-id}`. It registers
itself with the Catalog, and the existing API and UI discover it through the
common campaign queries. A future game type supplies its own campaign and level
Workflows while reusing the small Catalog registration, campaign view, player
progress, and level-result contracts; the game-specific path remains explicit
instead of hiding behavior behind a generic payload layer.

## Implemented game rules

- The default `Temporal Foundations` campaign never expires.
- Three levels unlock when a player joins that campaign, followed by three more
  every 24 hours. Missed levels remain available.
- Levels are sequential and guesses are unlimited.
- Campaign prerequisites are all-of requirements over completed campaigns and
  completed campaign levels.
- Leaving gameplay returns to the campaign browser without closing the active
  child Workflow. The player can resume it but cannot start another level until
  it completes.
- An ended campaign rejects new level starts; a level started before the end may
  finish.
- Words that can be made from the letter queue but are not in the puzzle remain
  in durable, newest-first game history beside the board.
- Completing at least one level on a Toronto calendar day maintains the daily
  streak. A streak cannot be restored after a missed day.
- Every completed level awards 10 base points, plus up to 10 points each for
  speed, accuracy, and conserving hints. Incorrect guesses reduce the accuracy
  bonus; letter, brush, and whole-word hints reduce the hint bonus by 2, 4,
  and 8 points. The speed bonus drops from 10 to 7 to 4 in one-minute tiers,
  then reaches zero after three minutes. The in-game panel shows the live
  Workflow-derived speed, hint, and accuracy bonuses. A win always awards at
  least 10 points.
- Spendable points can be used for hints and streak freezes. Lifetime points
  track every earned game, milestone, and event point without decreasing when
  points are spent.
- Levels 5, 10, 15, and so on add campaign-configured milestone points; campaign events can add
  bonus points.
- Each game starts with two single-letter hints, one multi-letter brush, and one
  whole-word hint. Additional hints cost 10, 20, and 30 points respectively.
- A player can spend 50 points on one streak freeze. It is automatically
  consumed to protect the streak after exactly one missed Toronto calendar day.
- The included campaign has 30 Temporal-themed levels and two short bonus events.

## Continue-As-New and deployments

Each mutable workflow carries its complete state in one serializable structure.
Catalog, Player, Level, and Leaderboard Workflows Continue-As-New when Temporal
recommends it or when a new target Worker Deployment Version is available. A
level can Continue-As-New while it is active. The player does not Continue-As-New
while it has an active level child; it waits for the child result while remaining
responsive to Updates. Before continuing, each workflow waits for its Update
handlers to finish. Campaign definitions are immutable after start, so campaign
Workflows stay open with only their optional expiration timer and queries.

Lambda workflows are registered as `Pinned`. At a Continue-As-New boundary,
the new run opts into the target Worker Deployment Version when one is
available. This is the cleanest upgrade boundary for permanent entity
workflows, but both Serverless Workers and upgrading pinned workflows at a
Continue-As-New boundary are currently preview features.

## Run locally

Requirements: Go 1.26 or newer and the Temporal CLI.

Use three terminals:

```bash
make temporal
```

```bash
make worker
```

```bash
make server
```

The `server` target supplies a local-only `SESSION_JWT_SECRET`. Set that
environment variable to a private value of at least 32 characters for any
other deployment; every API replica must use the same value.

Then open <http://localhost:8080>. The Temporal UI is at
<http://localhost:8233>. The standalone event leaderboard is at
<http://localhost:8080/leaderboard>.

`make temporal` stores the development Temporal database in `.temporal/`.
That SQLite file belongs to the Temporal development service; the application
still has no separate persistence system.

Run validation with:

```bash
make test
make vet
make workflowcheck
```

## AWS Lambda worker profile

[`cmd/lambda-worker/main.go`](cmd/lambda-worker/main.go) uses Temporal's
`lambdaworker` package. It registers the exact same workflows as the local
worker, including the point-spend and leaderboard-publishing Activities, while enabling the pinned
versioning behavior required by Serverless Workers.

Build an AWS Lambda custom-runtime zip without generating any AWS deployment
configuration:

```bash
make lambda
```

The artifact is `dist/temporal-word-game-worker.zip`. It contains a Linux
`bootstrap` binary built with the required `lambda.norpc` tag. Set these Lambda
environment variables:

- `TEMPORAL_ADDRESS`
- `TEMPORAL_NAMESPACE`
- `TEMPORAL_API_KEY`
- `TEMPORAL_TASK_QUEUE=temporal-word-game`
- `TEMPORAL_WORKER_BUILD_ID`, uniquely identifying this immutable Lambda build

Publish the function as an immutable Lambda version and map that qualified ARN
one-to-one to the same Temporal Worker Deployment Build ID. Temporal's current
setup and IAM steps are documented in the
[Serverless Worker deployment guide](https://docs.temporal.io/production-deployment/worker-deployments/serverless-workers/aws-lambda).

The web API can run anywhere that can reach Temporal. It remains stateless and
does not need to share a process or filesystem with a Worker.

## Kubernetes steady worker

[`docker/worker.Dockerfile`](docker/worker.Dockerfile) builds the long-running
worker for steady Kubernetes traffic. Set `TEMPORAL_WORKER_BUILD_ID` to the
same Build ID as the Lambda worker so both processes poll the same pinned
Worker Deployment Version. Without that variable, the worker remains
unversioned for local development.

## Serverless AWS deployment

[`deploy/terraform`](deploy/terraform) deploys the complete public demo into the
AWS account and Region selected by the active AWS CLI configuration:

- an immutable, published Worker Lambda version;
- a Temporal Cloud Worker Deployment Version whose Build ID is the full Git SHA;
- a stateless web/API Lambda serving the embedded UI and API;
- an API Gateway HTTP API served at `https://games.tmprl-demo.cloud`;
- a DNS-validated ACM certificate and Route 53 alias for that domain;
- separate Lambda execution and Temporal Cloud invocation roles;
- CloudWatch log groups; and
- a Secrets Manager secret containing the Temporal Cloud API key.

Terraform also generates a stable JWT signing secret and supplies it only to
the API Lambda.

The secret value enters Terraform through an ephemeral variable. Terraform
writes it directly to Secrets Manager but never stores it in the plan or state.
The Lambdas receive only the secret ID and resolve the credential at cold start.

The deployment intentionally requires a clean Git worktree. This keeps each
Temporal Worker Build ID, published Lambda version, and source revision in a
one-to-one relationship. Commit changes before each deployment, then run:

```bash
export TF_VAR_temporal_api_key='your Temporal Cloud API key'
terraform -chdir=deploy/terraform init
terraform -chdir=deploy/terraform apply
```

The key must be able to connect to the configured Namespace and manage Worker
Deployments. The checked-in defaults target
`michaelj-durable-games.a2dd6`. Override `temporal_namespace` and
`temporal_address` in a `.tfvars` file for another Namespace; `.tfvars` files
are ignored because they may contain deployment-specific values.

Re-running `terraform apply` after a new commit builds and publishes a new
Worker Lambda version, registers the matching Git SHA as a Temporal Worker
Deployment Version, confirms that it bound the Task Queue, and then makes it
current. Older Lambda and Worker Deployment Versions remain available for
Workflows pinned to their original code. To rotate the Temporal key, supply the
new value and increment `temporal_api_key_revision`.

The web/API Lambda owns no application state. It keeps only its JWT signing
secret and a reusable Temporal client during a warm invocation; all durable
accounts, campaigns, levels, scores, points, and leaderboard data remain in
Temporal Cloud.

Workflow links in the web app default to the local Temporal UI at
`http://localhost:8233`. Set `TEMPORAL_WEB_UI_URL=https://cloud.temporal.io`
when the API uses Temporal Cloud. The URL namespace defaults to
`TEMPORAL_NAMESPACE`; set `TEMPORAL_WEB_UI_NAMESPACE` when the namespace
segment used by Temporal Cloud differs.

## Identity model

Signup is local to the application: the unique `player/{username}` Workflow ID
is the username claim, while the Player Workflow owns the password hash and the
API signs and verifies session JWTs. Raw passwords never enter Workflow history. This deliberately small
authentication model is suitable for the demo; its deterministic password hash
is still vulnerable to offline guessing if Workflow data is exposed, and it
does not attempt email verification, account recovery, MFA, or distributed
request rate limiting.
