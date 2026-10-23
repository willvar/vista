"""Harbor adapter for an unmodified Vista binary, with per-trial state."""

import asyncio
import json
from pathlib import Path
import shlex
import tempfile

from harbor.agents.installed.base import BaseInstalledAgent
from harbor.models.agent.context import ModelUsage

from benchmark_profile import digest, selected_profile


HERE = Path(__file__).resolve().parent
REMOTE = "/installed-agent/vista-eval"


class VistaAgent(BaseInstalledAgent):
    skip_model_parsing = True

    @staticmethod
    def name():
        return "vista"

    def __init__(self, logs_dir, *, binary_path, profile_path, model_key=None,
                 reasoning_effort=None, **kwargs):
        super().__init__(logs_dir=logs_dir, **kwargs)
        self.binary = Path(binary_path).resolve()
        self.profile = Path(profile_path).resolve()
        self.model_key = model_key
        self.reasoning_effort = reasoning_effort
        self._version = self._version or ("sha256:" + digest(self.binary)[:12])

    async def install(self, environment):
        if self.mcp_servers or self.skills_dir:
            raise ValueError("This adapter does not install benchmark MCP services or skills")
        config, credentials = selected_profile(self.profile, self.model_name, self.model_key)
        await self.ensure_system_dependencies(environment, ("python3", "ca_certificates"))
        command = f"mkdir -p {REMOTE}/xdg/vista/codex-auth"
        result = await self.exec_as_root(environment, command)
        if result.return_code:
            raise RuntimeError("Could not create the isolated Vista directory")
        await environment.upload_file(self.binary, REMOTE + "/vista")
        await environment.upload_file(HERE / "vista_runner.py", REMOTE + "/runner.py")
        with tempfile.TemporaryDirectory(prefix="vista-harbor-profile-") as directory:
            path = Path(directory) / "models.json"
            path.write_text(json.dumps(config))
            path.chmod(0o600)
            await environment.upload_file(path, REMOTE + "/models.json")
        for source, relative in credentials:
            await environment.upload_file(source, REMOTE + "/xdg/vista/" + relative)
        catalog = self.profile / "omp-models.json"
        if catalog.is_file():
            await environment.upload_file(catalog, REMOTE + "/omp-models.json")
        command = f"chmod 700 {REMOTE} {REMOTE}/xdg {REMOTE}/xdg/vista {REMOTE}/xdg/vista/codex-auth; chmod 600 {REMOTE}/models.json; chmod 755 {REMOTE}/vista"
        for _, relative in credentials:
            command += "; chmod 600 " + shlex.quote(REMOTE + "/xdg/vista/" + relative)
        if environment.default_user is not None:
            command += "; chown -R " + shlex.quote(str(environment.default_user)) + " " + REMOTE
        if (await self.exec_as_root(environment, command)).return_code:
            raise RuntimeError("Could not set isolated Vista permissions")

    async def run(self, instruction, environment, context):
        with tempfile.TemporaryDirectory(prefix="vista-harbor-instruction-") as directory:
            path = Path(directory) / "instruction.md"
            path.write_text(instruction)
            await environment.upload_file(path, REMOTE + "/instruction.md")
        command = f"python3 {REMOTE}/runner.py --logs-dir {shlex.quote(str(self.environment_logs_dir))}"
        if self.reasoning_effort is not None:
            command += " --effort " + shlex.quote(self.reasoning_effort)
        try:
            result = await self.exec_as_agent(environment, command)
            (self.logs_dir / "runner.stdout.txt").write_text(result.stdout or "")
            (self.logs_dir / "runner.stderr.txt").write_text(result.stderr or "")
            if result.return_code:
                raise RuntimeError("Vista runner failed; see the trial's agent logs")
        finally:
            try:
                await asyncio.wait_for(asyncio.shield(self.exec_as_agent(
                    environment, f"python3 {REMOTE}/runner.py --stop", timeout_sec=20,
                )), timeout=25)
            except Exception as error:
                self.logger.warning("Isolated Vista cleanup: %s", type(error).__name__)
            self.populate_context_post_run(context)

    def populate_context_post_run(self, context):
        from summarize import vista_usage

        usage = vista_usage(self.logs_dir / "vista-events.jsonl")
        if usage["complete"]:
            context.model_usage = {key: ModelUsage(**value) for key, value in usage["models"].items()}
            for field in ("n_input_tokens", "n_cache_tokens", "n_output_tokens"):
                setattr(context, field, sum(getattr(value, field) for value in context.model_usage.values()))
        context.metadata = {"adapter": "vista-http-v1", "reasoning_effort": self.reasoning_effort,
                            **{key: value for key, value in usage.items() if key != "models"}}
