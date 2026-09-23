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
hash used to claim the account, while the stateless API verifies the JWT before
calling Temporal.

The public display name receives a stable eight-character discriminator derived
from that unique username, such as `Michael#8f3a1c2d`. The tagged value is stored
on first registration and used by the leaderboard.

## State ownership

| Component | Owns | Lifetime |
| --- | --- | --- |
| `CatalogWorkflow` | Immutable campaign snapshots used to render campaign cards and authorize level starts | Singleton entity; Continue-As-New keeps history bounded |
| `DailyWordflowChallengeWorkflow` | Generates dated daily challenges through OpenAI tool calls and starts their campaigns | Singleton entity; retains ten days of puzzles and Continue-As-New keeps history bounded |
| `WordflowCampaignWorkflow` | One campaign's levels, unlock schedule, prerequisites, availability dates, and expiration timer | Permanent campaigns stay open; time-bounded campaigns complete at expiration |
| `PlayerWorkflow` | Password hash, display name, campaign progress, points, rewards, streaks, and the active level reference | Long-lived entity; Continue-As-New keeps history bounded |
| `WordflowLevelWorkflow` | One player's level answers, guesses, revealed cells, hints, timing, scoring, and completion | Runs until the level is solved or its hard limit expires; can Continue-As-New after heavy use |
| `LeaderboardWorkflow` | The top 100 absolute lifetime-point snapshots | Singleton entity; Continue-As-New keeps history bounded |
| HTTP API | Nothing durable; verifies signed session JWTs and translates HTTP requests into Updates and Queries | Stateless |
| Browser | The HTTP-only session JWT, transient letter ordering, and letters currently selected on screen | Local session and presentation state only |

All mutations use Workflow Updates because the UI needs an accepted result.
Queries rebuild screens without changing state. HTTP request IDs are passed as
Temporal Update IDs, so Temporal deduplicates retries without a request ledger
in Workflow state.

A campaign registers its immutable definition and level snapshot with
`catalog/global` through a short Activity that performs Update-with-Start. The
Catalog derives every player-specific campaign card in one Query, so the API
does not fan out to each Campaign Workflow. A campaign with an end time removes
its snapshot and completes when that time arrives.

`DailyWordflowChallengeWorkflow` asks OpenAI to finish each generation turn by
calling one strict `create_daily_wordflow_challenge` tool. The tool supplies only
the campaign description and creative level content. The Activity validates the
letter multisets, answer uniqueness, full-queue words, and connected crossword
layouts, then applies the fixed daily rules in code. The singleton retains the
last ten days of puzzles and rejects any generated answer used during that
window. It starts each dated campaign as an abandoned child, then waits on a
durable timer for the next Toronto midnight.

To start a level, the Player runs a short Activity that asks the Catalog to
resolve the registered campaign snapshot against its compact progress. The
Catalog owns eligibility and unlock decisions and returns an immutable Wordflow
level definition. The Player starts `WordflowLevelWorkflow` as a child, returns
the initial game view in the same Update result, and keeps the child Future in
the main event loop. The child returns only a small game-independent level
result. The Player does not Continue-As-New while that child is open.

Registration sends an authentication Update through Update-with-Start to
`player/{username}`. The first accepted Update claims that username; later calls
must supply the same password hash and return the current `PlayerView`. Login
uses the same Update against an existing Workflow without Update-with-Start, so
an unknown username returns `account does not exist` and cannot reserve a name.
After that credential check, the API issues a 30-day HMAC-signed JWT. Normal
requests validate it entirely in the API and query player state only when the
endpoint actually needs that state. Session creation, validation, and logout
remain HTTP API concerns; the Player Workflow stores no session tokens.

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

The repository also includes a small manual campaign starter. It parses the
same compact level format as the permanent campaign, builds the crossword
layouts, and starts the durable campaign entity:

```bash
go run ./cmd/campaign -file campaigns/daily-challenge-2026-09-21.json
```

To start the daily generator entity, set `OPENAI_API_KEY` on the worker and run
the starter with the first campaign date. The Workflow keeps generating future
dates automatically. `OPENAI_MODEL` is optional and defaults to
`gpt-5.4-mini`.

```bash
OPENAI_API_KEY=... make worker
go run ./cmd/daily-challenge -date 2026-09-22
```

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
- A daily challenge is a dated, single-attempt campaign with three immediately
  unlocked but sequential levels. Its levels can configure a hard time limit,
  higher base score, and higher paid-hint prices. Timing out clears the active
  game and permanently closes that player's attempt for the dated campaign.
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
- Campaign levels may override the 10-point base score and the default hint
  prices without changing the shared level Workflow.
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
Catalog, Daily Challenge, Player, Level, and Leaderboard Workflows Continue-As-New when Temporal
recommends it or when a new target Worker Deployment Version is available. A
level can Continue-As-New while it is active. The player does not Continue-As-New
while it has an active level child; it waits for the child result while remaining
responsive to Updates. Before continuing, each workflow waits for its Update
handlers to finish. Campaign definitions are immutable after start. Permanent
Campaign Workflows stay open for inspection; time-bounded campaigns unregister
and complete when their expiration timer fires.

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

