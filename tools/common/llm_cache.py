import hashlib
import os
from typing import Optional

try:
    import redis

    REDIS_AVAILABLE = True
except ImportError:
    REDIS_AVAILABLE = False


class LLMCache:
    def __init__(self, redis_url: str = None, ttl: int = None):
        if redis_url is None:
            redis_url = os.getenv("REDIS_URL", "redis://localhost:6379/0")
        if ttl is None:
            ttl = int(os.getenv("LLM_CACHE_TTL", "3600"))

        self.redis_url = redis_url
        self.ttl = ttl
        self._redis = None

    @property
    def redis(self):
        if not REDIS_AVAILABLE:
            raise RuntimeError(
                "redis package not available. Install with: pip install redis"
            )
        if self._redis is None:
            self._redis = redis.from_url(self.redis_url)
        return self._redis

    def _generate_key(self, prompt: str, model: str) -> str:
        content = f"{model}:{prompt}"
        return f"llm:cache:{hashlib.sha256(content.encode()).hexdigest()}"

    def get(self, prompt: str, model: str) -> Optional[str]:
        key = self._generate_key(prompt, model)
        try:
            cached = self.redis.get(key)
            return cached.decode() if cached else None
        except Exception:
            return None

    def set(self, prompt: str, model: str, response: str):
        key = self._generate_key(prompt, model)
        try:
            self.redis.setex(key, self.ttl, response)
        except Exception:
            pass

    def invalidate(self, prompt: str, model: str):
        key = self._generate_key(prompt, model)
        try:
            self.redis.delete(key)
        except Exception:
            pass


_cache_instance = None


def get_llm_cache() -> LLMCache:
    global _cache_instance
    if _cache_instance is None:
        _cache_instance = LLMCache()
    return _cache_instance


def call_llm_with_cache(llm_api_url: str, llm_model: str, prompt: str) -> str:
    cache = get_llm_cache()
    cached = cache.get(prompt, llm_model)
    if cached:
        return cached

    from common.retry import call_llm_with_retry

    response = call_llm_with_retry(llm_api_url, llm_model, prompt)
    cache.set(prompt, llm_model, response)
    return response
