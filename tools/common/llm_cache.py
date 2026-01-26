import hashlib
import hmac
import json
import os
import logging
from typing import Optional
from urllib.parse import urlparse

try:
    import redis

    REDIS_AVAILABLE = True
except ImportError:
    REDIS_AVAILABLE = False

logger = logging.getLogger(__name__)


class CacheError(Exception):
    """Exception raised for cache-related errors."""

    pass


class LLMCache:
    MAX_TTL = 86400  # 24 hours
    MIN_TTL = 60  # 1 minute
    ALLOWED_REDIS_HOSTS = {"localhost", "127.0.0.1", "::1"}

    def __init__(
        self, redis_url: str = None, ttl: int = None, secret_key: Optional[str] = None
    ):
        if redis_url is None:
            redis_url = os.getenv("REDIS_URL", "redis://localhost:6379/0")
        if ttl is None:
            ttl = int(os.getenv("LLM_CACHE_TTL", "3600"))

        self.redis_url = self._validate_redis_url(redis_url)
        self.ttl = self._validate_ttl(ttl)
        self.secret_key = secret_key or os.getenv("CACHE_SECRET_KEY", "")
        self._redis = None

    def _validate_redis_url(self, url: str) -> str:
        """Validate Redis URL to prevent SSRF attacks."""
        try:
            parsed = urlparse(url)

            if parsed.scheme not in ("redis", "rediss"):
                raise CacheError(f"Invalid Redis scheme: {parsed.scheme}")

            hostname = parsed.hostname or "localhost"
            if hostname not in self.ALLOWED_REDIS_HOSTS:
                raise CacheError(
                    f"Redis host '{hostname}' not in allowed list: {self.ALLOWED_REDIS_HOSTS}"
                )

            return url
        except Exception as e:
            raise CacheError(f"Invalid Redis URL: {url}") from e

    def _validate_ttl(self, ttl: int) -> int:
        """Validate TTL is within reasonable bounds."""
        if not isinstance(ttl, int):
            raise CacheError(f"TTL must be integer, got: {type(ttl)}")

        if ttl < self.MIN_TTL:
            logger.warning(f"TTL {ttl} below minimum, using {self.MIN_TTL}")
            return self.MIN_TTL

        if ttl > self.MAX_TTL:
            logger.warning(f"TTL {ttl} above maximum, using {self.MAX_TTL}")
            return self.MAX_TTL

        return ttl

    @property
    def redis(self):
        if not REDIS_AVAILABLE:
            raise RuntimeError(
                "redis package not available. Install with: pip install redis"
            )
        if self._redis is None:
            try:
                self._redis = redis.from_url(
                    self.redis_url,
                    socket_connect_timeout=5,
                    socket_timeout=5,
                    decode_responses=False,
                )
                self._redis.ping()
            except redis.ConnectionError as e:
                logger.error(f"Failed to connect to Redis: {e}")
                raise CacheError(f"Redis connection failed") from e
        return self._redis

    def _generate_key(self, prompt: str, model: str) -> str:
        content = f"{model}:{prompt}"
        return f"llm:cache:{hashlib.sha256(content.encode()).hexdigest()}"

    def _compute_signature(self, data: str) -> str:
        """Compute HMAC signature for cache integrity."""
        if not self.secret_key:
            return ""
        return hmac.new(
            self.secret_key.encode(), data.encode(), hashlib.sha256
        ).hexdigest()

    def _pack_value(self, response: str) -> str:
        """Pack response with signature for integrity checking."""
        if not self.secret_key:
            return response

        signature = self._compute_signature(response)
        return json.dumps({"data": response, "sig": signature})

    def _unpack_value(self, packed: str) -> Optional[str]:
        """Unpack and verify cached value."""
        if not self.secret_key:
            return packed

        try:
            obj = json.loads(packed)
            data = obj.get("data", "")
            signature = obj.get("sig", "")

            expected_sig = self._compute_signature(data)
            if not hmac.compare_digest(signature, expected_sig):
                logger.warning("Cache integrity check failed - signature mismatch")
                return None

            return data
        except (json.JSONDecodeError, KeyError) as e:
            logger.warning(f"Failed to unpack cached value: {e}")
            return None

    def get(self, prompt: str, model: str) -> Optional[str]:
        key = self._generate_key(prompt, model)
        try:
            cached = self.redis.get(key)
            if not cached:
                return None

            decoded = cached.decode("utf-8")
            return self._unpack_value(decoded)

        except redis.RedisError as e:
            logger.error(f"Redis error during get: {e}")
            return None
        except Exception as e:
            logger.error(f"Unexpected error during cache get: {e}")
            return None

    def set(self, prompt: str, model: str, response: str):
        key = self._generate_key(prompt, model)
        try:
            packed = self._pack_value(response)
            self.redis.setex(key, self.ttl, packed)
            logger.debug(f"Cached LLM response for key: {key}")

        except redis.RedisError as e:
            logger.error(f"Redis error during set: {e}")
        except Exception as e:
            logger.error(f"Unexpected error during cache set: {e}")

    def invalidate(self, prompt: str, model: str):
        key = self._generate_key(prompt, model)
        try:
            self.redis.delete(key)
            logger.debug(f"Invalidated cache key: {key}")
        except redis.RedisError as e:
            logger.error(f"Redis error during invalidate: {e}")
        except Exception as e:
            logger.error(f"Unexpected error during cache invalidate: {e}")


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
