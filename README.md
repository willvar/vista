# Vista

[简体中文](./README.zh-CN.md)

![Vista desktop interface comparing Samsung Galaxy S26 Ultra, OnePlus 15, and iPhone Pro Max for gaming in the US](./docs/assets/vista-hero.png)

Vista is a general-purpose agent for Linux, Windows, and macOS. Describe a goal in its browser interface and let the agent use files, shell commands, web queries, MCP tools, and skills to complete the work. Each open conversation has its own Worker process; conversations and their history are stored where Vista runs.

## Getting started

Download the archive for your system from [GitHub Releases](https://github.com/willvar/vista/releases/latest) and extract it.
Linux, Windows, and macOS each provide x86-64 (`amd64`) and ARM64 (`arm64`) archives.

Run `vista` to start the service in the background and open the default browser. Opening it again uses the running instance. The browser opens `http://127.0.0.1:5186/` by default, while the service listens on all IPv4 interfaces (`0.0.0.0`). Other devices use `http://<host-IP>:5186/`. Set the optional `VISTA_HOST` environment variable to a specific IPv4 address to restrict the listener; HTTPS is provided by a reverse proxy, not Vista itself.

On Windows, open `vista-desktop.exe` next to `vista.exe` to use the [desktop app](./docs/desktop.md) with WebView2. It opens the same interface and adds a notification-area icon for opening Vista, checking status, restarting, and stopping the service. Closing the window leaves the icon and service running. The command-line service does not add an icon.

Connect a model provider in Settings before sending the first message. Built-in connections provide their authentication and protocol settings; custom connections let you choose the conversation API format and authentication independently. Model availability follows the connected account or service.

| Command | Action |
| --- | --- |
| `vista` / `vista start` | Start the service or open the existing instance |
| `vista status` | Show service, conversation, Worker, and process status |
| `vista stop` | Stop the service and its managed work, keeping history |
| `vista restart` | Stop and start the service, keeping history |

Closing the browser does not stop work. Closing a conversation stops its Worker and managed background work, while retaining its history; deleting it also removes its associated data. Restarting Vista records unfinished operations as interrupted and does not replay their side effects.

About Vista provides service restart and port settings. Restart keeps conversations and the current port, then continues unfinished agent tasks from saved context; managed background processes stop. See [service controls](./docs/product.md#about-vista-and-service-controls) for details.

## Build from source

Building requires Go 1.26+, Node.js with native TypeScript support, npm, and Make. From the repository root:

```sh
make build
./bin/vista start
```

`make build` builds and embeds the frontend with version `dev`; use `make build VERSION=v0.1.0` for a release version. Development rules are in [AGENTS.md](./AGENTS.md); build checks and test commands are in [verification](./docs/testing.md).

For a desktop window that uses the same interface, see the [Wails desktop build](./docs/desktop.md).

## Configuration and state directories

Settings manages service connections, model web access, MCP, global instructions, skills, and resource storage.

Configuration and conversations are stored under these directories, with runtime data in `state/`:

| Platform | Configuration root |
| --- | --- |
| Linux | `$XDG_CONFIG_HOME/vista`, or `~/.config/vista` when unset |
| Windows | `%APPDATA%/vista` |
| macOS | `~/Library/Application Support/vista` |

The directory includes settings, plaintext credentials, global `AGENTS.md` instructions, and skills. Keep it private. See [configuration editing](./docs/product.md#configuration-editing) for file formats and upgrades, and [configuration storage](./docs/architecture.md#unified-configuration-storage) for technical contracts.

## Conversations and resources

Conversations have no fixed working directory. Provide relevant paths in the task. Different conversations share the real filesystem: editing or withdrawing a message rolls back conversation context only, keeping file changes and other tool side effects.

Closing a conversation keeps its attachments. After deleting the last conversation that uses an attachment, its original file becomes eligible for manual cleanup in Settings. See [resources](./docs/resources.md).

Vista can compact long conversations automatically after a model reports a context limit, or manually while idle. See [context compaction](./docs/context-compaction.md).

## Documentation

- [Product scope and audience](./docs/product.md)
- [Architecture and runtime contracts](./docs/architecture.md)
- [Provider routing and model metadata](./docs/providers.md)
- Advanced configuration: [network and proxies](./docs/network.md), [prompt caching](./docs/prompt-caching.md)
- [Worker and background process lifecycle](./docs/process-lifecycle.md)
- [Test goals and capacity measurement](./docs/testing.md)
- [Benchmarks: setup and results](./docs/benchmarks.md)
- [Frontend, internationalization, and browser checks](./web/README.md)
- [Context compaction](./docs/context-compaction.md), [resources](./docs/resources.md), and [conversation rollback](./docs/message-revert.md)

## License

Vista's original code and documentation are licensed under the [MIT License](./LICENSE). Third-party components retain their own licenses and copyrights; the complete [license and attribution notices](./licenses/NOTICE) are included in distribution archives and available at `/licenses` on the running service.
