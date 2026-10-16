"""thumbnailer: an example app for piway.

Upload an image; a background worker makes thumbnails of it in three sizes
(CPU and memory work), reports progress over a WebSocket, and the files
expire after a while. Everything lives on the data volume.

    POST /api/images            multipart "file": queues a job
    GET  /api/jobs[/{id}]       jobs and their state
    WS   /ws                    job events as they happen
    GET  /files/{id}/{name}     the original and the thumbnails
    GET  /api/info, /healthz    the example apps' common contract
"""

import asyncio
import hashlib
import json
import logging
import os
import platform
import queue
import shutil
import sys
import threading
import time
import uuid
from contextlib import asynccontextmanager
from pathlib import Path

from fastapi import FastAPI, HTTPException, UploadFile, WebSocket, WebSocketDisconnect
from fastapi.responses import FileResponse, HTMLResponse
from PIL import Image, ImageOps

DATA = Path(os.environ.get("DATA_DIR", "/data"))
EXPIRE_MINUTES = int(os.environ.get("EXPIRE_MINUTES", "60"))
MAX_UPLOAD = int(os.environ.get("MAX_UPLOAD_MB", "20")) << 20
SIZES = (128, 512, 1024)
Image.MAX_IMAGE_PIXELS = 40_000_000  # about 160 MB decoded: keeps a job inside run.memory

logging.basicConfig(stream=sys.stdout, level=logging.INFO, format='{"level":"%(levelname)s","msg":"%(message)s"}')
log = logging.getLogger("thumbnailer")

jobs: dict[str, dict] = {}  # id -> job, since this process started
work: "queue.Queue[str | None]" = queue.Queue()
listeners: set[asyncio.Queue] = set()
loop: asyncio.AbstractEventLoop | None = None


def publish(job: dict) -> None:
    """Sends a job's state to every WebSocket, from any thread."""
    if loop is None:
        return
    event = json.dumps(job)
    for q in list(listeners):
        loop.call_soon_threadsafe(q.put_nowait, event)


def worker() -> None:
    """Makes the thumbnails, one job at a time, in a thread of its own."""
    while (job_id := work.get()) is not None:
        job = jobs[job_id]
        job.update(state="running", progress=0)
        publish(job)
        started = time.monotonic()
        try:
            folder = DATA / job_id
            with Image.open(folder / job["original"]) as im:
                im = ImageOps.exif_transpose(im).convert("RGB")
                for i, size in enumerate(SIZES, 1):
                    thumb = im.copy()
                    thumb.thumbnail((size, size), Image.Resampling.LANCZOS)
                    thumb.save(folder / f"{size}.jpg", "JPEG", quality=85, optimize=True)
                    job.update(progress=round(100 * i / len(SIZES)), thumbs=[f"{s}.jpg" for s in SIZES[:i]])
                    publish(job)
            job.update(state="done", seconds=round(time.monotonic() - started, 2))
            log.info(f"job {job_id} done in {job['seconds']}s")
        except Exception as e:  # a bad image fails its job, not the worker
            job.update(state="failed", error=str(e))
            log.warning(f"job {job_id} failed: {e}")
        publish(job)


async def expire() -> None:
    """Deletes jobs older than EXPIRE_MINUTES, every minute."""
    while True:
        cutoff = time.time() - EXPIRE_MINUTES * 60
        for folder in DATA.glob("*/"):
            if folder.stat().st_mtime < cutoff:
                shutil.rmtree(folder, ignore_errors=True)
                if jobs.pop(folder.name, None) is not None:
                    publish({"id": folder.name, "state": "expired"})
                log.info(f"expired {folder.name}")
        await asyncio.sleep(60)


@asynccontextmanager
async def lifespan(_: FastAPI):
    global loop
    loop = asyncio.get_running_loop()
    DATA.mkdir(parents=True, exist_ok=True)
    thread = threading.Thread(target=worker, daemon=True)
    thread.start()
    janitor = asyncio.create_task(expire())
    log.info(f"listening, data in {DATA}, files expire after {EXPIRE_MINUTES} min")
    yield
    # SIGTERM: uvicorn stops taking requests, then this runs. Let the job in
    # hand finish; queued ones are dropped with the container.
    log.info("shutting down")
    janitor.cancel()
    work.put(None)
    await asyncio.to_thread(thread.join, 20)


app = FastAPI(lifespan=lifespan)


@app.get("/healthz")
def healthz() -> str:
    return "ok"


@app.get("/api/info")
def info() -> dict:
    secret = os.environ.get("THUMB_TOKEN", "")
    return {
        "app": "thumbnailer",
        "language": "Python",
        "runtime": f"{platform.python_implementation()} {platform.python_version()}",
        "env": {"GREETING": os.environ.get("GREETING", "")},
        "secret": {"name": "THUMB_TOKEN", "set": bool(secret), "length": len(secret),
                   "sha256_prefix": hashlib.sha256(secret.encode()).hexdigest()[:8]},
    }


@app.post("/api/images", status_code=202)
async def upload(file: UploadFile) -> dict:
    job_id = uuid.uuid4().hex[:12]
    folder = DATA / job_id
    folder.mkdir(parents=True)
    suffix = Path(file.filename or "").suffix.lower()[:8] or ".img"
    original = f"original{suffix}"
    size = 0
    with open(folder / original, "wb") as out:
        while chunk := await file.read(1 << 20):
            size += len(chunk)
            if size > MAX_UPLOAD:
                out.close()
                shutil.rmtree(folder)
                raise HTTPException(413, f"larger than {MAX_UPLOAD >> 20} MB")
            out.write(chunk)
    job = {"id": job_id, "name": file.filename, "bytes": size, "original": original,
           "state": "queued", "progress": 0, "thumbs": [], "created": time.time()}
    jobs[job_id] = job
    work.put(job_id)
    publish(job)
    log.info(f"job {job_id}: {file.filename}, {size} bytes")
    return job


@app.get("/api/jobs")
def list_jobs() -> list[dict]:
    return sorted(jobs.values(), key=lambda j: j["created"], reverse=True)


@app.get("/api/jobs/{job_id}")
def get_job(job_id: str) -> dict:
    if job_id not in jobs:
        raise HTTPException(404, "no such job")
    return jobs[job_id]


@app.get("/files/{job_id}/{name}")
def get_file(job_id: str, name: str) -> FileResponse:
    path = (DATA / job_id / name).resolve()
    if path.parent.parent != DATA.resolve() or not path.is_file():
        raise HTTPException(404, "no such file")
    return FileResponse(path)


@app.websocket("/ws")
async def events(ws: WebSocket) -> None:
    await ws.accept()
    q: asyncio.Queue = asyncio.Queue()
    listeners.add(q)
    try:
        while True:
            await ws.send_text(await q.get())
    except WebSocketDisconnect:
        pass
    finally:
        listeners.discard(q)


@app.get("/", response_class=HTMLResponse)
def index() -> str:
    return (Path(__file__).parent / "index.html").read_text()
