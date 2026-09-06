## Why

The hackathon brief's first functional requirement (RF1, `docs/project-requirements.pdf`) is to process more than one video at a time. Everything that makes that possible already ships: a durable dispatch queue, prefetch 1 per worker, an atomic conditional claim (`UPDATE video_jobs SET status='processing' WHERE id=$1 AND status='queued'`), epoch fencing, and a recovery sweeper that is safe across replicas. What does not ship is a *demonstration* of it — `docker-compose.yml`'s `worker` service has no replica count, so the documented single command, `docker compose up --build`, starts exactly one worker and the stack processes videos strictly serially.

The gap is therefore between the architecture and the default a reviewer actually runs. `docker compose up --build --scale worker=3` closes it and is documented (README quickstart step 2b, `docs/development.md`'s Docker Workflow), but it is an opt-in flag: someone who follows the quickstart's main path sees one worker, one `ffmpeg` at a time, and no evidence for RF1. Concurrency being worker count is a design property of this system, and the default stack should exhibit it rather than require a flag to reveal it.

## What Changes

- `docker-compose.yml`'s `worker` service gains `deploy.replicas: 3`, so `docker compose up` (with or without `--build`, with no flags added) starts three worker containers competing for one queue. Verified empirically against Compose v5.1.4 rather than assumed: despite `deploy` being a Swarm-mode key, `docker compose up` honours `replicas` for a non-Swarm project, and `--scale worker=N` still overrides it in **both** directions (`--scale worker=1` scales down to one, `--scale worker=5` up to five). The existing documented flag therefore keeps working unchanged and becomes the way to *choose* a count, including choosing one.
- A comment on the new key, in the style the rest of this file uses: why worker count is the concurrency knob at all (prefetch 1), why three workers do not collide on one job (the conditional claim, not the Redis lease), and why `deploy` here is not a Swarm deployment descriptor.
- No application code, no test, and no other service changes. In particular the `app` service is deliberately left at one: it publishes `127.0.0.1:8080:8080`, so a second replica would fail to bind the host port and half the stack would come up broken. `notifier` is left at one because RF1 is about video processing throughput, and a single notifier is not the bottleneck for it.

## Capabilities

### New Capabilities
(none)

### Modified Capabilities
- `development-workflow`: modifies the existing "Local Full-Stack Development Service" requirement, which today says only that one documented command starts the application together with its dependencies. It gains the guarantee that the stack that command starts runs **more than one** video-processing worker, so concurrent processing is what the default demonstrates, and that the replica count stays overridable per run. This must be a `MODIFIED Requirements` delta rather than an `ADDED` one: the claim being strengthened is about what that same single command starts, and a second requirement standing beside it would describe two different stacks.

## Impact

- **Changed configuration**: `docker-compose.yml`, `worker` service only — one `deploy.replicas` key plus its comment. No environment variable is added, removed, or changed; the three workers share the identical configuration one worker has today.
- **No code, no tests**: the diff carries no Go module input, so `development-workflow`'s "A non-build change is exempt from the local test-run requirement" applies. Verification is behavioural instead — `docker compose up -d` starting `worker-1/2/3`, each logging its own connected relay and consumer, and `--scale worker=1` still collapsing the stack to one.
- **Runtime cost on a developer machine**: up to three concurrent `ffmpeg` extractions instead of one. That is the point of the change, and it is bounded by prefetch 1 per worker (three jobs in flight, never more).
- **Docs** (finalization PR only): README's quickstart step 2b and its "Current Limitations" worker bullet both state the opposite of what will then be true ("The default stack starts one") and invert — the flag stops being how you scale up and becomes how you pick a different number, `--scale worker=1` included; `docs/development.md`'s Docker Workflow block for the same reason; `CLAUDE.md`'s statement that scaling out means running more worker processes, which stays true but no longer describes the default; `docs/roadmap.md`'s Change Backlog row for this change under "Local development tooling", flipped to `archived` (the row itself is added by the finalization PR, not this one — `docs/roadmap.md` is out of scope for a propose PR per `docs/development.md`'s PR Separation Rule).
- **Dependencies**: none new.
