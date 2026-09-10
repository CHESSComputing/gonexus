FROM python:3.12-slim

# h5py's wheels bundle their own HDF5, so no libhdf5-dev is required here
# (unlike the Go server, which uses cgo bindings against the system lib).
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY servers/py/requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY servers/py/ .

ENV PORT=8080 HOST=0.0.0.0
EXPOSE 8080
ENTRYPOINT ["python3", "server.py"]
