"""Validate the schema, examples and shared parser fixtures.

Run with: uv run --with jsonschema python config/schema_test.py
The daemon itself uses only the Go standard library.
"""

import json
import pathlib
import unittest

from jsonschema import Draft202012Validator

ROOT = pathlib.Path(__file__).resolve().parent


class SchemaTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        schema = json.loads((ROOT / "schema.json").read_text())
        Draft202012Validator.check_schema(schema)
        cls.validator = Draft202012Validator(schema)

    def test_examples(self):
        paths = [
            *ROOT.parent.joinpath("examples").glob("*.json"),
            ROOT.parent / "wanproxy.example.json",
            ROOT.parent.parent / "srv" / "demo" / "wanproxy.example.json",
        ]
        for path in paths:
            with self.subTest(path=path.name):
                self.validator.validate(json.loads(path.read_text()))

    def test_shared_fixtures(self):
        for kind in ("valid", "invalid"):
            paths = list(ROOT.joinpath("testdata", kind).glob("*.json"))
            self.assertTrue(paths)
            for path in paths:
                with self.subTest(path=path.name, kind=kind):
                    self.assertEqual(
                        self.validator.is_valid(json.loads(path.read_text())),
                        kind == "valid",
                    )


if __name__ == "__main__":
    unittest.main()
