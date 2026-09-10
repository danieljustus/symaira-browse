import importlib.util
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("run_mcp_contract.py")
SPEC = importlib.util.spec_from_file_location("run_mcp_contract", SCRIPT)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class RunMcpContractTests(unittest.TestCase):
    def test_prepare_endpoint_parent_creates_native_parent(self):
        with tempfile.TemporaryDirectory() as root:
            endpoint = Path(root) / "runtime" / "symbrowse" / "default.sock"
            MODULE.prepare_endpoint_parent(endpoint)
            self.assertTrue(endpoint.parent.is_dir())


if __name__ == "__main__":
    unittest.main()
