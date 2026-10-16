FROM python:3.13-slim

# Wheels for aarch64 exist for all of these, so nothing compiles on the Pi.
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY app.py index.html ./
# An unprivileged user, and a /data it owns: a new volume mounted there
# starts as a copy of it, so the app can write without root.
RUN useradd --uid 10001 --no-create-home app && mkdir /data && chown app /data
USER app
ENV DATA_DIR=/data PYTHONUNBUFFERED=1
EXPOSE 8000
# One process: jobs and WebSocket listeners live in memory. uvicorn stops
# taking requests on SIGTERM and runs the app's shutdown.
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8000", "--proxy-headers", "--timeout-graceful-shutdown", "20"]
