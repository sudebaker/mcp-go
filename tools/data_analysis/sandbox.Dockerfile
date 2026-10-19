FROM python:3.14-slim

# Security: Run as non-root user
RUN groupadd -r sandbox && useradd -r -g sandbox -u 1000 sandbox

# Create secure directories with proper permissions
RUN mkdir -p /data/input /data/output /app/tools/common && \
    chown -R sandbox:sandbox /data && \
    chmod 755 /data/input && \
    chmod 700 /data/output

WORKDIR /tmp

# Install dependencies with pinned versions for reproducibility.
# Pins are the newest releases that publish cp314 wheels (pandas 2.3.3 is the
# last 2.x with them, numpy/matplotlib moved to the 2.5/3.11 line for 3.14).
RUN pip install --no-cache-dir \
    pandas==2.3.3 \
    numpy==2.5.4 \
    matplotlib==3.11.2 \
    seaborn==0.13.2 \
    openpyxl==3.1.5 \
    xlrd==2.0.2 \
    tabulate==0.10.0

# Copy common modules (safe_file_ops, validators, etc.)
# Context is repo root, so use absolute paths from there
COPY tools/common/*.py /app/tools/common/
RUN chown -R sandbox:sandbox /app/tools && \
    chmod 755 /app/tools/common

# Copy and secure bootstrap script
# 0444, not 0400: the image drops to USER sandbox below, and a root-only mode
# made the container die on start with "Permission denied" on this file.
COPY tools/data_analysis/sandbox_bootstrap.py /sandbox/sandbox_bootstrap.py
RUN chown root:root /sandbox/sandbox_bootstrap.py && \
    chmod 444 /sandbox/sandbox_bootstrap.py

# Security: Drop to non-root user
USER sandbox

# Set secure environment variables
ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    INPUT_DIR=/data/input \
    OUTPUT_DIR=/data/output \
    MAX_FILE_SIZE_MB=100

CMD ["python3", "/sandbox/sandbox_bootstrap.py"]
