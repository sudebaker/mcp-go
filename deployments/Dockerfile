# Stage 1: Build Go binary
FROM golang:1.23-alpine AS go-builder

WORKDIR /build

# Install build dependencies
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY cmd/ ./cmd/
COPY internal/ ./internal/

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o mcp-server ./cmd/server

# Stage 2: Python base with system dependencies
FROM python:3.11-slim-bookworm AS python-base

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
    # General utilities
    curl \
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

# PDF Reports
jinja2>=3.1.2
weasyprint>=60.0

# Data Analysis
pandas>=2.1.0
openpyxl>=3.1.0
numpy>=1.24.0
matplotlib>=3.8.0

# Vision/OCR
opencv-python-headless>=4.8.0
pillow>=10.0.0
pytesseract>=0.3.10

# Knowledge Base
psycopg2-binary>=2.9.0
sentence-transformers>=2.2.0

# Document parsing
pypdf>=3.0.0
python-docx>=1.0.0

# Config Auditor & PDF Reports
pyyaml>=6.0
markdown>=3.5
minio>=7.2.0
EOF

# Install Python dependencies
RUN pip install --no-cache-dir -r requirements.txt

# Pre-download sentence-transformers model
RUN python -c "from sentence_transformers import SentenceTransformer; SentenceTransformer('all-MiniLM-L6-v2')"

# Stage 4: Final image
FROM python-base AS final

WORKDIR /app

# Copy Python packages from deps stage
COPY --from=python-deps /usr/local/lib/python3.11/site-packages /usr/local/lib/python3.11/site-packages
COPY --from=python-deps /usr/local/bin /usr/local/bin

# Copy pre-downloaded models
COPY --from=python-deps /root/.cache/huggingface /root/.cache/huggingface

# Copy Go binary
COPY --from=go-builder /build/mcp-server /app/mcp-server

# Copy configuration and tools
COPY configs/ /app/configs/
COPY tools/ /app/tools/
COPY templates/ /app/templates/

# Create data directory
RUN mkdir -p /data/reports

# Set environment variables
ENV PYTHONUNBUFFERED=1
ENV PYTHONDONTWRITEBYTECODE=1

# Expose port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/sse || exit 1

# Run the server
ENTRYPOINT ["/app/mcp-server"]
CMD ["-config", "/app/configs/config.yaml"]
