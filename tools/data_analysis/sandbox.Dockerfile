FROM python:3.11-slim

WORKDIR /tmp

RUN pip install --no-cache-dir \
    pandas>=2.0.0 \
    matplotlib>=3.7.0 \
    seaborn>=0.12.0 \
    numpy>=1.24.0

COPY sandbox_bootstrap.py /sandbox/sandbox_bootstrap.py
RUN chmod 444 /sandbox/sandbox_bootstrap.py

USER nobody

CMD ["python3", "/sandbox/sandbox_bootstrap.py"]
