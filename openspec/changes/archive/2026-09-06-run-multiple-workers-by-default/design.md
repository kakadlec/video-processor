## Context

`docker-compose.yml` is this repository's only documented Docker workflow for local development, and `docker compose up --build` is the single command the README's quickstart puts in front of a first-time reader — including the hackathon evaluator. That stack currently starts one `app`, one `worker`, one `notifier`, and the four backing services.

One worker is not a limit of the design. `videojob-worker` fixes prefetch at 1 deliberately, so a worker holds exactly one delivery at a time and throughput scales by process count; `videojob-execution`'s `ClaimForProcessing` is an atomic conditional `UPDATE` gated on `status = 'queued'`, so replicas competing for one queue cannot both run one job; `videojob-lease-recovery` makes concurrent sweepers safe by the same conditional-write argument. Three workers is a supported configuration today and was verified as one in the previous change (`--scale worker=3`, three relays and three consumers connected).

What is missing is only the default. RF1 in `docs/project-requirements.pdf` asks the system to process more than one video at a time, and the stack a reader gets without reading further processes them one at a time.

## Goals / Non-Goals

**Goals:**

- `docker compose up` — no flags beyond `--build` — starts a stack that processes several videos concurrently, so RF1 is demonstrated by the documented command rather than by a flag the reader has to know about.
- Keep `--scale worker=N` working as the per-run override, in both directions, so a developer who wants one worker (to read a single log stream, or to reproduce a serial trace) still has a one-flag way to get it.
- Change configuration only. No Go code, no test, no new environment variable, no change to what a worker *is*.

**Non-Goals:**

- Replicating `app`. It publishes a fixed host port, and a second replica cannot bind it.
- Replicating `notifier`. RF1 is video-processing throughput; delivery concurrency is a different question with its own claim protocol, and one notifier is not the bottleneck being demonstrated.
- Any autoscaling, resource limit, or orchestration behaviour. `deploy.replicas` is used here for its Compose meaning (how many containers to start), not as a step toward Swarm or Kubernetes.
- Changing the worker's own concurrency. Prefetch stays 1; the concurrency knob stays process count.

## Decisions

### 1. `deploy.replicas: 3` on the `worker` service, rather than three named services

Three copy-pasted services (`worker`, `worker2`, `worker3`) would achieve the same containers and would triplicate the service's entire environment block, which is the part most likely to drift — three MinIO credential sets and three DSNs that must stay byte-identical, with nothing enforcing it. `deploy.replicas` states the count once against one definition.

The obvious objection to `deploy` is that it is documented as the Swarm-mode section and much of it (`resources`, `restart_policy`, `placement`) is ignored by `docker compose up`. `replicas` is the exception, and this was verified rather than assumed: on Compose v5.1.4, a minimal project with `deploy.replicas: 3` and no Swarm involvement starts `-1`, `-2`, and `-3` under a plain `docker compose up -d`. The verification belongs in the implementation task, not only here, because the key's behaviour is the entire mechanism of this change.

This also requires the service to have no `container_name`, which `worker` does not have. Nothing needs to be removed.

### 2. Three, not two

Two would satisfy "more than one" and cost less. Three is chosen because it is the number already written into the README quickstart, `docs/development.md`, and the previous change's verification (`--scale worker=3`), so the default and the documented example agree instead of quietly differing; and because three makes the behaviour being demonstrated legible — a queue of several jobs visibly spreads across workers rather than alternating between two.

The cost is bounded and known: three concurrent `ffmpeg` processes on a developer machine, one per worker, because prefetch is 1. Anyone for whom that is too much has `--scale worker=1`, which is documented in the same breath.

### 3. `--scale` stays the override, and the docs invert rather than get deleted

Verified in both directions on the same Compose version: with `deploy.replicas: 3` present, `--scale worker=1` scales the running project down to one container and `--scale worker=5` up to five. So the flag does not stop being useful — it stops being the way to *turn on* concurrency and becomes the way to pick a different number. The finalization PR rewrites the README's step 2b and `docs/development.md`'s Docker Workflow around that inversion, and the README's "The default stack starts one" sentence becomes false and must go.

### 4. `app` stays at one, said out loud in the file

"Why did `worker` get replicas and `app` not?" is the natural next edit for someone reading this file, and acting on it produces a stack where the second `app` container fails to bind `127.0.0.1:8080` and the reader is left debugging a port conflict rather than using the app. The reason is a property of that service's own definition (a published fixed host port), so it is recorded as a comment where that decision lives, in a file whose existing comments already carry this much rationale per key.

## Risks / Trade-offs

- **`deploy` is read as a Swarm key by a future editor and "cleaned up"** → The comment states plainly that `replicas` is honoured by `docker compose up` outside Swarm and that it is the mechanism this stack's default concurrency depends on. The spec delta pins the observable behaviour (more than one worker on the documented command), so removing the key breaks a stated requirement rather than a silent convention.
- **Three concurrent `ffmpeg` runs make a modest dev machine slower under load** → Bounded at three by prefetch 1, and `--scale worker=1` is documented as the way down. The stack is idle unless jobs are actually queued.
- **More log interleaving in the foreground `docker compose up` view** → Compose prefixes each line with `worker-1/2/3`, and this is the same output the previously documented `--scale worker=3` already produced. A single stream remains one flag away.
- **A reviewer reads three workers as "the system needs three to be correct"** → It does not, and the docs say so: concurrency is worker count, correctness comes from the conditional claim and the epoch fence, and one worker remains a valid configuration.
