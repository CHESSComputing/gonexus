# gonexus-client

Python client for the gonexus HTTP server (`servers/go`).

## Install

```bash
cd client
pip install .            # or: pip install -e .   (development)
pip install ".[fast]"    # adds `requests` (HTTP connection re-use)
```

## Use

```python
from gonexus_client import from_uri

client = from_uri("http://localhost:8081")       # api_key=... optional
nexus = client["chess/results.nxs"]
I = nexus["saxs_azimuthal/data/I"]
print(I.shape, I.dtype, I.units)
row = I[10]                  # only this row is downloaded
data = I.read()              # whole array as numpy
```

Command line:

```bash
gonexus-client http://localhost:8081                                   # list files
gonexus-client http://localhost:8081 chess/results.nxs                 # tree
gonexus-client http://localhost:8081 chess/results.nxs saxs_azimuthal/data/I
python -m gonexus_client ...                                           # same thing
```

See `examples/demo.py` for the full session example

## Test

```bash
pip install -e ".[test]"
pytest                       # or: python -m unittest discover -s tests
```

The tests start an in-process fake gonexus server; no real server is needed.
