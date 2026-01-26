"""
Thread-safe embedding model cache with security hardening.

Security Features:
- Proper thread-safe singleton pattern
- Memory management for large models
- Model validation and whitelisting
"""

import threading
import os
import sys
from typing import Optional

# Add parent directory to path for common imports
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from common.structured_logging import get_logger

logger = get_logger(__name__, "model_cache")

try:
    from sentence_transformers import SentenceTransformer

    SENTENCE_TRANSFORMERS_AVAILABLE = True
except ImportError:
    SENTENCE_TRANSFORMERS_AVAILABLE = False
    SentenceTransformer = None  # type: ignore

# Security: Whitelist of allowed embedding models
ALLOWED_MODELS = {
    "all-MiniLM-L6-v2",
    "all-MiniLM-L12-v2",
    "paraphrase-MiniLM-L6-v2",
    "paraphrase-multilingual-MiniLM-L12-v2",
}


class ModelCache:
    """
    Thread-safe singleton cache for embedding models.

    Security: Prevents multiple model loads and validates model names.
    """

    _instance = None
    _lock = threading.RLock()  # Reentrant lock for better thread safety
    _model: Optional["SentenceTransformer"] = None
    _model_name: Optional[str] = None

    @classmethod
    def get_model(cls, model_name: str = "all-MiniLM-L6-v2") -> "SentenceTransformer":
        """
        Get or load an embedding model with thread-safe caching.

        Args:
            model_name: Name of the model to load (must be in whitelist)

        Returns:
            Loaded SentenceTransformer model

        Raises:
            RuntimeError: If sentence-transformers is not available
            ValueError: If model_name is not in whitelist
        """
        if not SENTENCE_TRANSFORMERS_AVAILABLE:
            raise RuntimeError(
                "sentence-transformers not available. Install with: pip install sentence-transformers"
            )

        # SECURITY: Validate model name against whitelist
        if model_name not in ALLOWED_MODELS:
            raise ValueError(
                f"Model '{model_name}' is not in the allowed models list. "
                f"Allowed: {', '.join(ALLOWED_MODELS)}"
            )

        # Thread-safe double-checked locking
        with cls._lock:
            if cls._model is None or cls._model_name != model_name:
                logger.info(
                    "Loading embedding model",
                    extra={"model_name": model_name, "reload": cls._model is not None},
                )

                # Clear old model if switching
                if cls._model is not None and cls._model_name != model_name:
                    logger.info(
                        "Clearing old model", extra={"old_model": cls._model_name}
                    )
                    cls._model = None

                try:
                    cls._model = SentenceTransformer(model_name)
                    cls._model_name = model_name
                    logger.info(
                        "Model loaded successfully", extra={"model_name": model_name}
                    )
                except Exception as e:
                    logger.error(
                        "Failed to load model",
                        extra={"model_name": model_name, "error": str(e)},
                    )
                    raise RuntimeError(
                        f"Failed to load model '{model_name}': {str(e)}"
                    ) from e

        return cls._model

    @classmethod
    def clear(cls):
        """Clear the cached model to free memory."""
        with cls._lock:
            if cls._model is not None:
                logger.info(
                    "Clearing model cache", extra={"model_name": cls._model_name}
                )
                cls._model = None
                cls._model_name = None

    @classmethod
    def is_loaded(cls) -> bool:
        """Check if a model is currently loaded."""
        with cls._lock:
            return cls._model is not None

    @classmethod
    def get_loaded_model_name(cls) -> Optional[str]:
        """Get the name of the currently loaded model."""
        with cls._lock:
            return cls._model_name


def get_embedding_model(model_name: str = "all-MiniLM-L6-v2") -> "SentenceTransformer":
    """
    Get an embedding model (convenience wrapper for ModelCache.get_model).

    Args:
        model_name: Name of the model to load

    Returns:
        Loaded SentenceTransformer model
    """
    return ModelCache.get_model(model_name)
