#!/usr/bin/env python

import matplotlib.pyplot as plt
import numpy as np
import requests

# Server and Dataset details
BASE_URL = "http://foxden-dev.classe.cornell.edu:8080/field"
KEY = "chess/results.nxs"

# ---------------------------------------------------------
# Example A: Fetch a 1D Slice (Radial axis slice)
# ---------------------------------------------------------
radial_params = {
    "key": KEY,
    "path": "/saxs_azimuthal/data/radial",
    "offset": 50,  # Start index
    "limit": 100,  # Fetch 100 points
}

response = requests.get(BASE_URL, params=radial_params)
response.raise_for_status()
data = response.json()

# Convert JSON array directly to a NumPy array with the right data type
radial_slice = np.array(data["values"], dtype=data["dtype"])

print(f"Radial Slice shape: {radial_slice.shape}")
print(f"First 5 radial values: {radial_slice[:5]}")

# ---------------------------------------------------------
# Example B: Fetch a Slice of 2D Intensity Data (I)
# ---------------------------------------------------------
# For 2D datasets (shape [6587, 300]), offset/limit pagination fetches array elements
intensity_params = {
    "key": KEY,
    "path": "/saxs_azimuthal/data/I",
    "offset": 0,
    "limit": 300,  # Fetch first row/frame (300 intensity values)
}

resp_I = requests.get(BASE_URL, params=intensity_params)
resp_I.raise_for_status()
data_I = resp_I.json()

# Convert intensity slice to NumPy array
intensity_slice = np.array(data_I["values"], dtype=data_I["dtype"])

print(f"Intensity Slice shape: {intensity_slice.shape}")

# ---------------------------------------------------------
# Example C: Plotting SAXS Intensity vs. Radial Data
# ---------------------------------------------------------
# Fetch full 300 points for matching radial and intensity line slice
full_radial = requests.get(
    BASE_URL,
    params={
        "key": KEY,
        "path": "/saxs_azimuthal/data/radial",
        "offset": 0,
        "limit": 300,
    },
).json()["values"]
full_I = requests.get(
    BASE_URL,
    params={"key": KEY, "path": "/saxs_azimuthal/data/I", "offset": 0, "limit": 300},
).json()["values"]

q_vec = np.array(full_radial)
I_vec = np.array(full_I)

plt.figure(figsize=(8, 5))
plt.plot(q_vec, I_vec, label="Frame 0 (SAXS)")
plt.xlabel("q (1/Å)")
plt.ylabel("Intensity (a.u.)")
plt.title("SAXS Azimuthal Profile")
plt.yscale("log")
plt.grid(True)
plt.legend()
plt.show()
