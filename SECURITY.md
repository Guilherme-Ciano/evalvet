# Security policy

## Supported versions

The latest minor release receives fixes.

## Reporting a vulnerability

Please use GitHub Private Vulnerability Reporting on this repository. You will get an acknowledgement within 72 hours, and we aim to disclose within 90 days.

In scope: secret leakage (API keys reaching logs, reports, the cache or recorded fixtures), the PR-comment poster, the release download and checksum verification in the Action, and parsing of untrusted suite or dataset files.

## Notes for users

- API keys are only read from the environment variable named by `api_key_env`. `evalvet validate` rejects inline keys.
- `.evalvet/cache` contains model outputs. Keep it out of git (`evalvet init` adds it to `.gitignore`); only baselines are meant to be committed.
- Fork pull requests have no secrets. The Action runs them with `--cache replay` against cached outputs. Do not switch to `pull_request_target` without understanding that it runs untrusted code with your secrets.
- `--http-record` scrubs `Authorization`, `X-Api-Key`, cookies and request ids from fixtures. Review fixtures before committing them anyway.
