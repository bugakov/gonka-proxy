# Issue tracker: GitHub

Issues for this repo live in GitHub Issues at https://github.com/bugakov/gonka-proxy.

## Conventions

- Publish one issue per tracer-bullet ticket.
- Apply `ready-for-agent` to fully specified implementation tickets.
- Record dependency edges in the issue body under **Blocked by**; use native GitHub relationships when available.
- Keep credentials, local configs, and deployment secrets out of issues.

## When a skill says "publish to the issue tracker"

Create the issue with the GitHub CLI, in dependency order, so later issues can reference earlier issue numbers.

## When a skill says "fetch the relevant ticket"

Read the issue body and comments with the GitHub CLI or GitHub web interface.
