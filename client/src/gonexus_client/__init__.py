"""gonexus_client - Python client for the gonexus HTTP server.

    from gonexus_client import from_uri
    client = from_uri("http://localhost:8081")
    data = client["chess/results.nxs"]["saxs_azimuthal/data/I"][10:20]
"""
from .client import (
    Client,
    Field,
    GonexusError,
    Group,
    NexusFile,
    from_uri,
)

__version__ = "0.1.0"

__all__ = ["from_uri", "Client", "NexusFile", "Group", "Field", "GonexusError", "__version__"]
