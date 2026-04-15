FROM python:3.11-slim

# Security: Run as non-root user
RUN groupadd -r sandbox && useradd -r -g sandbox -u 1000 sandbox

# Create secure directories with proper permissions
RUN mkdir -p /data/input /data/output /app/tools/common && \
    chown -R sandbox:sandbox /data && \
    chmod 755 /data/input && \
    chmod 700 /data/output

WORKDIR /tmp

# Install dependencies with pinned versions for reproducibility
RUN pip install --no-cache-dir \
    pandas==2.1.4 \
    numpy==1.26.2 \
    matplotlib==3.8.2 \
    seaborn==0.13.0 \
    openpyxl==3.1.2 \
    xlrd==2.0.1

# Copy common modules (safe_file_ops, validators, etc.)
# Context is repo root, so use absolute paths from there
COPY tools/common/*.py /app/tools/common/
RUN chown -R sandbox:sandbox /app/tools && \
    chmod 755 /app/tools/common

# Copy and secure bootstrap script
COPY tools/data_analysis/sandbox_bootstrap.py /sandbox/sandbox_bootstrap.py
RUN chown root:root /sandbox/sandbox_bootstrap.py && \
    chmod 400 /sandbox/sandbox_bootstrap.py

# Security: Drop to non-root user
USER sandbox

# Set secure environment variables
ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    INPUT_DIR=/data/input \
    OUTPUT_DIR=/data/output \
    MAX_FILE_SIZE_MB=100

CMD ["python3", "/sandbox/sandbox_bootstrap.py"]
