import threading
from typing import Optional

try:
    from sentence_transformers import SentenceTransformer

    SENTENCE_TRANSFORMERS_AVAILABLE = True
except ImportError:
    SENTENCE_TRANSFORMERS_AVAILABLE = False


class ModelCache:
    _instance = None
    _lock = threading.Lock()
    _model = None
    _model_name = None

    @classmethod
    def get_model(cls, model_name: str = "all-MiniLM-L6-v2") -> "SentenceTransformer":
        if not SENTENCE_TRANSFORMERS_AVAILABLE:
            raise RuntimeError(
                "sentence-transformers not available. Install with: pip install sentence-transformers"
            )

        if cls._model is None or cls._model_name != model_name:
            with cls._lock:
                if cls._model is None or cls._model_name != model_name:
                    cls._model = SentenceTransformer(model_name)
                    cls._model_name = model_name
        return cls._model

    @classmethod
    def clear(cls):
        with cls._lock:
            cls._model = None
            cls._model_name = None

    @classmethod
    def is_loaded(cls) -> bool:
        return cls._model is not None


def get_embedding_model(model_name: str = "all-MiniLM-L6-v2"):
    return ModelCache.get_model(model_name)
