# Stage 1: Build Go binary
FROM golang:1.25-alpine AS go-builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY .git/ /tmp/build/.git/

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o mcp-server ./cmd/server

# Stage 2: Python base with system dependencies
FROM python:3.14-slim-bookworm AS python-base

# Install system dependencies for all Python tools
RUN apt-get update && apt-get install -y --no-install-recommends \
    # For WeasyPrint (PDF generation)
    libpango-1.0-0 \
    libpangocairo-1.0-0 \
    libgdk-pixbuf2.0-0 \
    libffi-dev \
    shared-mime-info \
    # For OpenCV
    libgl1-mesa-glx \
    libglib2.0-0 \
    libsm6 \
    libxext6 \
    libxrender1 \
    # For Tesseract OCR
    tesseract-ocr \
    tesseract-ocr-eng \
    tesseract-ocr-spa \
    # For PostgreSQL
    libpq-dev \
    # For python-magic (metadata_extractor)
    libmagic1 \
    # General utilities
    curl \
    git \
    && rm -rf /var/lib/apt/lists/*

# Stage 3: Install Python dependencies
FROM python-base AS python-deps

WORKDIR /app

# Create requirements file
COPY <<EOF /app/requirements.txt
# Core dependencies
requests>=2.31.0
httpx>=0.27.0
tenacity>=8.2.0
validators>=0.20.0
gitpython>=3.1.40

# RSS Reader
feedparser>=6.0.0

# Web Scraper
beautifulsoup4>=4.12.0

# PDF Reports
jinja2>=3.1.2
weasyprint>=60.0

# Data Analysis
pandas>=2.3.2,<3.0
openpyxl>=3.1.0
numpy>=2.3.2,<3.0
matplotlib>=3.8.0
tabulate>=0.9.0
docker>=7.0.0

# Vision/OCR
opencv-python-headless>=4.11,<5.0
pillow>=10.0.0
pytesseract>=0.3.10

# Knowledge Base
psycopg2-binary>=2.9.0
fastembed>=0.4.0
sentence-transformers>=2.2.0
transformers>=4.38.0,<5.0.0
accelerate>=0.26.0

# Document parsing
pypdf>=3.0.0
python-docx>=1.0.0

# Format checker
ruff>=0.6.0

# Config Auditor & PDF Reports
pyyaml>=6.0
markdown>=3.5
minio>=7.2.0

# Forensic investigation tools
rapidfuzz>=3.0.0
python-magic>=0.4.27
imagehash>=4.3.1
folium>=0.14.0

# Graph / search drivers for forensic tools
neo4j>=5.14.0
opensearch-py>=2.4.0
EOF

# Install Python dependencies
# Force CPU-only torch to avoid pulling NVIDIA/CUDA transitive dependencies.
# The trio is pinned to the newest release that publishes cp314 wheels on the CPU index
# (torch 2.9.0+ is the first with cp314; torchaudio stops at 2.11.0, so torch stays at 2.11.0).
RUN pip install --no-cache-dir torch==2.11.0+cpu torchvision==0.26.0+cpu torchaudio==2.11.0+cpu --index-url https://download.pytorch.org/whl/cpu
RUN pip install --no-cache-dir -r requirements.txt

# Pre-download and cache embedding models for offline use
# fastembed stores models in ~/.cache/fastembed/ (ONNX weights)
# sentence-transformers stores in ~/.cache/huggingface/ (PyTorch weights)
ENV FASTEMBED_CACHE_PATH=/root/.cache/fastembed
RUN mkdir -p /root/.cache/fastembed && \
    python -c "from fastembed import TextEmbedding; TextEmbedding(model_name='sentence-transformers/all-MiniLM-L6-v2')"
RUN python -c "from sentence_transformers import SentenceTransformer; SentenceTransformer('all-MiniLM-L6-v2')"

# Stage 4: Final image
FROM python-base AS final

WORKDIR /app

# Non-root runtime user. HOME is /app so every tool that resolves a cache or a
# config dir through $HOME lands somewhere this user owns.
RUN groupadd --system --gid 10001 app \
    && useradd --system --uid 10001 --gid app --no-create-home \
        --home-dir /app --shell /usr/sbin/nologin app

# Copy Python packages from deps stage
COPY --from=python-deps /usr/local/lib/python3.14/site-packages /usr/local/lib/python3.14/site-packages
COPY --from=python-deps /usr/local/bin /usr/local/bin

# Copy pre-downloaded models (both sentence-transformers and fastembed caches)
# to /app/.cache: fastembed and sentence-transformers find them through
# FASTEMBED_CACHE_PATH / HF_HOME, set below for the non-root user. --chown does
# the ownership here instead of a recursive chown, which would duplicate every
# model file into an extra layer.
COPY --chown=10001:10001 --from=python-deps /root/.cache/huggingface /app/.cache/huggingface
COPY --chown=10001:10001 --from=python-deps /root/.cache/fastembed /app/.cache/fastembed

# Copy Go binary
COPY --chown=10001:10001 --from=go-builder /build/mcp-server /app/mcp-server

# Copy configuration and tools. Code stays root-owned on purpose: only the
# cache (/app/.cache) and /data need to be writable by the runtime user, so a
# compromised tool cannot rewrite the code or the tool definitions it runs.
COPY configs/ /app/configs/
COPY tools/ /app/tools/
COPY templates/ /app/templates/

# Generate a lightweight git-log snapshot for tools that need commit history.
# Avoids copying the entire .git/ directory into the final image.
RUN if [ -d /tmp/build/.git ]; then \
        git -C /tmp/build log --pretty=format:'%H %s' -n 100 > /app/.git-log.txt; \
    else \
        touch /app/.git-log.txt; \
    fi

# Create data directory (the tools write reports and uploads here) and hand /app
# and /data to the runtime user. Non-recursive on purpose: the big trees under
# /app were already copied with --chown and only the roots are still root-owned.
RUN mkdir -p /data/reports /data/uploads /data/input \
    && chown app:app /app /app/.cache /data /data/reports /data/uploads /data/input

# Set environment variables
ENV HOME=/app
ENV HF_HOME=/app/.cache/huggingface
ENV FASTEMBED_CACHE_PATH=/app/.cache/fastembed
ENV PYTHONUNBUFFERED=1
ENV PYTHONDONTWRITEBYTECODE=1

# Run as the non-root user
USER app

# Expose port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/health || exit 1

# Run the server
ENTRYPOINT ["/app/mcp-server"]
CMD ["-config", "/app/configs/config.yaml"]
