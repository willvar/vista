package useragent

import "runtime"

// Keep the Codex preset and account transport's identity version together.
// https://github.com/openai/codex/blob/main/codex-rs/login/src/auth/default_client.rs
const CodexOriginator = "codex_cli_rs"

func Codex() string {
	return CodexOriginator + "/" + CodexVersion + " " + platformComment(runtime.GOOS, runtime.GOARCH)
}
