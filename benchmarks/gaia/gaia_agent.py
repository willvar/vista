"""GAIA adapter with network settings and run-local Codex refresh state."""

import asyncio
import json
from pathlib import Path
import shlex
import tempfile

import benchmark_profile
from benchmark_profile import selected_profile
from vista_agent import REMOTE, VistaAgent
import vista_runner


HERE = Path(__file__).resolve().parent


class GAIAVistaAgent(VistaAgent):
    async def install(self, environment):
        await super().install(environment)
        await environment.upload_file(Path(vista_runner.__file__), REMOTE + "/vista_runner.py")
        for name in ("gaia_runner.py", "dataset.py", "instruction.txt", "sources.json", "benchmark_profile.py"):
            source = HERE / name
            if name == "benchmark_profile.py":
                source = Path(benchmark_profile.__file__)
            await environment.upload_file(source, REMOTE + "/" + ("runner.py" if name == "gaia_runner.py" else name))
        network = self.profile / "search.json"
        paths = [REMOTE + "/vista_runner.py", REMOTE + "/runner.py", REMOTE + "/dataset.py", REMOTE + "/instruction.txt",
                 REMOTE + "/sources.json", REMOTE + "/benchmark_profile.py"]
        if network.is_file():
            paths.append(REMOTE + "/xdg/vista/search.json")
            await environment.upload_file(network, paths[-1])
        command = "chmod 600 " + shlex.join(paths)
        if environment.default_user is not None:
            command += "; chown " + shlex.quote(str(environment.default_user)) + " " + shlex.join(paths)
        if (await self.exec_as_root(environment, command)).return_code:
            raise RuntimeError("Could not set GAIA adapter file permissions")

    async def run(self, instruction, environment, context):
        try:
            await super().run(instruction, environment, context)
        finally:
            _, credentials = selected_profile(self.profile, self.model_name, self.model_key)
            for target, relative in credentials:
                try:
                    with tempfile.TemporaryDirectory(dir=self.profile, prefix=".auth-transfer-") as temporary:
                        path = Path(temporary) / "credential.json"
                        await asyncio.wait_for(environment.download_file(REMOTE + "/xdg/vista/" + relative, path), timeout=20)
                        updated, original = json.loads(path.read_text()), json.loads(target.read_text())
                        if (updated.get("accountId") != original.get("accountId")
                                or not all(updated.get(key) for key in ("accessToken", "refreshToken", "expiresAt"))):
                            raise ValueError("Unexpected benchmark OAuth state")
                        if updated != original:
                            path.chmod(0o600)
                            path.replace(target)
                except Exception as error:
                    self.logger.warning("Run-local OAuth collection: %s", type(error).__name__)
