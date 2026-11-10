import io
import contextlib
import os
import sys
import unittest

import numpy as np

sys.path.insert(0, os.path.dirname(__file__))      # for fake_server when run from anywhere
import fake_server as fs                           # noqa: E402
from gonexus_client import Field, GonexusError, Group, from_uri   # noqa: E402
from gonexus_client.cli import main                # noqa: E402

REF = np.array(fs.FLAT, dtype=np.int64).reshape(fs.ROWS, fs.COLS)


class ClientTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.srv, cls.url = fs.start()
        cls.client = from_uri(cls.url, api_key="secret")
        cls.nx = cls.client["chess/results.nxs"]
        cls.I = cls.nx["proc/data/I"]

    @classmethod
    def tearDownClass(cls):
        cls.srv.shutdown()

    # navigation
    def test_listing_and_lookup(self):
        self.assertEqual(list(self.client), [fs.KEY])
        self.assertIs(self.client["results"], self.client[fs.KEY])        # forgiving lookup
        self.assertEqual(list(self.nx), ["entry", "proc"])
        self.assertIsInstance(self.nx["entry"], Group)
        self.assertEqual(self.nx["entry"].nx_class, "NXentry")
        self.assertIn("proc/data", self.nx)
        self.assertNotIn("nope", self.nx)
        self.assertIs(self.nx["proc"]["data"]["I"], self.I)
        self.assertTrue(self.client.healthy())

    def test_field_metadata(self):
        self.assertIsInstance(self.I, Field)
        self.assertEqual(self.I.shape, (fs.ROWS, fs.COLS))
        self.assertEqual(self.I.dtype, np.int64)
        self.assertEqual(self.I.units, "a.u")
        self.assertEqual(self.I.attrs["long_name"], "Intensity")
        self.assertEqual(self.I.size, fs.ROWS * fs.COLS)
        self.assertEqual(len(self.I), fs.ROWS)

    # data access (server caps limit at 37, so every read is multi-chunk)
    def test_full_read_is_chunked_correctly(self):
        np.testing.assert_array_equal(self.I.read(), REF)
        np.testing.assert_array_equal(self.I.read(chunk_size=11), REF)

    def test_slicing_matches_numpy(self):
        I = self.I
        for idx in [10, -1, slice(3, 9), slice(0, 40, 7), slice(30, 5, -4), slice(5, 5),
                    (slice(2, 6), slice(1, 3)), (7, slice(None, None, 2)), (Ellipsis, 3),
                    (-2, -1), ([1, 5, 9],)]:
            with self.subTest(idx=idx):
                np.testing.assert_array_equal(I[idx], REF[idx])

    def test_bounds_and_scalars(self):
        with self.assertRaises(IndexError):
            self.I[fs.ROWS]
        self.assertEqual(self.nx["entry/scan_number"].read(), 7)
        self.assertEqual(str(self.nx["entry/title"].read()), '{"a": 1}')
        np.testing.assert_allclose(self.nx["proc/data/radial"].read(), [0, .5, 1, 1.5, 2])
        np.testing.assert_allclose(np.asarray(self.nx["proc/data/radial"]), [0, .5, 1, 1.5, 2])

    def test_iter_chunks(self):
        got = np.concatenate([b for _, b in self.I.iter_chunks(rows=7)])
        np.testing.assert_array_equal(got, REF)

    # errors / helpers
    def test_errors(self):
        for bad in ("nope", "entry/nope", "entry/scan_number/x"):
            with self.assertRaises(KeyError):
                self.nx[bad]
        with self.assertRaises(KeyError):
            self.client["zzz"]
        with self.assertRaises(GonexusError):
            self.client.open("bad/key")["entry"]
        with self.assertRaises(GonexusError):
            from_uri("http://127.0.0.1:1", timeout=1).refresh() or list(from_uri("http://127.0.0.1:1", timeout=1))

    def test_tree_walk_and_headers(self):
        text = self.nx.tree()
        self.assertIn("I = int64(40x5)", text)
        self.assertEqual([p for p, _ in self.nx.walk()][:3], ["entry", "entry/scan_number", "entry/title"])
        self.assertEqual(self.nx.tree_text(), "root:NXroot\n")
        list(self.client)
        self.assertEqual(self.srv.last_headers.get("Authorization"), "Apikey secret")

    def test_cli(self):
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            self.assertEqual(main(["gonexus-client", self.url]), 0)
            self.assertEqual(main(["gonexus-client", self.url, fs.KEY]), 0)
            self.assertEqual(main(["gonexus-client", self.url, fs.KEY, "proc/data/I"]), 0)
        out = buf.getvalue()
        self.assertIn(fs.KEY, out)
        self.assertIn("NXprocess", out)
        self.assertIn("@long_name = Intensity", out)


if __name__ == "__main__":
    unittest.main()
