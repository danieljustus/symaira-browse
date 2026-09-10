import importlib.util
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock


MODULE = Path(__file__).with_name("mcp_fixture_gen.py")
spec = importlib.util.spec_from_file_location("mcp_fixture_gen", MODULE)
assert spec and spec.loader
fixture_gen = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture_gen)


class MCPFixtureGeneratorTests(unittest.TestCase):
    def test_missing_fixture_fails_check(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "missing.out"
            with self.assertRaises(SystemExit):
                fixture_gen.sync_file(path, b"expected", check=True)

    def test_fixture_drift_fails_check(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "drift.out"
            path.write_bytes(b"old")
            with self.assertRaises(SystemExit):
                fixture_gen.sync_file(path, b"new", check=True)

    def test_windows_endpoint_prefers_xdg_runtime_dir(self):
        environment = {
            "HOME": "/poison/home",
            "LOCALAPPDATA": "/windows/cache",
            "XDG_RUNTIME_DIR": "/explicit/runtime",
        }
        with mock.patch.object(fixture_gen.sys, "platform", "win32"):
            endpoint = fixture_gen.oracle_endpoint(environment, "session")
        self.assertEqual(endpoint, Path("/explicit/runtime/symbrowse/session.sock"))

    def test_windows_endpoint_falls_back_to_localappdata_without_xdg(self):
        environment = {"LOCALAPPDATA": "/windows/cache", "XDG_RUNTIME_DIR": ""}
        with mock.patch.object(fixture_gen.sys, "platform", "win32"):
            endpoint = fixture_gen.oracle_endpoint(environment, "session")
        self.assertEqual(endpoint, Path("/windows/cache/symbrowse/run/session.sock"))

    def test_oracle_environment_has_no_runner_configuration_or_secret_roots(self):
        poison = {
            "HOME": "/poison/home",
            "USERPROFILE": "/poison/profile",
            "LOCALAPPDATA": "/poison/local",
            "APPDATA": "/poison/appdata",
            "XDG_CONFIG_HOME": "/poison/config",
            "XDG_DATA_HOME": "/poison/data",
            "XDG_CACHE_HOME": "/poison/cache",
            "XDG_STATE_HOME": "/poison/state",
            "XDG_RUNTIME_DIR": "/poison/runtime",
            "SYMBROWSE_CONFIG": "/poison/config.toml",
            "SYMBROWSE_TOKEN": "poison-secret",
        }
        old = {key: os.environ.get(key) for key in poison}
        try:
            os.environ.update(poison)
            temporary, environment = fixture_gen.oracle_environment()
            try:
                for key, value in poison.items():
                    self.assertNotEqual(environment.get(key), value, key)
                for key in ("HOME", "USERPROFILE", "LOCALAPPDATA", "APPDATA",
                            "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
                            "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "TMPDIR", "TMP", "TEMP"):
                    self.assertTrue(Path(environment[key]).is_relative_to(temporary.name), key)
            finally:
                temporary.cleanup()
        finally:
            for key, value in old.items():
                if value is None:
                    os.environ.pop(key, None)
                else:
                    os.environ[key] = value


if __name__ == "__main__":
    unittest.main()
