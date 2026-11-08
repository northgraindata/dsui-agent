# Security policy

## Supported versions

Security fixes are applied to the latest released minor version while dsui-agent is pre-1.0.

## Reporting a vulnerability

Do not open a public issue.
Use GitHub private vulnerability reporting for `northgraindata/dsui-agent`.
Include the affected version, impact, reproduction steps, and any suggested mitigation.
Northgrain Data acknowledges a complete report within five business days and coordinates disclosure after a fix is available.

## Hardening checklist for operators

- Keep the token in a file or secret store, never on the command line.
- Run the agent as a dedicated unprivileged user or a non-root container with a read-only filesystem.
- Keep the allowlist as narrow as possible and prefer exact `host:port` entries over wildcards and ranges.
- Give DSUI database accounts only the rights it needs, ideally read-only.
- Rotate the token when someone who knew it leaves, and revoke agents you no longer use.
