"""
Thread-safe embedding model cache with fastembed (ONNX) as primary backend.
Falls back to sentence-transformers (PyTorch) if fastembed is unavailable.
Optimized for air-gapped, low-latency deployments.
"""

import threading
import os
import sys
import numpy as np
from typing import Optional

# Add parent directory to path for common imports
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from common.structured_logging import get_logger

logger = get_logger(__name__, "model_cache")

# ── Backend detection ──────────────────────────────────────────────────────
try:
    from fastembed import TextEmbedding
    FASTEMBED_AVAILABLE = True
except ImportError:
    FASTEMBED_AVAILABLE = False
    TextEmbedding = None  # type: ignore

try:
    from sentence_transformers import SentenceTransformer
    SENTENCE_TRANSFORMERS_AVAILABLE = True
except ImportError:
    SENTENCE_TRANSFORMERS_AVAILABLE = False
    SentenceTransformer = None  # type: ignore

# ── Security: Whitelist of allowed embedding models ────────────────────────
ALLOWED_MODELS = {
    "all-MiniLM-L6-v2",
    "all-MiniLM-L12-v2",
    "paraphrase-MiniLM-L6-v2",
    "paraphrase-multilingual-MiniLM-L12-v2",
}

# fastembed uses HuggingFace-style IDs even when model name is short
FASTEMBED_MODEL_MAP = {
    "all-MiniLM-L6-v2": "sentence-transformers/all-MiniLM-L6-v2",
    "all-MiniLM-L12-v2": "sentence-transformers/all-MiniLM-L12-v2",
    "paraphrase-MiniLM-L6-v2": "sentence-transformers/paraphrase-MiniLM-L6-v2",
    "paraphrase-multilingual-MiniLM-L12-v2": "sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2",
}


class _EmbeddingModelWrapper:
    """Universal wrapper that provides a single .encode() interface."""

    def __init__(self, model, backend: str, model_name: str):
        self._model = model
        self._backend = backend
        self._model_name = model_name

    def encode(self, texts: list[str], show_progress_bar: bool = False) -> np.ndarray:
        if not texts:
            return np.zeros((0, 384), dtype=np.float32)

        if self._backend == "fastembed":
            # fastembed.embed() returns a generator of float lists
            embeddings = list(self._model.embed(texts))
            return np.array(embeddings, dtype=np.float32)
        else:
            # sentence-transformers
            return self._model.encode(
                texts,
                show_progress_bar=show_progress_bar,
                convert_to_numpy=True,
            )


class ModelCache:
    """Thread-safe singleton cache for embedding models."""

    _instance = None
    _lock = threading.RLock()
    _model: Optional[_EmbeddingModelWrapper] = None
    _model_name: Optional[str] = None
    _backend_used: str = ""

    @classmethod
    def get_model(cls, model_name: str = "all-MiniLM-L6-v2") -> _EmbeddingModelWrapper:
        if model_name not in ALLOWED_MODELS:
            raise ValueError(
                f"Model '{model_name}' is not in the allowed models list. "
                f"Allowed: {', '.join(ALLOWED_MODELS)}"
            )

        with cls._lock:
            if cls._model is not None and cls._model_name == model_name:
                return cls._model

            # Try fastembed first (ONNX, ~200ms load, low memory)
            if FASTEMBED_AVAILABLE:
                fastembed_id = FASTEMBED_MODEL_MAP.get(model_name, model_name)
                logger.info(
                    "Loading embedding model (fastembed/ONNX)",
                    extra_data={"model_name": model_name, "fastembed_id": fastembed_id},
                )
                try:
                    raw_model = TextEmbedding(model_name=fastembed_id)
                    cls._model = _EmbeddingModelWrapper(raw_model, "fastembed", model_name)
                    cls._model_name = model_name
                    cls._backend_used = "fastembed"
                    logger.info(
                        "Model loaded successfully (fastembed)",
                        extra_data={"model_name": model_name},
                    )
                    return cls._model
                except Exception as e:
                    logger.warning(
                        "fastembed failed, will try sentence-transformers fallback",
                        extra_data={"error": str(e), "model_name": model_name},
                    )

            # Fallback to sentence-transformers (PyTorch, ~12s load)
            if SENTENCE_TRANSFORMERS_AVAILABLE:
                logger.info(
                    "Loading embedding model (sentence-transformers/PyTorch) – fallback",
                    extra_data={"model_name": model_name},
                )
                try:
                    raw_model = SentenceTransformer(model_name)
                    cls._model = _EmbeddingModelWrapper(raw_model, "sentence-transformers", model_name)
                    cls._model_name = model_name
                    cls._backend_used = "sentence-transformers"
                    logger.info(
                        "Model loaded successfully (sentence-transformers fallback)",
                        extra_data={"model_name": model_name},
                    )
                    return cls._model
                except Exception as e:
                    logger.error(
                        "Failed to load sentence-transformers fallback",
                        extra_data={"model_name": model_name, "error": str(e)},
                    )
                    raise RuntimeError(
                        f"Failed to load model '{model_name}': {str(e)}"
                    ) from e

            # Nothing available
            raise RuntimeError(
                "No embedding backend available. Install fastembed for fast loading (air-gapped) "
                "or sentence-transformers for PyTorch fallback."
            )

    @classmethod
    def clear(cls) -> None:
        with cls._lock:
            if cls._model is not None:
                logger.info("Clearing model cache", extra_data={"model_name": cls._model_name})
                cls._model = None
                cls._model_name = None
                cls._backend_used = ""

    @classmethod
    def is_loaded(cls) -> bool:
        with cls._lock:
            return cls._model is not None

    @classmethod
    def get_loaded_model_name(cls) -> Optional[str]:
        with cls._lock:
            return cls._model_name

    @classmethod
    def get_backend(cls) -> str:
        with cls._lock:
            return cls._backend_used


def get_embedding_model(model_name: str = "all-MiniLM-L6-v2") -> _EmbeddingModelWrapper:
    return ModelCache.get_model(model_name)
