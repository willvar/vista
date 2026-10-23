---
name: vista-guide
description: Configure and troubleshoot Vista itself using its settings APIs and official project sources. Use for model connections, MCP services, skills, personalization, version checks, or configuration imports from another agent.
---

# Vista guide

Official project sources:

- [Repository](https://github.com/willvar/vista): Vista's source code.
- [README](https://github.com/willvar/vista/blob/main/README.md) and [documentation](https://github.com/willvar/vista/tree/main/docs): current product behavior and technical contracts.
- [Releases](https://github.com/willvar/vista/releases): published versions, release notes, and downloads.
- [Issues](https://github.com/willvar/vista/issues): reported problems and feature discussions.

Vista settings apply to subsequent executions; saving them does not change the current execution's tools or model. Configure only the requested capability, preserve unrelated settings, and verify through the settings API. Do not edit Vista's database or request a restart for ordinary settings changes.

The runtime context identifies Vista's configuration directory. Read `state/instance.json` under that directory for the running service's `address`; do not assume its port. Use that address for `/api/` requests. If no instance is running, report that before trying to change settings through its API.

For version-specific questions or troubleshooting, read `GET /api/status`: `version`, `sourceCommit`, `sourceModified`, and `binaryHash` identify the running build; `hostOS` identifies its operating system. Check documentation and source at that commit, or the matching release tag if the commit is unavailable. The default branch may describe newer behavior. A modified build can differ from its recorded commit; report missing build metadata rather than guessing. Do not assume the service host has a Vista source checkout or that a local checkout matches the running executable.

Read [settings contracts](./references/settings.md) with `read_skill` when changing a connection, importing configuration, or managing personalization. Relative links in this system skill resolve under `skill://system/vista-guide/`; these are embedded documents, not filesystem paths.

For another agent's configuration, prefer Vista's preview/confirm import API when supported. Plugin-generated connections require inspecting the plugin's actual configuration and deriving an equivalent Vista connection. Do not execute a source plugin to discover credentials or assume that a provider name establishes its protocol. Keep keys and tokens out of chat, logs, command arguments, and saved diagnostic artifacts; resolve them from the source inside the configuration request.

For failures, distinguish reading configuration, saving it, model discovery, and an actual model request. A successful save does not prove live inference compatibility. Use the returned HTTP status, upstream error code and explanation; a 401 alone does not establish whether the key is expired, revoked, or the authentication format is wrong.
