#!/usr/bin/env python
"""gonexus python client session."""
import os
from gonexus_client import from_uri

client = from_uri(
    os.environ.get("GONEXUS_URI", "http://localhost:8081")
)

print(client)
print("Registered entries:", list(client))

for key in client:
    print(key, client[key])

# Access a registered NeXus file
nexus = client["chess/results.nxs"]
print(nexus)
print("Children:", list(nexus))

# Navigate the NeXus hierarchy
data_group = nexus["saxs_azimuthal"]["data"]            # or nexus["saxs_azimuthal/data"]
intensity = data_group["I"]
radial = data_group["radial"].read()

print("Shape:", intensity.shape, "Dtype:", intensity.dtype, "Units:", intensity.units)
print("Attributes:", intensity.attrs)

# Cheap partial reads: only the requested rows are downloaded
frame = intensity[10]                  # one row   (300 values)
block = intensity[100:200, 50:80]      # sub-block (100 x 30)
print("frame:", frame.shape, "block:", block.shape)

# Full read (several chunked requests under the hood)
data = intensity.read()
print("Shape:", data.shape)
print("Dtype:", data.dtype)
print("Minimum:", data.min())
print("Maximum:", data.max())

# Process an array larger than memory chunk by chunk
total = 0
for first_row, rows in intensity.iter_chunks(1000):
    total += int(rows.sum())
print("Sum:", total)

# Optional plot of one row against the radial axis
try:
    import matplotlib.pyplot as plt
    plt.plot(radial, frame)
    plt.xlabel("radial"); plt.ylabel(f"I ({intensity.units})")
    plt.show()
except ImportError:
    pass