### Read-only Workflow Explorer

The web app includes an application-aware Workflow Explorer at
<http://localhost:8080/workflows/>.
It reads Visibility, Describe, and Event History data without issuing Workflow
Queries, Updates, or Signals. Anonymous visitors can inspect sanitized catalog,
campaign, daily-generator, and leaderboard executions. A browser carrying a
valid Wordflow session cookie can also inspect that player's Player and Level
Workflows. Event details are decoded and sanitized at the API boundary;
credentials, puzzle answers, headers, identities, failure messages, memos, and
search attributes are never sent to the browser.

The explorer is served by the same web/API process and uses the same session
cookie, but neither the page nor its read-only endpoints execute or modify
Workflow state.

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
worker, including the daily-generation, point-spend, and leaderboard-publishing Activities, while enabling the pinned
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
- `OPENAI_API_KEY`, used only by the daily-challenge generation Activity
- `OPENAI_MODEL`, optional; defaults to `gpt-5.4-mini`

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

[`deploy/terraform`](deploy/terraform) creates an isolated, disposable test
stack in the AWS account and Region selected by the active AWS CLI
configuration and the Temporal Cloud Account selected by the provider:

- a fresh Temporal Cloud Namespace, Namespace-scoped service account, and
  runtime API key;
- an immutable, published Worker Lambda version;
- one provisioned Worker Lambda environment to reduce first-use startup delay;
- a Temporal Cloud Worker Deployment Version whose Build ID combines the Git
  SHA with a fingerprint of the exact application source;
- a stateless web/API Lambda serving the embedded UI and API;
- one provisioned API Lambda environment with additional CPU for password
  hashing and initialization;
- an API Gateway HTTP API using its AWS-provided URL;
- separate Lambda execution and Temporal Cloud invocation roles;
- CloudWatch log groups; and
- a Secrets Manager secret containing the Temporal Cloud API key.

The defaults place both Lambda and the Temporal Cloud Namespace in AWS
`ca-central-1`, which is enabled for the demo account. Keep `aws_region` and
`temporal_region` colocated when overriding either one; every game interaction
includes at least one Temporal round trip. Set either provisioned-concurrency
variable to zero when complete scale-to-zero is more important than first-use
latency.

Terraform also generates a stable JWT signing secret and supplies it only to
the API Lambda.

The provider creates the runtime key and writes it to Secrets Manager. The
Lambdas receive only the secret ID and resolve the credential at cold start.
The runtime identity is restricted to the generated Namespace.

Set a short-lived account API key for the Temporal Cloud provider, then run:

```bash
export TEMPORAL_CLOUD_API_KEY='your Temporal Cloud account API key'
terraform -chdir=deploy/terraform init
terraform -chdir=deploy/terraform apply
```

The bootstrap key must be allowed to create Namespaces, service accounts, and
API keys in the configured Temporal Cloud Account. Terraform creates the
application credential separately. The default Account ID is `a2dd6`; override
`temporal_account_id` in a `.tfvars` file for another Account.

Re-running `terraform apply` after a source change builds and publishes a new
Worker Lambda version, registers the matching source fingerprint as a Temporal
Worker Deployment Version, confirms that it bound the Task Queue, and then
makes it current. The apply also starts the permanent and temporary seed
campaigns and waits for their Catalog registrations, so the returned URL is
ready to test. Older Lambda and Worker Deployment Versions remain available for
Workflows pinned to their original code. Run `terraform destroy` to remove the
entire generated Namespace and AWS stack.

The web/API Lambda owns no application state. It keeps only its JWT signing
secret and a reusable Temporal client during a warm invocation; all durable
accounts, campaigns, levels, scores, points, and leaderboard data remain in
Temporal Cloud.

Workflow links in the web app open the read-only, application-aware explorer at
`/workflows/`. This keeps private Player and Level executions behind the same
Wordflow session boundary instead of linking visitors to Temporal Cloud.

## Identity model

Signup is local to the application: the unique `player/{username}` Workflow ID
is the username claim, while the Player Workflow owns the password hash and the
API signs and verifies session JWTs. Raw passwords never enter Workflow history. This deliberately small
authentication model is suitable for the demo; its deterministic password hash
is still vulnerable to offline guessing if Workflow data is exposed, and it
does not attempt email verification, account recovery, MFA, or distributed
request rate limiting.
