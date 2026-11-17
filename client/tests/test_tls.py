import os
import shutil
import subprocess
import sys
import tempfile
import unittest
import warnings
from unittest import mock

sys.path.insert(0, os.path.dirname(__file__))      # for fake_server when run from anywhere
import fake_server as fs                           # noqa: E402
import gonexus_client.client as client_module      # noqa: E402
from gonexus_client import GonexusError, from_uri  # noqa: E402


@unittest.skipUnless(shutil.which("openssl"), "openssl is needed to create a test certificate")
class HttpsTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.dir = tempfile.mkdtemp()
        cls.crt, cls.key = os.path.join(cls.dir, "server.crt"), os.path.join(cls.dir, "server.key")
        subprocess.run(
            ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-sha256", "-days", "2",
             "-keyout", cls.key, "-out", cls.crt, "-subj", "/CN=localhost",
             "-addext", "subjectAltName=DNS:localhost,IP:127.0.0.1"],
            check=True, capture_output=True)
        cls.srv, cls.url = fs.start(cls.crt, cls.key)

    @classmethod
    def tearDownClass(cls):
        cls.srv.shutdown()
        shutil.rmtree(cls.dir, ignore_errors=True)

    def transports(self):
        """Run each check with the `requests` and with the urllib code path."""
        yield "default", contextless()
        yield "urllib", mock.patch.object(client_module, "requests", None)

    def check_all(self, fn):
        for name, ctx in self.transports():
            with self.subTest(transport=name), ctx:
                fn()

    def test_untrusted_certificate_is_refused_with_a_hint(self):
        def fn():
            with mock.patch.dict(os.environ, {}, clear=False):
                os.environ.pop("GONEXUS_CA_BUNDLE", None)
                with self.assertRaises(GonexusError) as cm:
                    list(from_uri(self.url, timeout=10))
            self.assertIn("not trusted", str(cm.exception))
            self.assertIn("verify=", str(cm.exception))
        self.check_all(fn)

    def test_verify_with_ca_file(self):
        def fn():
            c = from_uri(self.url, verify=self.crt)
            self.assertEqual(list(c), [fs.KEY])
            self.assertEqual(c[fs.KEY]["proc/data/I"][3].tolist(), [300, 301, 302, 303, 304])
        self.check_all(fn)

    def test_verify_false_works_and_warns(self):
        def fn():
            with warnings.catch_warnings(record=True) as w:
                warnings.simplefilter("always")
                c = from_uri(self.url, verify=False)
                self.assertEqual(list(c), [fs.KEY])
            self.assertTrue(any("verification is disabled" in str(x.message) for x in w))
        self.check_all(fn)

    def test_environment_variable(self):
        def fn():
            with mock.patch.dict(os.environ, {"GONEXUS_CA_BUNDLE": self.crt}):
                self.assertEqual(list(from_uri(self.url)), [fs.KEY])
        self.check_all(fn)

    def test_plain_http_is_unaffected(self):
        srv, url = fs.start()
        try:
            self.assertEqual(list(from_uri(url)), [fs.KEY])
            self.assertEqual(list(from_uri(url, verify=self.crt)), [fs.KEY])
        finally:
            srv.shutdown()


def contextless():
    import contextlib
    return contextlib.nullcontext()


if __name__ == "__main__":
    unittest.main()
