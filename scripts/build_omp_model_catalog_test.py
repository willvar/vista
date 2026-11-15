import copy
import json
import pathlib
import unittest

from build_omp_model_catalog import build


FIXTURES = pathlib.Path(__file__).resolve().parent.parent / "internal/provider/testdata"


class OMPCatalogTests(unittest.TestCase):
    def setUp(self):
        self.expected = json.loads((FIXTURES / "omp_snapshot_expected.json").read_text())
        self.compact = json.loads((FIXTURES / "omp_source_compact_v1.json").read_text())

    def test_compact_source_preserves_stable_metadata(self):
        expected = self.expected
        self.assertEqual(build(self.compact, "fixture"), expected)
        self.assertEqual(expected["modelCount"], 4)
        self.assertTrue(expected["providers"]["fixture"]["bitmap"]["supportsTools"])
        self.assertFalse(expected["providers"]["opencode-zen"]["vista-no-tools"]["supportsTools"])

    def test_more_than_256_providers_are_preserved(self):
        source = {
            "version": 1,
            "keys": ["provider", "name", "api", "input"],
            "booleanShapes": [[]],
            "nodes": [[1, ["text"]], [0, [], 0, []]],
            "providers": [],
        }
        for i in range(328):
            provider = f"provider-{i:03d}"
            source["nodes"].append([0, [0, provider, 1, "Test model", 2, "openai-completions", 3, [0]], 0, []])
            source["providers"].append([provider, [len(source["nodes"]) - 1], [["model", [1]]]])
        result = build(source, "fixture")
        self.assertEqual(result["modelCount"], 328)
        self.assertEqual(len(result["providers"]), 328)
        for i in range(328):
            self.assertEqual(result["providers"][f"provider-{i:03d}"]["model"]["name"], "Test model")

    def test_legacy_source_is_rejected(self):
        with self.assertRaisesRegex(ValueError, "unsupported OMP catalog version"):
            build({"fixture": {"model": {"id": "model", "provider": "fixture"}}}, "fixture")

    def test_model_budget_is_preserved(self):
        source = copy.deepcopy(self.compact)
        source["providers"][0][2] = [source["providers"][0][2][0]] * 20001
        with self.assertRaisesRegex(ValueError, "invalid compact OMP model count"):
            build(source, "fixture")

    def test_invalid_formats_are_rejected(self):
        for version in (None, True, 2, "1"):
            with self.subTest(version=version):
                source = copy.deepcopy(self.compact)
                source["version"] = version
                with self.assertRaises(ValueError):
                    build(source, "fixture")
        for table in ("keys", "booleanShapes", "nodes", "providers"):
            with self.subTest(table=table):
                source = copy.deepcopy(self.compact)
                source[table] = []
                with self.assertRaises(ValueError):
                    build(source, "fixture")

    def test_invalid_and_cyclic_references_are_rejected(self):
        for reference in (-2, 1000000, 0.5, True):
            with self.subTest(reference=reference):
                source = copy.deepcopy(self.compact)
                source["providers"][0][1] = [reference]
                with self.assertRaises(ValueError):
                    build(source, "fixture")
        source = copy.deepcopy(self.compact)
        reference = source["providers"][0][1]
        source["nodes"][reference[0]][1] = [0, reference]
        with self.assertRaises(ValueError):
            build(source, "fixture")


if __name__ == "__main__":
    unittest.main()
